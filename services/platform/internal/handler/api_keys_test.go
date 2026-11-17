package handler

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

const (
	someGameID = "0d6f2c0e-5b1a-4f5e-8f55-2f4f0c7a9b11"
	someKeyID  = "3b1c6f0e-9d2a-4a53-8f0b-7a9d2f6c1e44"
)

func TestCreateAPIKey_ReturnsTheFullKeyOnce(t *testing.T) {
	created := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	var gotDev, gotGame string
	svc := authedFake()
	svc.createAPIKey = func(dev, game string) (service.NewAPIKey, error) {
		gotDev, gotGame = dev, game
		return service.NewAPIKey{
			APIKey: service.APIKey{ID: someKeyID, GameID: game, KeyPrefix: "clk_Ab3x", CreatedAt: created},
			Key:    "clk_Ab3xSECRET",
		}, nil
	}

	rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games/"+someGameID+"/api-keys", "", "good")

	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if gotDev != "d1" || gotGame != someGameID {
		t.Fatalf("developer %q (from token), game %q (from path)", gotDev, gotGame)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
	want := `{"id":"` + someKeyID + `","gameId":"` + someGameID + `","keyPrefix":"clk_Ab3x","createdAt":"2026-11-07T10:00:00Z","revokedAt":null,"key":"clk_Ab3xSECRET"}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestListAPIKeys_NeverContainsTheKey(t *testing.T) {
	revoked := time.Date(2026, 11, 7, 11, 0, 0, 0, time.UTC)
	svc := authedFake()
	svc.listAPIKeys = func(dev, game string) ([]service.APIKey, error) {
		return []service.APIKey{
			{ID: "k2", GameID: game, KeyPrefix: "clk_bbbb", CreatedAt: time.Unix(2, 0)},
			{ID: "k1", GameID: game, KeyPrefix: "clk_aaaa", CreatedAt: time.Unix(1, 0), RevokedAt: &revoked},
		}, nil
	}

	rec := call(t, svc, discardLog(), http.MethodGet, "/api/v1/games/"+someGameID+"/api-keys", "", "good")

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		APIKeys []map[string]any `json:"apiKeys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.APIKeys) != 2 {
		t.Fatalf("body %s (%v)", rec.Body.String(), err)
	}
	for _, k := range body.APIKeys {
		if _, has := k["key"]; has {
			t.Fatalf("a list must never carry the key: %v", k)
		}
	}
	if body.APIKeys[0]["revokedAt"] != nil || body.APIKeys[1]["revokedAt"] != "2026-11-07T11:00:00Z" {
		t.Fatalf("revokedAt wrong: %v", body.APIKeys)
	}
}

func TestListAPIKeys_EmptyIsAnEmptyArray(t *testing.T) {
	svc := authedFake()
	svc.listAPIKeys = func(string, string) ([]service.APIKey, error) { return nil, nil }

	rec := call(t, svc, discardLog(), http.MethodGet, "/api/v1/games/"+someGameID+"/api-keys", "", "good")

	if rec.Body.String() != `{"apiKeys":[]}`+"\n" {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestRevokeAPIKey(t *testing.T) {
	var gotDev, gotKey string
	svc := authedFake()
	svc.revokeAPIKey = func(dev, key string) error { gotDev, gotKey = dev, key; return nil }

	rec := call(t, svc, discardLog(), http.MethodDelete, "/api/v1/api-keys/"+someKeyID, "", "good")

	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("status %d, body %q", rec.Code, rec.Body.String())
	}
	if gotDev != "d1" || gotKey != someKeyID {
		t.Fatalf("developer %q, key %q", gotDev, gotKey)
	}
}

func TestAPIKeyEndpoints_MapServiceErrors(t *testing.T) {
	notFoundGame := httpx.NewError(http.StatusNotFound, "game_not_found", "Game not found")
	notFoundKey := httpx.NewError(http.StatusNotFound, "api_key_not_found", "API key not found")
	svc := authedFake()
	svc.createAPIKey = func(string, string) (service.NewAPIKey, error) { return service.NewAPIKey{}, notFoundGame }
	svc.listAPIKeys = func(string, string) ([]service.APIKey, error) { return nil, notFoundGame }
	svc.revokeAPIKey = func(string, string) error { return notFoundKey }

	for _, tt := range []struct{ method, path, code string }{
		{http.MethodPost, "/api/v1/games/" + someGameID + "/api-keys", "game_not_found"},
		{http.MethodGet, "/api/v1/games/" + someGameID + "/api-keys", "game_not_found"},
		{http.MethodDelete, "/api/v1/api-keys/" + someKeyID, "api_key_not_found"},
	} {
		rec := call(t, svc, discardLog(), tt.method, tt.path, "", "good")
		if rec.Code != http.StatusNotFound || errorCode(t, rec) != tt.code {
			t.Fatalf("%s %s: got %d %s", tt.method, tt.path, rec.Code, rec.Body.String())
		}
	}
}

func TestAPIKeyEndpoints_RequireDeveloperToken(t *testing.T) {
	svc := authedFake() // the key functions are nil: any call would panic
	for _, tt := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/games/" + someGameID + "/api-keys"},
		{http.MethodGet, "/api/v1/games/" + someGameID + "/api-keys"},
		{http.MethodDelete, "/api/v1/api-keys/" + someKeyID},
	} {
		for _, token := range []string{"", "bad"} {
			rec := call(t, svc, discardLog(), tt.method, tt.path, "", token)
			if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "unauthorized" {
				t.Fatalf("%s %s token %q: got %d %s", tt.method, tt.path, token, rec.Code, rec.Body.String())
			}
		}
	}
}

// memKey is what memStore keeps for a key: the table's columns, no full key.
type memKey struct {
	repository.APIKey
	Hash string
}

func (m *memStore) CreateAPIKey(_ context.Context, gameID, keyPrefix, keyHash string) (repository.APIKey, error) {
	k := repository.APIKey{
		ID:     fmt.Sprintf("00000000-0000-4000-8000-%012d", len(m.keys)+1),
		GameID: gameID, KeyPrefix: keyPrefix, CreatedAt: time.Now(),
	}
	m.keys = append(m.keys, memKey{APIKey: k, Hash: keyHash})
	return k, nil
}

func (m *memStore) APIKeysByGame(_ context.Context, gameID string) ([]repository.APIKey, error) {
	out := []repository.APIKey{}
	for _, k := range m.keys {
		if k.GameID == gameID {
			out = append(out, k.APIKey)
		}
	}
	return out, nil
}

func (m *memStore) RevokeAPIKey(_ context.Context, keyID, developerID string) error {
	for i, k := range m.keys {
		for _, g := range m.games {
			if k.ID == keyID && g.ID == k.GameID && m.owners[g.ID] == developerID {
				if m.keys[i].RevokedAt == nil {
					now := time.Now()
					m.keys[i].RevokedAt = &now
				}
				return nil
			}
		}
	}
	return repository.ErrNotFound
}

func (m *memStore) GameIDByActiveKeyHash(_ context.Context, keyHash string) (string, error) {
	for _, k := range m.keys {
		if k.Hash == keyHash && k.RevokedAt == nil {
			return k.GameID, nil
		}
	}
	return "", repository.ErrNotFound
}

// TestAPIKeyFlow runs the whole key life cycle through the real handler and service:
// create (key shown once), list (prefix only), another developer locked out, revoke, key dead.
func TestAPIKeyFlow(t *testing.T) {
	_, signingKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	store := &memStore{}
	svc, err := service.New(store, signingKey, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	register := func(email string) string {
		t.Helper()
		creds := `"email":"` + email + `","password":"correct horse"`
		if rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/developers/register", `{`+creds+`,"name":"Dev"}`, ""); rec.Code != http.StatusCreated {
			t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
		}
		rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/developers/login", `{`+creds+`}`, "")
		var l loginResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &l)
		return l.Token
	}
	alice, bob := register("alice@example.com"), register("bob@example.com")

	rec := call(t, svc, discardLog(), http.MethodPost, "/api/v1/games", `{"name":"Space Raiders"}`, alice)
	var game gameResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &game)
	keysPath := "/api/v1/games/" + game.ID + "/api-keys"

	// create: the full key is in the response
	rec = call(t, svc, discardLog(), http.MethodPost, keysPath, "", alice)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created newAPIKeyResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if !strings.HasPrefix(created.Key, "clk_") || created.KeyPrefix != created.Key[:8] || created.RevokedAt != nil {
		t.Fatalf("created key: %+v", created)
	}

	// only the hash is stored
	sum := sha256.Sum256([]byte(created.Key))
	if len(store.keys) != 1 || store.keys[0].Hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("store should hold exactly the sha256 of the key, got %+v", store.keys)
	}

	// list: prefix, no secret anywhere in the body
	rec = call(t, svc, discardLog(), http.MethodGet, keysPath, "", alice)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), created.Key) || strings.Contains(rec.Body.String(), `"key"`) {
		t.Fatalf("list must not reveal the key: %d %s", rec.Code, rec.Body.String())
	}

	// the key works for its game
	if gid, err := svc.GameForAPIKey(context.Background(), created.Key); err != nil || gid != game.ID {
		t.Fatalf("key should work: %q, %v", gid, err)
	}

	// bob is locked out of alice's game and key
	for _, tt := range []struct{ method, path string }{
		{http.MethodGet, keysPath}, {http.MethodPost, keysPath}, {http.MethodDelete, "/api/v1/api-keys/" + created.ID},
	} {
		if rec := call(t, svc, discardLog(), tt.method, tt.path, "", bob); rec.Code != http.StatusNotFound {
			t.Fatalf("bob %s %s: want 404, got %d %s", tt.method, tt.path, rec.Code, rec.Body.String())
		}
	}
	if len(store.keys) != 1 || store.keys[0].RevokedAt != nil {
		t.Fatal("bob must not have created or revoked anything")
	}

	// alice revokes; the key is dead at once but still listed
	if rec := call(t, svc, discardLog(), http.MethodDelete, "/api/v1/api-keys/"+created.ID, "", alice); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := svc.GameForAPIKey(context.Background(), created.Key); err == nil {
		t.Fatal("revoked key must stop working immediately")
	}
	rec = call(t, svc, discardLog(), http.MethodGet, keysPath, "", alice)
	var list apiKeyListResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.APIKeys) != 1 || list.APIKeys[0].RevokedAt == nil {
		t.Fatalf("revoked key should be listed as revoked: %s", rec.Body.String())
	}
}
