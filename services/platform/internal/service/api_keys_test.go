package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

// storedKey is what the fake store keeps for an API key: exactly the columns of the
// api_keys table. There is no column for the full key, so tests can check what reaches the database.
type storedKey struct {
	repository.APIKey
	Hash string
}

func (f *fakeStore) CreateAPIKey(_ context.Context, gameID, keyPrefix, keyHash string) (repository.APIKey, error) {
	if f.err != nil {
		return repository.APIKey{}, f.err
	}
	n := len(f.keys) + 1
	k := repository.APIKey{
		ID:        "00000000-0000-4000-8000-" + strings.Repeat("0", 12-len(strconv.Itoa(n))) + strconv.Itoa(n),
		GameID:    gameID,
		KeyPrefix: keyPrefix,
		CreatedAt: time.Unix(1_800_000_000+int64(n), 0),
	}
	f.keys = append(f.keys, storedKey{APIKey: k, Hash: keyHash})
	return k, nil
}

func (f *fakeStore) APIKeysByGame(_ context.Context, gameID string) ([]repository.APIKey, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := []repository.APIKey{}
	for i := len(f.keys) - 1; i >= 0; i-- { // newest first
		if f.keys[i].GameID == gameID {
			out = append(out, f.keys[i].APIKey)
		}
	}
	return out, nil
}

// RevokeAPIKey follows the SQL: a key of a game developerID owns is revoked (an already revoked key keeps
// its first revocation time), a member of the game gets ErrOwnerRequired, and a key of a game the developer
// is not in, or no key at all, is ErrNotFound.
func (f *fakeStore) RevokeAPIKey(_ context.Context, keyID, developerID string) error {
	if f.err != nil {
		return f.err
	}
	for i, k := range f.keys {
		if k.ID != keyID {
			continue
		}
		for _, g := range f.games {
			if g.ID != k.GameID {
				continue
			}
			as, ok := f.gameAs(g, developerID)
			if !ok {
				continue
			}
			if as.Role != repository.RoleOwner {
				return repository.ErrOwnerRequired
			}
			if f.keys[i].RevokedAt == nil {
				now := time.Unix(1_900_000_000+int64(i), 0)
				f.keys[i].RevokedAt = &now
			}
			return nil
		}
	}
	return repository.ErrNotFound
}

func (f *fakeStore) GameIDByActiveKeyHash(_ context.Context, keyHash string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	for _, k := range f.keys {
		if k.Hash == keyHash && k.RevokedAt == nil {
			return k.GameID, nil
		}
	}
	return "", repository.ErrNotFound
}

// gameOfAlice returns a service with two developers and one game owned by alice.
func gameOfAlice(t *testing.T) (svc *Service, store *fakeStore, alice, bob Developer, game Game) {
	t.Helper()
	svc, store, _ = newTestService(t)
	reg := func(email string) Developer {
		d, err := svc.Register(context.Background(), RegisterInput{Email: email, Password: "correct horse", Name: "Dev"})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	alice, bob = reg("alice@example.com"), reg("bob@example.com")
	game, err := svc.RegisterGame(context.Background(), alice.ID, RegisterGameInput{Name: "Space Raiders"})
	if err != nil {
		t.Fatal(err)
	}
	return svc, store, alice, bob, game
}

func TestCreateAPIKey_FormatAndRandomness(t *testing.T) {
	svc, _, alice, _, game := gameOfAlice(t)

	a, err := svc.CreateAPIKey(context.Background(), alice.ID, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.CreateAPIKey(context.Background(), alice.ID, game.ID)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(a.Key, "clk_") || len(a.Key) != 4+43 { // marker + 32 random bytes in base64url
		t.Fatalf("unexpected key format %q (len %d)", a.Key, len(a.Key))
	}
	if a.Key == b.Key {
		t.Fatal("two keys must not be equal")
	}
	if a.KeyPrefix != a.Key[:8] || a.GameID != game.ID || a.RevokedAt != nil {
		t.Fatalf("unexpected metadata %+v", a.APIKey)
	}
}

func TestCreateAPIKey_StoresOnlyAHash(t *testing.T) {
	svc, store, alice, _, game := gameOfAlice(t)

	k, err := svc.CreateAPIKey(context.Background(), alice.ID, game.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(store.keys) != 1 {
		t.Fatalf("want 1 stored key, got %d", len(store.keys))
	}
	stored := store.keys[0]
	sum := sha256.Sum256([]byte(k.Key))
	if stored.Hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("stored hash %q is not the sha256 of the key", stored.Hash)
	}
	// Nothing the database receives may contain the full key.
	for name, v := range map[string]string{"hash": stored.Hash, "prefix": stored.KeyPrefix, "id": stored.ID, "game": stored.GameID} {
		if strings.Contains(v, k.Key) || v == k.Key {
			t.Fatalf("full key leaked into stored %s", name)
		}
	}
	if len(stored.KeyPrefix) != 8 {
		t.Fatalf("prefix %q should be 8 characters", stored.KeyPrefix)
	}
}

func TestAPIKeys_OnlyOwnerCanManage(t *testing.T) {
	svc, store, alice, bob, game := gameOfAlice(t)
	ctx := context.Background()
	k, err := svc.CreateAPIKey(ctx, alice.ID, game.ID)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("bob cannot create a key for alice's game", func(t *testing.T) {
		_, err := svc.CreateAPIKey(ctx, bob.ID, game.ID)
		wantError(t, err, http.StatusNotFound, "game_not_found")
		if len(store.keys) != 1 {
			t.Fatal("no key may be created for someone else's game")
		}
	})

	t.Run("bob cannot list alice's keys", func(t *testing.T) {
		_, err := svc.ListAPIKeys(ctx, bob.ID, game.ID)
		wantError(t, err, http.StatusNotFound, "game_not_found")
	})

	t.Run("bob cannot revoke alice's key, and it keeps working", func(t *testing.T) {
		err := svc.RevokeAPIKey(ctx, bob.ID, k.ID)
		wantError(t, err, http.StatusNotFound, "api_key_not_found")
		if gid, err := svc.GameForAPIKey(ctx, k.Key); err != nil || gid != game.ID {
			t.Fatalf("key must still work, got %q, %v", gid, err)
		}
	})

	t.Run("missing and malformed IDs give the same 404", func(t *testing.T) {
		_, err := svc.CreateAPIKey(ctx, alice.ID, "00000000-0000-4000-8000-00000000beef")
		wantError(t, err, http.StatusNotFound, "game_not_found")
		_, err = svc.ListAPIKeys(ctx, alice.ID, "not-a-uuid")
		wantError(t, err, http.StatusNotFound, "game_not_found")

		errMissing := svc.RevokeAPIKey(ctx, alice.ID, "00000000-0000-4000-8000-00000000beef")
		errOther := svc.RevokeAPIKey(ctx, bob.ID, k.ID)
		wantError(t, errMissing, http.StatusNotFound, "api_key_not_found")
		if errMissing.Error() != errOther.Error() {
			t.Fatalf("responses differ: %q vs %q", errMissing, errOther)
		}
		wantError(t, svc.RevokeAPIKey(ctx, alice.ID, "not-a-uuid"), http.StatusNotFound, "api_key_not_found")
	})
}

func TestListAPIKeys(t *testing.T) {
	svc, _, alice, _, game := gameOfAlice(t)
	ctx := context.Background()

	empty, err := svc.ListAPIKeys(ctx, alice.ID, game.ID)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("want empty non-nil slice, got %#v, %v", empty, err)
	}

	first, _ := svc.CreateAPIKey(ctx, alice.ID, game.ID)
	second, _ := svc.CreateAPIKey(ctx, alice.ID, game.ID)
	if err := svc.RevokeAPIKey(ctx, alice.ID, first.ID); err != nil {
		t.Fatal(err)
	}

	keys, err := svc.ListAPIKeys(ctx, alice.ID, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0].ID != second.ID || keys[1].ID != first.ID {
		t.Fatalf("want both keys, newest first, got %+v", keys)
	}
	if keys[0].RevokedAt != nil || keys[1].RevokedAt == nil {
		t.Fatalf("revoked state wrong: %+v", keys)
	}
	if keys[0].KeyPrefix != second.Key[:8] {
		t.Fatalf("prefix %q", keys[0].KeyPrefix)
	}
}

func TestRevokedKeyStopsWorkingImmediately(t *testing.T) {
	svc, _, alice, _, game := gameOfAlice(t)
	ctx := context.Background()
	k, _ := svc.CreateAPIKey(ctx, alice.ID, game.ID)
	other, _ := svc.CreateAPIKey(ctx, alice.ID, game.ID)

	if gid, err := svc.GameForAPIKey(ctx, k.Key); err != nil || gid != game.ID {
		t.Fatalf("active key: got %q, %v", gid, err)
	}

	if err := svc.RevokeAPIKey(ctx, alice.ID, k.ID); err != nil {
		t.Fatal(err)
	}

	_, err := svc.GameForAPIKey(ctx, k.Key)
	wantError(t, err, http.StatusUnauthorized, "invalid_api_key")
	if _, err := svc.GameForAPIKey(ctx, other.Key); err != nil {
		t.Fatalf("revoking one key must not affect another: %v", err)
	}
}

func TestRevokeAPIKey_IsIdempotent(t *testing.T) {
	svc, store, alice, _, game := gameOfAlice(t)
	ctx := context.Background()
	k, _ := svc.CreateAPIKey(ctx, alice.ID, game.ID)

	if err := svc.RevokeAPIKey(ctx, alice.ID, k.ID); err != nil {
		t.Fatal(err)
	}
	firstRevokedAt := *store.keys[0].RevokedAt
	if err := svc.RevokeAPIKey(ctx, alice.ID, k.ID); err != nil {
		t.Fatalf("second revoke should succeed: %v", err)
	}
	if !store.keys[0].RevokedAt.Equal(firstRevokedAt) {
		t.Fatal("second revoke must keep the first revocation time")
	}
}

func TestGameForAPIKey_RejectsBadKeys(t *testing.T) {
	svc, _, alice, _, game := gameOfAlice(t)
	k, _ := svc.CreateAPIKey(context.Background(), alice.ID, game.ID)

	for name, key := range map[string]string{
		"empty":           "",
		"no marker":       strings.TrimPrefix(k.Key, "clk_"),
		"unknown key":     "clk_" + strings.Repeat("A", 43),
		"truncated":       k.Key[:20],
		"extra character": k.Key + "x",
		"hash of the key": hashKey(k.Key), // the stored value must not work as a key
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.GameForAPIKey(context.Background(), key)
			wantError(t, err, http.StatusUnauthorized, "invalid_api_key")
		})
	}
}

func TestGameForAPIKey_ResolvesTheKeysOwnGame(t *testing.T) {
	svc, _, alice, _, gameA := gameOfAlice(t)
	ctx := context.Background()
	gameB, err := svc.RegisterGame(ctx, alice.ID, RegisterGameInput{Name: "Second Game"})
	if err != nil {
		t.Fatal(err)
	}
	ka, _ := svc.CreateAPIKey(ctx, alice.ID, gameA.ID)
	kb, _ := svc.CreateAPIKey(ctx, alice.ID, gameB.ID)

	if gid, _ := svc.GameForAPIKey(ctx, ka.Key); gid != gameA.ID {
		t.Fatalf("key of game A resolved to %q", gid)
	}
	if gid, _ := svc.GameForAPIKey(ctx, kb.Key); gid != gameB.ID {
		t.Fatalf("key of game B resolved to %q", gid)
	}
}
