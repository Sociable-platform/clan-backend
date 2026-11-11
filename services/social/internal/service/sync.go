package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/repository"
)

const (
	// defaultSyncLimit and maxSyncLimit are the page size of the friendships sync when no limit is given, and the most it may be.
	defaultSyncLimit = 200
	maxSyncLimit     = 500
)

// SyncFriendship is one friendship for another service that keeps its own copy.
type SyncFriendship struct {
	GameID  string
	PlayerA FriendPlayer // the smaller ID of the pair
	PlayerB FriendPlayer
	Since   time.Time
}

// SyncFriendshipsInput is what the caller of the sync endpoint can ask for. Everything is text, as
// it comes from the URL, and an empty value means "not given".
type SyncFriendshipsInput struct {
	After string // the NextCursor of the previous page; empty for the first page
	Limit string // 1 to 500; default 200
}

// FriendshipPage is one page of friendships.
type FriendshipPage struct {
	Friendships []SyncFriendship
	NextCursor  string // pass it as After to get the next page; empty on the last page
}

// ListFriendshipsForSync returns one page of the friendships of every game, in a fixed order, so
// the realtime service can rebuild its conversations after it was down or missed an event. The
// caller is another service (the handler checks the internal token), not a player, which is why
// this is not limited to one game.
//
// Wrong input is a 400: invalid_cursor or invalid_limit.
func (s *Service) ListFriendshipsForSync(ctx context.Context, in SyncFriendshipsInput) (FriendshipPage, error) {
	limit := defaultSyncLimit
	if in.Limit != "" {
		// plain digits only: no sign, no decimals, no leading zero (same rule as the other lists)
		n, err := strconv.Atoi(in.Limit)
		if !limitText.MatchString(in.Limit) || err != nil || n > maxSyncLimit {
			return FriendshipPage{}, httpx.NewError(http.StatusBadRequest, "invalid_limit",
				fmt.Sprintf("limit must be a whole number from 1 to %d", maxSyncLimit))
		}
		limit = n
	}
	after, err := decodeSyncCursor(in.After)
	if err != nil {
		return FriendshipPage{}, err
	}

	// One more than asked for tells whether there is a next page, without a second query.
	rows, err := s.store.ListFriendshipsAfter(ctx, after, limit+1)
	if err != nil {
		return FriendshipPage{}, fmt.Errorf("list friendships for sync: %w", err)
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}

	page := FriendshipPage{Friendships: make([]SyncFriendship, 0, len(rows))}
	for _, f := range rows {
		page.Friendships = append(page.Friendships, SyncFriendship{
			GameID:  f.GameID,
			PlayerA: FriendPlayer(f.PlayerA),
			PlayerB: FriendPlayer(f.PlayerB),
			Since:   f.CreatedAt,
		})
	}
	if more {
		page.NextCursor = encodeSyncCursor(rows[len(rows)-1].Key())
	}
	return page, nil
}

// The cursor is the key of the last friendship of a page, written as one opaque string. Callers
// pass it back unchanged and must not rely on what is inside.

func encodeSyncCursor(k repository.FriendshipKey) string {
	return base64.RawURLEncoding.EncodeToString([]byte(k.GameID + "|" + k.PlayerA + "|" + k.PlayerB))
}

// decodeSyncCursor turns a cursor back into a key. An empty cursor is the start. Anything else
// must be a cursor this service made, because its parts go to the database.
func decodeSyncCursor(cursor string) (repository.FriendshipKey, error) {
	if cursor == "" {
		return repository.FriendshipKey{}, nil
	}
	bad := httpx.NewError(http.StatusBadRequest, "invalid_cursor", "after is not a cursor from a previous page")
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return repository.FriendshipKey{}, bad
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 {
		return repository.FriendshipKey{}, bad
	}
	for _, p := range parts {
		if !uuidPattern.MatchString(p) {
			return repository.FriendshipKey{}, bad
		}
	}
	return repository.FriendshipKey{GameID: parts[0], PlayerA: parts[1], PlayerB: parts[2]}, nil
}
