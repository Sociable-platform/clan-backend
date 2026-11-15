package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// deletedCall sends a request with a fresh token for u_42 of testGame through RequirePlayer and
// then RejectDeletedPlayers, like the service does. deleted is the gate's answer; asked is who it
// was asked about.
func deletedCall(t *testing.T, deleted bool) (status int, code string, reached bool, asked []Player) {
	t.Helper()
	pub, priv := testKeys(t)
	tok := sign(t, priv, playerClaims("u_42", testGame), time.Hour)
	endpoint := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	gate := func(_ context.Context, p Player) bool {
		asked = append(asked, p)
		return deleted
	}
	rec := httptest.NewRecorder()
	RequirePlayer(pub, func() time.Time { return testNow })(RejectDeletedPlayers(gate)(endpoint)).
		ServeHTTP(rec, withAuth("Bearer "+tok))
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
		t.Error("a 401 without WWW-Authenticate")
	}
	return rec.Code, body.Error.Code, reached, asked
}

func TestRejectDeletedPlayersRefusesATokenIssuedBeforeTheDeletion(t *testing.T) {
	status, code, reached, asked := deletedCall(t, true)
	if status != http.StatusUnauthorized || code != "player_deleted" || reached {
		t.Fatalf("status %d, code %q, reached %v; want 401 player_deleted and the endpoint not reached", status, code, reached)
	}
	want := Player{GameID: testGame, ExternalID: "u_42", Name: "Ali", IssuedAt: time.Unix(testNow.Unix(), 0)}
	if len(asked) != 1 || asked[0] != want {
		t.Fatalf("asked about %+v, want [%+v]", asked, want)
	}
}

func TestRejectDeletedPlayersLetsALivePlayerThrough(t *testing.T) {
	if status, _, reached, _ := deletedCall(t, false); status != http.StatusOK || !reached {
		t.Fatalf("status %d, reached %v; want 200 and the endpoint reached", status, reached)
	}
}

// Without RequirePlayer in front there is no player: a wiring mistake, so a 500.
func TestRejectDeletedPlayersWithoutRequirePlayerIsAnError(t *testing.T) {
	reached := false
	h := RejectDeletedPlayers(func(context.Context, Player) bool { return false })(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, withAuth(""))
	if rec.Code != http.StatusInternalServerError || reached {
		t.Fatalf("status %d, reached %v; want 500 and the endpoint not reached", rec.Code, reached)
	}
}
