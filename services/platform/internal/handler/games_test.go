package handler

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

// authedFake is a fakeService that accepts the token "good" as developer d1.
func authedFake() fakeService {
	return fakeService{auth: func(token string) (string, error) {
		if token != "good" {
			return "", httpx.NewError(http.StatusUnauthorized, "unauthorized", "no")
		}
		return "d1", nil
	}}
}

func TestRegisterGame_Created(t *testing.T) {
	created := time.Date(2026, 11, 7, 9, 30, 0, 0, time.UTC)
	var gotDev string
	var gotIn service.RegisterGameInput
	svc := authedFake()
	svc.registerGame = func(dev string, in service.RegisterGameInput) (service.Game, error) {
		gotDev, gotIn = dev, in
		return service.Game{ID: "g1", Name: in.Name, Description: in.Description, CreatedAt: created}, nil
	}

	rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games",
		`{"name":"Space Raiders","description":"Browser shooter"}`, "good")

	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if gotDev != "d1" {
		t.Fatalf("owner must come from the token, got %q", gotDev)
	}
	if gotIn != (service.RegisterGameInput{Name: "Space Raiders", Description: "Browser shooter"}) {
		t.Fatalf("service got %+v", gotIn)
	}
	want := `{"id":"g1","name":"Space Raiders","description":"Browser shooter","allowedOrigins":[],"createdAt":"2026-11-07T09:30:00Z"}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestRegisterGame_OwnerCannotBeChosenInTheBody(t *testing.T) {
	svc := authedFake()
	svc.registerGame = func(string, service.RegisterGameInput) (service.Game, error) {
		t.Fatal("service must not be called")
		return service.Game{}, nil
	}
	for _, body := range []string{
		`{"name":"G","developerId":"someone-else"}`,
		`{"name":"G","developer_id":"someone-else"}`,
	} {
		rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games", body, "good")
		if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_json" {
			t.Fatalf("body %s: got %d %s", body, rec.Code, rec.Body.String())
		}
	}
}

func TestRegisterGame_Errors(t *testing.T) {
	svc := authedFake()
	svc.registerGame = func(_ string, in service.RegisterGameInput) (service.Game, error) {
		return service.Game{}, httpx.NewError(http.StatusBadRequest, "invalid_name", "bad")
	}
	tests := []struct {
		name, body, code string
	}{
		{"validation failure from service", `{"name":""}`, "invalid_name"},
		{"not json", `nope`, "invalid_json"},
		{"empty body", ``, "invalid_json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games", tt.body, "good")
			if rec.Code != http.StatusBadRequest || errorCode(t, rec) != tt.code {
				t.Fatalf("got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestListGames(t *testing.T) {
	var gotDev string
	svc := authedFake()
	svc.listGames = func(dev string) ([]service.Game, error) {
		gotDev = dev
		return []service.Game{
			{ID: "g2", Name: "Two", CreatedAt: time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)},
			{ID: "g1", Name: "One", Description: "first", CreatedAt: time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)},
		}, nil
	}

	rec := call(t, svc, discardLog(), http.MethodGet, "/api/v1/games", "", "good")

	if rec.Code != http.StatusOK || gotDev != "d1" {
		t.Fatalf("status %d, developer %q: %s", rec.Code, gotDev, rec.Body.String())
	}
	var body struct {
		Games []struct{ ID, Name, Description string } `json:"games"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Games) != 2 || body.Games[0].ID != "g2" {
		t.Fatalf("body %s (%v)", rec.Body.String(), err)
	}
}

func TestListGames_EmptyIsAnEmptyArray(t *testing.T) {
	svc := authedFake()
	svc.listGames = func(string) ([]service.Game, error) { return nil, nil }

	rec := call(t, svc, discardLog(), http.MethodGet, "/api/v1/games", "", "good")

	if rec.Body.String() != `{"games":[]}`+"\n" {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestGames_RequireDeveloperToken(t *testing.T) {
	svc := authedFake() // registerGame and listGames are nil: any call would panic
	for _, route := range []struct{ method, body string }{
		{http.MethodGet, ""},
		{http.MethodPost, `{"name":"G"}`},
	} {
		for _, token := range []string{"", "bad"} {
			rec := call(t, svc, discardLog(), route.method, "/api/v1/games", route.body, token)
			if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "unauthorized" {
				t.Fatalf("%s with token %q: got %d %s", route.method, token, rec.Code, rec.Body.String())
			}
		}
	}
}

// TestGamesOwnershipFlow uses the real handler and service with two developers.
func TestGamesOwnershipFlow(t *testing.T) {
	svc := newRealService(t)
	register := func(email string) string {
		t.Helper()
		creds := `"email":"` + email + `","password":"correct horse"`
		rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/developers/register", `{`+creds+`,"name":"Dev"}`, "")
		if rec.Code != http.StatusCreated {
			t.Fatalf("register %s: %d %s", email, rec.Code, rec.Body.String())
		}
		rec = call(t, svc, discardLog(), http.MethodPost, "/api/v1/developers/login", `{`+creds+`}`, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("login %s: %d %s", email, rec.Code, rec.Body.String())
		}
		var l loginResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &l)
		return l.Token
	}
	alice, bob := register("alice@example.com"), register("bob@example.com")

	list := func(token string) []gameResponse {
		t.Helper()
		rec := call(t, svc, discardLog(), http.MethodGet, "/api/v1/games", "", token)
		if rec.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		var out gameListResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Games
	}

	rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games", `{"name":"Space Raiders","description":"shooter"}`, alice)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register game: %d %s", rec.Code, rec.Body.String())
	}
	var created gameResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	if games := list(alice); len(games) != 1 || games[0].ID != created.ID {
		t.Fatalf("alice should see her game, got %+v", games)
	}
	if games := list(bob); len(games) != 0 {
		t.Fatalf("bob must not see alice's game, got %+v", games)
	}

	// Bob's own game never mixes into Alice's list.
	if rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games", `{"name":"Bob's Game"}`, bob); rec.Code != http.StatusCreated {
		t.Fatalf("bob register game: %d", rec.Code)
	}
	if games := list(alice); len(games) != 1 || games[0].Name != "Space Raiders" {
		t.Fatalf("alice's list changed: %+v", games)
	}
	if games := list(bob); len(games) != 1 || games[0].Name != "Bob's Game" {
		t.Fatalf("bob's list: %+v", games)
	}
}

// newRealService builds the real service on an in-memory store.
func newRealService(t *testing.T) *service.Service {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := service.New(&memStore{}, key, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}
