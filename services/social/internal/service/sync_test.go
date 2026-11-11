package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
)

// syncStore serves friendships from a list, in the order given, like the repository does.
type syncStore struct {
	memStore
	rows  []repository.SyncFriendship
	after repository.FriendshipKey // what the last call asked for
	limit int
	err   error
}

func (s *syncStore) ListFriendshipsAfter(_ context.Context, after repository.FriendshipKey, limit int) ([]repository.SyncFriendship, error) {
	s.after, s.limit = after, limit
	if s.err != nil {
		return nil, s.err
	}
	start := 0
	if after != (repository.FriendshipKey{}) {
		for i, f := range s.rows {
			if f.Key() == after {
				start = i + 1
			}
		}
	}
	end := start + limit
	if end > len(s.rows) {
		end = len(s.rows)
	}
	return s.rows[start:end], nil
}

// syncRows makes n friendships with growing IDs, so the list is in key order.
func syncRows(n int) []repository.SyncFriendship {
	rows := make([]repository.SyncFriendship, n)
	for i := range rows {
		rows[i] = repository.SyncFriendship{
			GameID:    gameA,
			PlayerA:   repository.FriendPlayer{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i*2+1), DisplayName: "A"},
			PlayerB:   repository.FriendPlayer{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i*2+2), DisplayName: "B"},
			CreatedAt: time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC),
		}
	}
	return rows
}

func TestListFriendshipsForSyncFirstPage(t *testing.T) {
	store := &syncStore{rows: syncRows(3)}
	page, err := New(store).ListFriendshipsForSync(context.Background(), SyncFriendshipsInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Friendships) != 3 || page.NextCursor != "" {
		t.Fatalf("page = %d friendships, cursor %q; want 3 and no cursor", len(page.Friendships), page.NextCursor)
	}
	first := page.Friendships[0]
	if first.GameID != gameA || first.PlayerA.DisplayName != "A" || first.PlayerB.DisplayName != "B" || first.Since.IsZero() {
		t.Errorf("first = %+v", first)
	}
	if store.after != (repository.FriendshipKey{}) {
		t.Errorf("asked after %+v, want the start", store.after)
	}
	// It asks for one more than the default page, to know whether another page follows.
	if store.limit != defaultSyncLimit+1 {
		t.Errorf("asked for %d rows, want %d", store.limit, defaultSyncLimit+1)
	}
}

func TestListFriendshipsForSyncEmptyIsNotNil(t *testing.T) {
	page, err := New(&syncStore{}).ListFriendshipsForSync(context.Background(), SyncFriendshipsInput{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Friendships == nil || len(page.Friendships) != 0 || page.NextCursor != "" {
		t.Errorf("page = %+v, want an empty list and no cursor", page)
	}
}

// TestListFriendshipsForSyncWalksEveryPage follows the cursor to the end: every friendship comes
// exactly once, and the last page has no cursor.
func TestListFriendshipsForSyncWalksEveryPage(t *testing.T) {
	for _, total := range []int{1, 4, 5, 6, 11} {
		t.Run(fmt.Sprintf("%d friendships", total), func(t *testing.T) {
			svc := New(&syncStore{rows: syncRows(total)})
			seen, cursor, pages := 0, "", 0
			for {
				page, err := svc.ListFriendshipsForSync(context.Background(), SyncFriendshipsInput{After: cursor, Limit: "5"})
				if err != nil {
					t.Fatal(err)
				}
				seen += len(page.Friendships)
				pages++
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
				if pages > total {
					t.Fatal("the cursor never ends")
				}
			}
			if seen != total {
				t.Errorf("saw %d friendships, want %d", seen, total)
			}
		})
	}
}

func TestListFriendshipsForSyncBadInput(t *testing.T) {
	good := encodeSyncCursor(repository.FriendshipKey{GameID: gameA, PlayerA: gameA, PlayerB: gameA})
	cases := []struct {
		name string
		in   SyncFriendshipsInput
		code string
	}{
		{"limit 0", SyncFriendshipsInput{Limit: "0"}, "invalid_limit"},
		{"limit too big", SyncFriendshipsInput{Limit: "501"}, "invalid_limit"},
		{"limit not a number", SyncFriendshipsInput{Limit: "ten"}, "invalid_limit"},
		{"limit with a sign", SyncFriendshipsInput{Limit: "+5"}, "invalid_limit"},
		{"cursor not base64", SyncFriendshipsInput{After: "not a cursor!"}, "invalid_cursor"},
		{"cursor with other text", SyncFriendshipsInput{After: "aGVsbG8"}, "invalid_cursor"},
		{"cursor with SQL in it", SyncFriendshipsInput{After: encodeRaw("x'; drop table players;--|a|b")}, "invalid_cursor"},
		{"cursor with two parts", SyncFriendshipsInput{After: encodeRaw(gameA + "|" + gameA)}, "invalid_cursor"},
		{"cursor with four parts", SyncFriendshipsInput{After: encodeRaw(gameA + "|" + gameA + "|" + gameA + "|" + gameA)}, "invalid_cursor"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &syncStore{}
			_, err := New(store).ListFriendshipsForSync(context.Background(), c.in)
			var apiErr *httpx.Error
			if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest || apiErr.Code != c.code {
				t.Fatalf("err = %v, want 400 %s", err, c.code)
			}
			if store.limit != 0 {
				t.Error("the store was asked although the input was wrong")
			}
		})
	}
	// A cursor the service made itself is accepted.
	if _, err := New(&syncStore{}).ListFriendshipsForSync(context.Background(), SyncFriendshipsInput{After: good, Limit: "500"}); err != nil {
		t.Errorf("good cursor and limit 500: %v", err)
	}
}

func TestListFriendshipsForSyncDatabaseFailure(t *testing.T) {
	boom := errors.New("boom")
	_, err := New(&syncStore{err: boom}).ListFriendshipsForSync(context.Background(), SyncFriendshipsInput{})
	var apiErr *httpx.Error
	if !errors.Is(err, boom) || errors.As(err, &apiErr) {
		t.Errorf("err = %v, want the database error wrapped and not an API error", err)
	}
}

// encodeRaw writes text as a cursor would be written, for cursors that are made wrong on purpose.
func encodeRaw(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
