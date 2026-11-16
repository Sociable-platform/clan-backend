package service

import (
	"context"
	"net/http"
	"testing"
	"time"
)

type sentCode struct{ to, name, code string }

// recordingVerification keeps the registration emails instead of sending them.
type recordingVerification struct {
	codes             []sentCode
	alreadyRegistered []string // addresses told that someone tried to register with them
}

func (r *recordingVerification) SendVerificationCode(to, name, code string) {
	r.codes = append(r.codes, sentCode{to, name, code})
}

func (r *recordingVerification) SendAlreadyRegistered(to, _ string) {
	r.alreadyRegistered = append(r.alreadyRegistered, to)
}

// lastCode returns the code of the newest email.
func (r *recordingVerification) lastCode(t *testing.T) string {
	t.Helper()
	if len(r.codes) == 0 {
		t.Fatal("no verification code was sent")
	}
	return r.codes[len(r.codes)-1].code
}

// verifyingService returns a service with email verification on, a clock the test moves, and a
// sender that records the emails.
func verifyingService(t *testing.T) (*Service, *fakeStore, *recordingVerification, *time.Time) {
	t.Helper()
	svc, store, _ := newTestService(t)
	now := time.Unix(1_800_000_000, 0)
	svc.now = func() time.Time { return now }
	sender := &recordingVerification{}
	svc.WithEmailVerification(sender)
	return svc, store, sender, &now
}

// wrongCode returns a 6-digit code that is not code.
func wrongCode(code string) string {
	if code == "000000" {
		return "000001"
	}
	return "000000"
}

func verify(svc *Service, code string) (Developer, error) {
	return svc.VerifyRegistration(context.Background(), VerifyRegistrationInput{Email: "dev@example.com", Code: code})
}

func TestEmailVerificationIsOffByDefault(t *testing.T) {
	svc, store, _ := newTestService(t)
	if svc.EmailVerificationOn() {
		t.Fatal("verification must be off until WithEmailVerification is called")
	}
	// Off: Register creates the account at once, as before.
	if _, err := svc.Register(context.Background(), validRegister()); err != nil {
		t.Fatal(err)
	}
	if len(store.byEmail) != 1 || len(store.pendingRegs) != 0 {
		t.Fatalf("%d developers, %d pending", len(store.byEmail), len(store.pendingRegs))
	}
	if err := svc.RequestRegistration(context.Background(), validRegister()); err == nil {
		t.Fatal("RequestRegistration must fail while verification is off")
	}
}

func TestRequestRegistration_StoresItPendingAndSendsACode(t *testing.T) {
	svc, store, sender, _ := verifyingService(t)
	if !svc.EmailVerificationOn() {
		t.Fatal("verification should be on")
	}

	in := RegisterInput{Email: "  Dev@Example.com ", Password: "correct horse", Name: "  Dev One "}
	if err := svc.RequestRegistration(context.Background(), in); err != nil {
		t.Fatal(err)
	}

	// No developer yet.
	if len(store.byEmail) != 0 {
		t.Fatalf("%d developers exist before the code was entered", len(store.byEmail))
	}
	if len(sender.codes) != 1 {
		t.Fatalf("%d codes sent, want 1", len(sender.codes))
	}
	got := sender.codes[0]
	if got.to != "dev@example.com" || got.name != "Dev One" {
		t.Fatalf("sent to %q (%q)", got.to, got.name)
	}
	if !isVerificationCode(got.code) {
		t.Fatalf("code %q is not 6 digits", got.code)
	}

	p, ok := store.pendingRegs["dev@example.com"]
	if !ok {
		t.Fatal("no pending registration stored")
	}
	if p.Name != "Dev One" || p.Attempts != 0 {
		t.Fatalf("pending %+v", p)
	}
	// Neither the password nor the code is stored as it is.
	if p.PasswordHash == "correct horse" || p.CodeHash == got.code || p.CodeHash != hashVerificationCode("dev@example.com", got.code) {
		t.Fatalf("pending holds a plain secret or a wrong hash: %+v", p)
	}
	if want := svc.now().Add(15 * time.Minute); !p.ExpiresAt.Equal(want) {
		t.Fatalf("expires at %v, want %v", p.ExpiresAt, want)
	}
}

func TestRequestRegistration_ValidatesLikeRegister(t *testing.T) {
	svc, store, sender, _ := verifyingService(t)
	ctx := context.Background()
	for code, in := range map[string]RegisterInput{
		"invalid_email":    {Email: "nope", Password: "correct horse", Name: "Dev"},
		"invalid_password": {Email: "dev@example.com", Password: "short", Name: "Dev"},
		"invalid_name":     {Email: "dev@example.com", Password: "correct horse", Name: "  "},
	} {
		wantError(t, svc.RequestRegistration(ctx, in), http.StatusBadRequest, code)
	}
	if len(store.pendingRegs) != 0 || len(sender.codes) != 0 {
		t.Fatalf("%d pending, %d codes after invalid input", len(store.pendingRegs), len(sender.codes))
	}
}

func TestRequestRegistration_AnExistingAccountLooksTheSame(t *testing.T) {
	svc, store, sender, _ := verifyingService(t)
	ctx := context.Background()
	if _, err := svc.Register(ctx, validRegister()); err != nil { // the account already exists
		t.Fatal(err)
	}

	in := RegisterInput{Email: "DEV@example.com", Password: "another password", Name: "Someone Else"}
	if err := svc.RequestRegistration(ctx, in); err != nil {
		t.Fatalf("an address with an account must not be an error, got %v", err)
	}

	if len(store.pendingRegs) != 0 || len(sender.codes) != 0 {
		t.Fatalf("%d pending, %d codes for an address that has an account", len(store.pendingRegs), len(sender.codes))
	}
	if len(sender.alreadyRegistered) != 1 || sender.alreadyRegistered[0] != "dev@example.com" {
		t.Fatalf("the account's owner was not told: %v", sender.alreadyRegistered)
	}
	// The account is untouched: the old password still works.
	if _, err := login(svc, "correct horse"); err != nil {
		t.Fatalf("the existing account changed: %v", err)
	}
}

func TestVerifyRegistration_CreatesTheDeveloperOnce(t *testing.T) {
	svc, store, sender, _ := verifyingService(t)
	if err := svc.RequestRegistration(context.Background(), validRegister()); err != nil {
		t.Fatal(err)
	}
	code := sender.lastCode(t)

	d, err := svc.VerifyRegistration(context.Background(), VerifyRegistrationInput{Email: " Dev@Example.com ", Code: " " + code + " "})
	if err != nil {
		t.Fatal(err)
	}
	if d.ID == "" || d.Email != "dev@example.com" || d.Name != "Dev One" || d.Status != "active" {
		t.Fatalf("developer %+v", d)
	}
	if len(store.pendingRegs) != 0 {
		t.Fatal("the pending registration was not removed")
	}
	if _, err := login(svc, "correct horse"); err != nil {
		t.Fatalf("the new developer cannot log in: %v", err)
	}

	// The code works once.
	_, err = verify(svc, code)
	wantError(t, err, http.StatusBadRequest, "invalid_verification_code")
	if len(store.byEmail) != 1 {
		t.Fatalf("%d developers, want 1", len(store.byEmail))
	}
}

func TestVerifyRegistration_RefusesBadCodes(t *testing.T) {
	svc, store, sender, now := verifyingService(t)
	if err := svc.RequestRegistration(context.Background(), validRegister()); err != nil {
		t.Fatal(err)
	}
	code := sender.lastCode(t)

	for name, bad := range map[string]string{
		"empty":      "",
		"too short":  "12345",
		"too long":   "1234567",
		"not digits": "12a456",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := verify(svc, bad)
			wantError(t, err, http.StatusBadRequest, "invalid_verification_code")
		})
	}
	// A malformed code is not an attempt.
	if got := store.pendingRegs["dev@example.com"].Attempts; got != 0 {
		t.Fatalf("%d attempts counted for malformed codes", got)
	}

	t.Run("another address", func(t *testing.T) {
		_, err := svc.VerifyRegistration(context.Background(), VerifyRegistrationInput{Email: "other@example.com", Code: code})
		wantError(t, err, http.StatusBadRequest, "invalid_verification_code")
	})

	t.Run("expired", func(t *testing.T) {
		*now = now.Add(15 * time.Minute)
		_, err := verify(svc, code)
		wantError(t, err, http.StatusBadRequest, "invalid_verification_code")
		if len(store.byEmail) != 0 {
			t.Fatal("an expired code created a developer")
		}
	})
}

func TestVerifyRegistration_FiveWrongCodesCancelTheRegistration(t *testing.T) {
	svc, store, sender, _ := verifyingService(t)
	if err := svc.RequestRegistration(context.Background(), validRegister()); err != nil {
		t.Fatal(err)
	}
	code := sender.lastCode(t)
	wrong := wrongCode(code)

	for i := 1; i <= 4; i++ {
		_, err := verify(svc, wrong)
		wantError(t, err, http.StatusBadRequest, "invalid_verification_code")
		if got := store.pendingRegs["dev@example.com"].Attempts; got != i {
			t.Fatalf("after %d wrong codes, %d are counted", i, got)
		}
	}
	_, err := verify(svc, wrong)
	wantError(t, err, http.StatusBadRequest, "verification_attempts_exceeded")
	if len(store.pendingRegs) != 0 {
		t.Fatal("the registration was not cancelled")
	}

	// The right code is too late now.
	_, err = verify(svc, code)
	wantError(t, err, http.StatusBadRequest, "invalid_verification_code")
	if len(store.byEmail) != 0 {
		t.Fatal("a cancelled registration created a developer")
	}
}

func TestVerifyRegistration_TheRightCodeAfterWrongOnesStillWorks(t *testing.T) {
	svc, _, sender, _ := verifyingService(t)
	if err := svc.RequestRegistration(context.Background(), validRegister()); err != nil {
		t.Fatal(err)
	}
	code := sender.lastCode(t)
	for i := 0; i < 4; i++ {
		if _, err := verify(svc, wrongCode(code)); err == nil {
			t.Fatal("a wrong code was accepted")
		}
	}
	if _, err := verify(svc, code); err != nil {
		t.Fatalf("the right code on the fifth try: %v", err)
	}
}

func TestVerifyRegistration_AccountAppearedMeanwhile(t *testing.T) {
	svc, store, sender, _ := verifyingService(t)
	ctx := context.Background()
	if err := svc.RequestRegistration(ctx, validRegister()); err != nil {
		t.Fatal(err)
	}
	// The same address gets an account by another way before the code is entered.
	if _, err := store.CreateDeveloper(ctx, "dev@example.com", "hash", "Other"); err != nil {
		t.Fatal(err)
	}
	_, err := verify(svc, sender.lastCode(t))
	wantError(t, err, http.StatusConflict, "email_taken")
}

func TestRequestRegistration_AgainReplacesTheCode(t *testing.T) {
	svc, store, sender, _ := verifyingService(t)
	ctx := context.Background()
	if err := svc.RequestRegistration(ctx, validRegister()); err != nil {
		t.Fatal(err)
	}
	first := sender.lastCode(t)
	if _, err := verify(svc, wrongCode(first)); err == nil {
		t.Fatal("a wrong code was accepted")
	}

	again := RegisterInput{Email: "dev@example.com", Password: "a second password", Name: "Dev Two"}
	if err := svc.RequestRegistration(ctx, again); err != nil {
		t.Fatal(err)
	}
	second := sender.lastCode(t)
	if len(store.pendingRegs) != 1 || store.pendingRegs["dev@example.com"].Attempts != 0 {
		t.Fatalf("pending %+v", store.pendingRegs)
	}
	if first != second {
		// (They are equal once in a million runs; then the next check proves nothing, and that is fine.)
		_, err := verify(svc, first)
		wantError(t, err, http.StatusBadRequest, "invalid_verification_code")
	}

	d, err := verify(svc, second)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "Dev Two" {
		t.Fatalf("the developer was created from the first registration: %+v", d)
	}
	if _, err := login(svc, "a second password"); err != nil {
		t.Fatalf("the password of the second registration does not work: %v", err)
	}
}

func TestResendRegistrationCode(t *testing.T) {
	svc, store, sender, now := verifyingService(t)
	ctx := context.Background()
	if err := svc.RequestRegistration(ctx, validRegister()); err != nil {
		t.Fatal(err)
	}
	first := sender.lastCode(t)
	if _, err := verify(svc, wrongCode(first)); err == nil {
		t.Fatal("a wrong code was accepted")
	}

	// Too soon.
	*now = now.Add(59 * time.Second)
	wantError(t, svc.ResendRegistrationCode(ctx, "dev@example.com"), http.StatusTooManyRequests, "rate_limited")
	if len(sender.codes) != 1 {
		t.Fatalf("%d codes sent, want 1", len(sender.codes))
	}

	*now = now.Add(time.Second)
	if err := svc.ResendRegistrationCode(ctx, " Dev@Example.com "); err != nil {
		t.Fatal(err)
	}
	if len(sender.codes) != 2 || sender.codes[1].to != "dev@example.com" || sender.codes[1].name != "Dev One" {
		t.Fatalf("codes %+v", sender.codes)
	}
	p := store.pendingRegs["dev@example.com"]
	if p.Attempts != 0 || !p.LastSentAt.Equal(*now) || !p.ExpiresAt.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("pending after resend: %+v", p)
	}
	second := sender.lastCode(t)
	if first != second {
		_, err := verify(svc, first)
		wantError(t, err, http.StatusBadRequest, "invalid_verification_code")
	}
	if _, err := verify(svc, second); err != nil {
		t.Fatalf("the new code does not work: %v", err)
	}
}

func TestResendRegistrationCode_NothingPendingLooksTheSame(t *testing.T) {
	svc, _, sender, now := verifyingService(t)
	ctx := context.Background()

	if err := svc.ResendRegistrationCode(ctx, "nobody@example.com"); err != nil {
		t.Fatalf("an address without a pending registration must not be an error, got %v", err)
	}
	wantError(t, svc.ResendRegistrationCode(ctx, "nope"), http.StatusBadRequest, "invalid_email")

	// An expired registration counts as none.
	if err := svc.RequestRegistration(ctx, validRegister()); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(15 * time.Minute)
	if err := svc.ResendRegistrationCode(ctx, "dev@example.com"); err != nil {
		t.Fatal(err)
	}
	if len(sender.codes) != 1 {
		t.Fatalf("%d codes sent, want only the first", len(sender.codes))
	}
}

func TestNewVerificationCode(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		code, err := newVerificationCode()
		if err != nil {
			t.Fatal(err)
		}
		if !isVerificationCode(code) {
			t.Fatalf("code %q is not 6 digits", code)
		}
		seen[code] = true
	}
	if len(seen) < 190 {
		t.Fatalf("only %d different codes in 200", len(seen))
	}
}

func TestHashVerificationCodeDependsOnTheEmail(t *testing.T) {
	if hashVerificationCode("a@example.com", "123456") == hashVerificationCode("b@example.com", "123456") {
		t.Fatal("the same code must hash differently for another address")
	}
}
