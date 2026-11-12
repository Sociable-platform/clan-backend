package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// fakeStore is an in-memory Store. Like the real table, it rejects a second row with the same email.
type fakeStore struct {
	byEmail map[string]repository.Developer
	admins  map[string]repository.Admin // by email

	// Answers of the admin read queries, and the filters they were last called with.
	devRows     []repository.DeveloperSummary
	adminDev    repository.AdminDeveloper
	adminGames  []repository.AdminGame
	auditRows   []repository.AuditEntry
	devFilter   repository.DeveloperFilter
	auditFilter repository.AuditFilter

	// What the status changes return, and the call they were last made with.
	statusDev    repository.AdminDeveloper
	statusGame   repository.AdminGame
	statusCalls  int
	statusTarget string
	statusChange repository.StatusChange

	games    []repository.Game
	keys     []storedKey
	drafts   map[string]repository.WidgetDraft     // by game ID
	versions map[string][]repository.WidgetVersion // by game ID, oldest first
	err      error                                 // returned by every call when set
}

func newFakeStore() *fakeStore {
	return &fakeStore{byEmail: map[string]repository.Developer{}, admins: map[string]repository.Admin{}}
}

func (f *fakeStore) Ping(context.Context) error { return f.err }

func (f *fakeStore) CreateDeveloper(_ context.Context, email, hash, name string) (repository.Developer, error) {
	if f.err != nil {
		return repository.Developer{}, f.err
	}
	if _, ok := f.byEmail[email]; ok {
		return repository.Developer{}, repository.ErrEmailTaken
	}
	d := repository.Developer{ID: "dev-" + strconv.Itoa(len(f.byEmail)+1), Email: email, PasswordHash: hash, Name: name, CreatedAt: time.Now()}
	f.byEmail[email] = d
	return d, nil
}

func (f *fakeStore) DeveloperByEmail(_ context.Context, email string) (repository.Developer, error) {
	if f.err != nil {
		return repository.Developer{}, f.err
	}
	d, ok := f.byEmail[email]
	if !ok {
		return repository.Developer{}, repository.ErrNotFound
	}
	return d, nil
}

func (f *fakeStore) DeveloperByID(_ context.Context, id string) (repository.Developer, error) {
	if f.err != nil {
		return repository.Developer{}, f.err
	}
	for _, d := range f.byEmail {
		if d.ID == id {
			return d, nil
		}
	}
	return repository.Developer{}, repository.ErrNotFound
}

func newTestService(t *testing.T) (*Service, *fakeStore, ed25519.PrivateKey) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	store := newFakeStore()
	svc, err := newService(store, key, time.Hour, bcrypt.MinCost) // MinCost keeps tests fast
	if err != nil {
		t.Fatal(err)
	}
	return svc, store, key
}

// wantError fails unless err is an *httpx.Error with the given status and code.
func wantError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var e *httpx.Error
	if !errors.As(err, &e) {
		t.Fatalf("want *httpx.Error %d %s, got %v", status, code, err)
	}
	if e.Status != status || e.Code != code {
		t.Fatalf("want %d %s, got %d %s", status, code, e.Status, e.Code)
	}
}

func validRegister() RegisterInput {
	return RegisterInput{Email: "dev@example.com", Password: "correct horse", Name: "Dev One"}
}

func TestRegister_Valid(t *testing.T) {
	svc, store, _ := newTestService(t)

	d, err := svc.Register(context.Background(), RegisterInput{Email: "  Dev@Example.COM ", Password: "correct horse", Name: " Dev One "})
	if err != nil {
		t.Fatal(err)
	}
	if d.ID == "" || d.Email != "dev@example.com" || d.Name != "Dev One" {
		t.Fatalf("unexpected developer %+v", d)
	}

	stored := store.byEmail["dev@example.com"]
	if stored.PasswordHash == "" || strings.Contains(stored.PasswordHash, "correct horse") {
		t.Fatalf("password must be stored hashed, got %q", stored.PasswordHash)
	}
	if bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte("correct horse")) != nil {
		t.Fatal("stored hash is not a bcrypt hash of the password")
	}
}

func TestRegister_Validation(t *testing.T) {
	tests := []struct {
		name string
		in   RegisterInput
		code string
	}{
		{"empty email", RegisterInput{"", "correct horse", "Dev"}, "invalid_email"},
		{"no at sign", RegisterInput{"dev.example.com", "correct horse", "Dev"}, "invalid_email"},
		{"no domain dot", RegisterInput{"dev@localhost", "correct horse", "Dev"}, "invalid_email"},
		{"display name form", RegisterInput{"Dev <dev@example.com>", "correct horse", "Dev"}, "invalid_email"},
		{"two addresses", RegisterInput{"a@example.com, b@example.com", "correct horse", "Dev"}, "invalid_email"},
		{"email too long", RegisterInput{strings.Repeat("a", 250) + "@example.com", "correct horse", "Dev"}, "invalid_email"},
		{"password too short", RegisterInput{"dev@example.com", "short", "Dev"}, "invalid_password"},
		{"password empty", RegisterInput{"dev@example.com", "", "Dev"}, "invalid_password"},
		{"password too long for bcrypt", RegisterInput{"dev@example.com", strings.Repeat("a", 73), "Dev"}, "invalid_password"},
		{"name empty", RegisterInput{"dev@example.com", "correct horse", ""}, "invalid_name"},
		{"name blank", RegisterInput{"dev@example.com", "correct horse", "   "}, "invalid_name"},
		{"name too long", RegisterInput{"dev@example.com", "correct horse", strings.Repeat("n", 101)}, "invalid_name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, store, _ := newTestService(t)
			_, err := svc.Register(context.Background(), tt.in)
			wantError(t, err, http.StatusBadRequest, tt.code)
			if len(store.byEmail) != 0 {
				t.Fatal("invalid input must not create a developer")
			}
		})
	}
}

func TestRegister_PasswordLengthBoundaries(t *testing.T) {
	for _, n := range []int{8, 72} {
		svc, _, _ := newTestService(t)
		in := validRegister()
		in.Password = strings.Repeat("p", n)
		if _, err := svc.Register(context.Background(), in); err != nil {
			t.Fatalf("%d-byte password should be accepted: %v", n, err)
		}
	}
}

func TestRegister_DuplicateEmail(t *testing.T) {
	svc, _, _ := newTestService(t)
	if _, err := svc.Register(context.Background(), validRegister()); err != nil {
		t.Fatal(err)
	}

	again := validRegister()
	again.Email = "DEV@example.com" // email match ignores case
	_, err := svc.Register(context.Background(), again)
	wantError(t, err, http.StatusConflict, "email_taken")
}

func TestRegister_StoreFailureIsNotAnAPIError(t *testing.T) {
	svc, store, _ := newTestService(t)
	store.err = errors.New("connection refused")
	_, err := svc.Register(context.Background(), validRegister())
	var e *httpx.Error
	if err == nil || errors.As(err, &e) {
		t.Fatalf("want a plain internal error, got %v", err)
	}
}

func registered(t *testing.T) (*Service, ed25519.PrivateKey, Developer) {
	t.Helper()
	svc, _, key := newTestService(t)
	d, err := svc.Register(context.Background(), validRegister())
	if err != nil {
		t.Fatal(err)
	}
	return svc, key, d
}

func TestLogin_ReturnsDeveloperToken(t *testing.T) {
	svc, key, dev := registered(t)
	now := time.Unix(1_800_000_000, 0)
	svc.now = func() time.Time { return now }

	s, err := svc.Login(context.Background(), LoginInput{Email: " DEV@example.com ", Password: "correct horse"})
	if err != nil {
		t.Fatal(err)
	}
	if s.ExpiresIn != 3600 {
		t.Fatalf("expiresIn %d, want 3600", s.ExpiresIn)
	}

	claims, err := jwt.Verify(key.Public().(ed25519.PublicKey), s.Token, jwt.TypeDeveloper, now)
	if err != nil {
		t.Fatalf("token does not verify as a developer token: %v", err)
	}
	if claims.Subject != dev.ID || claims.Type != jwt.TypeDeveloper || claims.GameID != "" {
		t.Fatalf("unexpected claims %+v", claims)
	}
	if claims.ExpiresAt != now.Add(time.Hour).Unix() {
		t.Fatalf("exp %d, want %d", claims.ExpiresAt, now.Add(time.Hour).Unix())
	}
	if _, err := jwt.Verify(key.Public().(ed25519.PublicKey), s.Token, jwt.TypePlayer, now); !errors.Is(err, jwt.ErrType) {
		t.Fatalf("developer token must not verify as a player token, got %v", err)
	}
}

func TestLogin_WrongPasswordOrUnknownEmail(t *testing.T) {
	svc, _, _ := registered(t)
	tests := []struct {
		name, email, password string
	}{
		{"wrong password", "dev@example.com", "not the password"},
		{"empty password", "dev@example.com", ""},
		{"password over bcrypt limit", "dev@example.com", strings.Repeat("a", 100)},
		{"unknown email", "nobody@example.com", "correct horse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Login(context.Background(), LoginInput{Email: tt.email, Password: tt.password})
			wantError(t, err, http.StatusUnauthorized, "invalid_credentials")
		})
	}
}

func TestLogin_StoreFailureIsNotAnAPIError(t *testing.T) {
	svc, store, _ := newTestService(t)
	store.err = errors.New("connection refused")
	_, err := svc.Login(context.Background(), LoginInput{Email: "dev@example.com", Password: "correct horse"})
	var e *httpx.Error
	if err == nil || errors.As(err, &e) {
		t.Fatalf("want a plain internal error, got %v", err)
	}
}

func TestAuthenticateDeveloper(t *testing.T) {
	svc, key, dev := registered(t)
	now := time.Now()
	s, err := svc.Login(context.Background(), LoginInput{Email: "dev@example.com", Password: "correct horse"})
	if err != nil {
		t.Fatal(err)
	}

	sign := func(k ed25519.PrivateKey, c jwt.Claims, ttl time.Duration) string {
		tok, err := jwt.Sign(k, c, ttl, now)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)

	tests := []struct {
		name  string
		token string
		want  bool
	}{
		{"login token", s.Token, true},
		{"player token", sign(key, jwt.Claims{Subject: "u_42", GameID: "g1", Type: jwt.TypePlayer}, time.Hour), false},
		{"expired", sign(key, jwt.Claims{Subject: dev.ID, Type: jwt.TypeDeveloper}, -time.Minute), false},
		{"signed by another key", sign(otherKey, jwt.Claims{Subject: dev.ID, Type: jwt.TypeDeveloper}, time.Hour), false},
		{"garbage", "not-a-token", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := svc.AuthenticateDeveloper(tt.token)
			if tt.want {
				if err != nil || id != dev.ID {
					t.Fatalf("want id %q, got %q, %v", dev.ID, id, err)
				}
				return
			}
			wantError(t, err, http.StatusUnauthorized, "unauthorized")
		})
	}
}

func TestMe(t *testing.T) {
	svc, _, dev := registered(t)

	got, err := svc.Me(context.Background(), dev.ID)
	if err != nil || got.ID != dev.ID || got.Email != "dev@example.com" {
		t.Fatalf("got %+v, %v", got, err)
	}

	_, err = svc.Me(context.Background(), "dev-gone")
	wantError(t, err, http.StatusUnauthorized, "unauthorized")
}
