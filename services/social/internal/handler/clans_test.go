package handler

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/auth"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/service"
)

// The other fakes must satisfy the Service interface too.
func (f fakeService) ListClans(context.Context, string, string) ([]service.Clan, error) {
	return []service.Clan{}, f.err
}

func (s *recordingService) ListClans(context.Context, string, string) ([]service.Clan, error) {
	return []service.Clan{}, s.err
}

// clanService remembers which game and search text the handler asked for.
type clanService struct {
	fakeService
	clans     []service.Clan
	err       error
	gameID, q string
}

func (s *clanService) ListClans(_ context.Context, gameID, q string) ([]service.Clan, error) {
	s.gameID, s.q = gameID, q
	return s.clans, s.err
}

const tokenGame = "a6c1e2f0-1111-4222-8333-944455556666"

// callClans sends GET /api/v1/clans with a real player token for tokenGame.
func callClans(t *testing.T, svc Service, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	tok, err := jwt.Sign(priv, jwt.Claims{Subject: "u_42", GameID: tokenGame, Name: "Ali", Type: jwt.TypePlayer}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New("social", svc, auth.RequirePlayer(pub, time.Now), testLog()).Register(mux)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/clans"+rawQuery, nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

func TestListClansUsesGameFromTokenAndQFromURL(t *testing.T) {
	svc := &clanService{clans: []service.Clan{{ID: "c-1", Name: "Wolves", MemberCount: 3, MaxMembers: 10}}}
	// a game_id in the URL must be ignored: only the token decides the game
	rec := callClans(t, svc, "?q="+url.QueryEscape("Wol f")+"&game_id=99999999-9999-4999-8999-999999999999")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if svc.gameID != tokenGame || svc.q != "Wol f" {
		t.Fatalf("service got game=%q q=%q", svc.gameID, svc.q)
	}
	want := `{"clans":[{"id":"c-1","name":"Wolves","memberCount":3,"maxMembers":10}]}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("body\n got %s\nwant %s", got, want)
	}
}

func TestListClansEmptyIsEmptyArray(t *testing.T) {
	rec := callClans(t, &clanService{clans: []service.Clan{}}, "")
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != `{"clans":[]}` {
		t.Fatalf("status %d, body %s (want {\"clans\":[]}, not null)", rec.Code, got)
	}
}

func TestListClansBadQuery(t *testing.T) {
	svc := &clanService{err: httpx.NewError(http.StatusBadRequest, "invalid_query", "Search text must be at most 50 characters")}
	rec := callClans(t, svc, "?q=x")
	var body httpx.ErrorBody
	if rec.Code != http.StatusBadRequest || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Error.Code != "invalid_query" {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestListClansNeedsAToken(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	mux := http.NewServeMux()
	New("social", &clanService{}, auth.RequirePlayer(pub, time.Now), testLog()).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/clans", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
}
