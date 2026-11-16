package handler

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

const (
	registerPath = "/api/v1/developers/register"
	verifyPath   = "/api/v1/developers/register/verify"
	resendPath   = "/api/v1/developers/register/resend"

	registerBody = `{"email":"dev@example.com","password":"correct horse","name":"Dev One"}`
)

// verifyingFake is a fake service with email verification on; tests set the calls they need.
func verifyingFake() fakeService {
	return fakeService{emailVerification: true}
}

func TestRegisterDeveloper_WithVerificationAnswers202AndCreatesNothing(t *testing.T) {
	var got service.RegisterInput
	svc := verifyingFake()
	svc.requestRegistration = func(in service.RegisterInput) error { got = in; return nil }
	// svc.register stays nil: calling it would panic, so this also proves no account is created.
	post := resetServer(svc)

	rec := post(registerPath, registerBody, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got.Email != "dev@example.com" || got.Password != "correct horse" || got.Name != "Dev One" {
		t.Fatalf("service got %+v", got)
	}
	if !strings.Contains(rec.Body.String(), `"message"`) || strings.Contains(rec.Body.String(), `"id"`) {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestRegisterDeveloper_WithVerificationPassesErrorsOn(t *testing.T) {
	svc := verifyingFake()
	svc.requestRegistration = func(service.RegisterInput) error {
		return httpx.NewError(http.StatusBadRequest, "invalid_password", "Password must be 8 to 72 characters")
	}
	post := resetServer(svc)

	rec := post(registerPath, registerBody, "")
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_password" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestRegisterDeveloper_WithVerificationLimitedPerEmail(t *testing.T) {
	calls := 0
	svc := verifyingFake()
	svc.requestRegistration = func(service.RegisterInput) error { calls++; return nil }
	svc.resendRegistrationCode = func(string) error { calls++; return nil }
	post := resetServer(svc)

	// Register and "send a new code" share the count of an email address.
	for i := 0; i < registerPerEmail; i++ {
		path, body := registerPath, registerBody
		if i%2 == 1 {
			path, body = resendPath, `{"email":" DEV@example.com "}`
		}
		if rec := post(path, body, "10.0.0."+string(rune('1'+i))); rec.Code != http.StatusAccepted {
			t.Fatalf("request %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	for _, c := range []struct{ path, body string }{{registerPath, registerBody}, {resendPath, `{"email":"dev@example.com"}`}} {
		rec := post(c.path, c.body, "10.0.0.9")
		if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "rate_limited" {
			t.Fatalf("%s over the limit: %d %s", c.path, rec.Code, rec.Body.String())
		}
	}
	if calls != registerPerEmail {
		t.Fatalf("the service was called %d times, want %d", calls, registerPerEmail)
	}
	// Another address is not affected.
	other := `{"email":"other@example.com","password":"correct horse","name":"Other"}`
	if rec := post(registerPath, other, "10.0.0.9"); rec.Code != http.StatusAccepted {
		t.Fatalf("another email: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRegisterDeveloper_WithoutVerificationIsNotLimited(t *testing.T) {
	svc := fakeService{register: func(in service.RegisterInput) (service.Developer, error) {
		return service.Developer{ID: "d1", Email: in.Email, Name: in.Name, Status: "active"}, nil
	}}
	post := resetServer(svc)

	for i := 0; i < registerPerIP+5; i++ {
		if rec := post(registerPath, registerBody, ""); rec.Code != http.StatusCreated {
			t.Fatalf("request %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
}

func TestVerifyRegistration(t *testing.T) {
	created := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	var got service.VerifyRegistrationInput
	svc := verifyingFake()
	svc.verifyRegistration = func(in service.VerifyRegistrationInput) (service.Developer, error) {
		got = in
		if in.Code != "123456" {
			return service.Developer{}, httpx.NewError(http.StatusBadRequest, "invalid_verification_code", "no")
		}
		return service.Developer{ID: "d1", Email: in.Email, Name: "Dev One", CreatedAt: created, Status: "active"}, nil
	}
	post := resetServer(svc)

	rec := post(verifyPath, `{"email":"dev@example.com","code":"123456"}`, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got.Email != "dev@example.com" || got.Code != "123456" {
		t.Fatalf("service got %+v", got)
	}
	for _, want := range []string{`"id":"d1"`, `"email":"dev@example.com"`, `"status":"active"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("body %s lacks %s", rec.Body.String(), want)
		}
	}

	rec = post(verifyPath, `{"email":"dev@example.com","code":"000000"}`, "")
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_verification_code" {
		t.Fatalf("wrong code: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(verifyPath, `not json`, ""); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_json" {
		t.Fatalf("bad body: %d %s", rec.Code, rec.Body.String())
	}
}

func TestVerifyRegistration_LimitedPerIP(t *testing.T) {
	svc := verifyingFake()
	svc.verifyRegistration = func(service.VerifyRegistrationInput) (service.Developer, error) {
		return service.Developer{}, httpx.NewError(http.StatusBadRequest, "invalid_verification_code", "no")
	}
	post := resetServer(svc)

	body := `{"email":"dev@example.com","code":"000000"}`
	for i := 0; i < verifyPerIP; i++ {
		if rec := post(verifyPath, body, "203.0.113.7"); rec.Code != http.StatusBadRequest {
			t.Fatalf("request %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	if rec := post(verifyPath, body, "203.0.113.7"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over the limit: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(verifyPath, body, "203.0.113.8"); rec.Code != http.StatusBadRequest {
		t.Fatalf("another address: %d %s", rec.Code, rec.Body.String())
	}
}

func TestResendRegistrationCode(t *testing.T) {
	var got string
	svc := verifyingFake()
	svc.resendRegistrationCode = func(email string) error {
		got = email
		if email == "soon@example.com" {
			return httpx.NewError(http.StatusTooManyRequests, "rate_limited", "A code was just sent.")
		}
		return nil
	}
	post := resetServer(svc)

	rec := post(resendPath, `{"email":"dev@example.com"}`, "")
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"message"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got != "dev@example.com" {
		t.Fatalf("service got %q", got)
	}
	rec = post(resendPath, `{"email":"soon@example.com"}`, "")
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "rate_limited" {
		t.Fatalf("too soon: %d %s", rec.Code, rec.Body.String())
	}
}

func TestVerificationRoutesAre404WhileVerificationIsOff(t *testing.T) {
	post := resetServer(fakeService{}) // off; a call into the service would panic
	for path, body := range map[string]string{
		verifyPath: `{"email":"dev@example.com","code":"123456"}`,
		resendPath: `{"email":"dev@example.com"}`,
	} {
		rec := post(path, body, "")
		if rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

// codeCatcher is the service's VerificationSender in the flow test below.
type codeCatcher struct {
	code              string
	alreadyRegistered int
}

func (c *codeCatcher) SendVerificationCode(_, _, code string) { c.code = code }
func (c *codeCatcher) SendAlreadyRegistered(_, _ string)      { c.alreadyRegistered++ }

// TestRegistrationWithVerificationFlow runs register -> code -> login through the real handler
// and service.
func TestRegistrationWithVerificationFlow(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	store := &memStore{}
	svc, err := service.New(store, key, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	catcher := &codeCatcher{}
	svc.WithEmailVerification(catcher)
	post := resetServer(svc)
	const login = `{"email":"dev@example.com","password":"correct horse"}`

	if rec := post(registerPath, registerBody, ""); rec.Code != http.StatusAccepted {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	if len(store.devs) != 0 || catcher.code == "" {
		t.Fatalf("%d developers before the code, code %q", len(store.devs), catcher.code)
	}
	// No account yet: logging in fails like for an unknown email.
	if rec := post("/api/v1/developers/login", login, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("login before the code: %d %s", rec.Code, rec.Body.String())
	}

	wrong := "000000"
	if catcher.code == wrong {
		wrong = "000001"
	}
	rec := post(verifyPath, `{"email":"dev@example.com","code":"`+wrong+`"}`, "")
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_verification_code" {
		t.Fatalf("wrong code: %d %s", rec.Code, rec.Body.String())
	}

	rec = post(verifyPath, `{"email":"Dev@Example.com","code":"`+catcher.code+`"}`, "")
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"email":"dev@example.com"`) {
		t.Fatalf("verify: %d %s", rec.Code, rec.Body.String())
	}
	if len(store.devs) != 1 || len(store.pending) != 0 {
		t.Fatalf("%d developers, %d pending after the code", len(store.devs), len(store.pending))
	}
	if rec := post("/api/v1/developers/login", login, ""); rec.Code != http.StatusOK {
		t.Fatalf("login after the code: %d %s", rec.Code, rec.Body.String())
	}

	// Registering the same address again answers the same, creates nothing and tells the owner.
	if rec := post(registerPath, registerBody, ""); rec.Code != http.StatusAccepted {
		t.Fatalf("register again: %d %s", rec.Code, rec.Body.String())
	}
	if len(store.devs) != 1 || len(store.pending) != 0 || catcher.alreadyRegistered != 1 {
		t.Fatalf("%d developers, %d pending, %d notices", len(store.devs), len(store.pending), catcher.alreadyRegistered)
	}
}
