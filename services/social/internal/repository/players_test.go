package repository

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"gitlab.com/clan-platform/clan-backend/pkg/pg"
)

// These tests talk to a real Postgres with the social migrations applied. They run only
// when SOCIAL_TEST_DATABASE_URL is set (see README "Integration tests"), otherwise they are skipped.
func testRepo(t *testing.T) (*Repository, func(gameIDs ...string)) {
	t.Helper()
	url := os.Getenv("SOCIAL_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SOCIAL_TEST_DATABASE_URL is not set; skipping database test")
	}
	pool, err := pg.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func(gameIDs ...string) {
		for _, g := range gameIDs {
			// rows that point at players first (a clan's members go with the clan)
			_, _ = pool.Exec(context.Background(), "delete from blocks where game_id = $1", g)
			_, _ = pool.Exec(context.Background(), "delete from friend_requests where game_id = $1", g)
			_, _ = pool.Exec(context.Background(), "delete from friendships where game_id = $1", g)
			_, _ = pool.Exec(context.Background(), "delete from clans where game_id = $1", g)
			_, _ = pool.Exec(context.Background(), "delete from players where game_id = $1", g)
		}
		pool.Close()
	}
	return New(pool), cleanup
}

// newGameID makes a random game ID so test runs never collide with each other or real data.
func newGameID(t *testing.T) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func TestUpsertPlayerFirstAndRepeatedCalls(t *testing.T) {
	r, cleanup := testRepo(t)
	game, otherGame := newGameID(t), newGameID(t)
	defer cleanup(game, otherGame)
	ctx := context.Background()

	first, err := r.UpsertPlayer(ctx, game, "u_42", "Ali")
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.UpsertPlayer(ctx, game, "u_42", "Ali")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || !again.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("second call created a new player: %+v vs %+v", again, first)
	}

	renamed, err := r.UpsertPlayer(ctx, game, "u_42", "Ali the Great")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.ID != first.ID || renamed.DisplayName != "Ali the Great" {
		t.Fatalf("rename: %+v", renamed)
	}

	other, err := r.UpsertPlayer(ctx, otherGame, "u_42", "Ali")
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == first.ID {
		t.Fatal("the same player ID in another game must be a different player")
	}
}

// Renamed is true only for the call that changed the name of an existing player.
func TestUpsertPlayerReportsARename(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	steps := []struct {
		name        string
		wantRenamed bool
	}{
		{"Ali", false},           // first insert
		{"Ali", false},           // same name again
		{"Ali the Great", true},  // a new name
		{"Ali the Great", false}, // the same new name again
		{"ali the great", true},  // upper/lower case counts as a change
		{"Ali", true},            // back to an older name
	}
	var id string
	for i, st := range steps {
		p, err := r.UpsertPlayer(ctx, game, "u_42", st.name)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			id = p.ID
		}
		if p.ID != id || p.DisplayName != st.name || p.Renamed != st.wantRenamed {
			t.Fatalf("step %d (%q): got %+v, want name %q renamed=%v", i, st.name, p, st.name, st.wantRenamed)
		}
	}
}

// Many requests with the same new name at the same moment: exactly one reports the rename.
func TestUpsertPlayerConcurrentRenameIsReportedOnce(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()

	for round := 0; round < 10; round++ {
		ext := fmt.Sprintf("u_%d", round)
		if _, err := r.UpsertPlayer(ctx, game, ext, "Old"); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		renamed := 0
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				p, err := r.UpsertPlayer(ctx, game, ext, "New")
				if err != nil {
					t.Error(err)
					return
				}
				if p.DisplayName != "New" {
					t.Errorf("name = %q, want New", p.DisplayName)
				}
				if p.Renamed {
					mu.Lock()
					renamed++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if renamed != 1 {
			t.Fatalf("round %d: %d calls reported the rename, want exactly 1", round, renamed)
		}
	}
}

// Two requests for a brand-new player at the same moment must still give one player.
func TestUpsertPlayerConcurrentFirstCalls(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)

	var wg sync.WaitGroup
	ids := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := r.UpsertPlayer(context.Background(), game, "u_new", "Newbie")
			if err != nil {
				t.Error(err)
				return
			}
			ids <- p.ID
		}()
	}
	wg.Wait()
	close(ids)
	distinct := map[string]bool{}
	for id := range ids {
		distinct[id] = true
	}
	if len(distinct) != 1 {
		t.Fatalf("20 parallel first calls produced %d different players, want 1", len(distinct))
	}
}

// search runs SearchPlayers with a large limit and fails the test on an error.
func search(t *testing.T, r *Repository, gameID, playerID, query string) []PlayerSearchResult {
	t.Helper()
	got, err := r.SearchPlayers(context.Background(), gameID, playerID, query, 100)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func searchNames(results []PlayerSearchResult) []string {
	names := make([]string, 0, len(results))
	for _, p := range results {
		names = append(names, p.DisplayName)
	}
	return names
}

func TestSearchPlayersByNameIgnoringCase(t *testing.T) {
	r, cleanup := testRepo(t)
	game, otherGame := newGameID(t), newGameID(t)
	defer cleanup(game, otherGame)
	ctx := context.Background()
	me, err := r.UpsertPlayer(ctx, game, "me", "Alina") // matches "ali" too, but is the caller
	if err != nil {
		t.Fatal(err)
	}
	for ext, name := range map[string]string{"1": "Ali", "2": "VALIDATOR", "3": "Rashad", "4": "ali_2", "5": "Dalia"} {
		if _, err := r.UpsertPlayer(ctx, game, ext, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.UpsertPlayer(ctx, otherGame, "x", "Alibaba"); err != nil { // another game
		t.Fatal(err)
	}

	got := searchNames(search(t, r, game, me.ID, "ALI"))
	want := []string{"Ali", "ali_2", "Dalia", "VALIDATOR"} // by name ignoring case; never the caller or other games
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("search ALI = %v, want %v", got, want)
	}
}

func TestSearchPlayersSpecialCharactersAreText(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	me := newPlayer(t, r, game, "me")
	for _, name := range []string{"100%", "a_b", "ab", "axb"} {
		newPlayer(t, r, game, name)
	}
	if got := searchNames(search(t, r, game, me, "%")); strings.Join(got, ",") != "100%" {
		t.Errorf(`search "%%" = %v, want [100%%]`, got)
	}
	if got := searchNames(search(t, r, game, me, "_")); strings.Join(got, ",") != "a_b" {
		t.Errorf(`search "_" = %v, want [a_b]`, got)
	}
}

func TestSearchPlayersLimitAndOrder(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	ctx := context.Background()
	me := newPlayer(t, r, game, "me")
	for i := 0; i < 5; i++ {
		if _, err := r.UpsertPlayer(ctx, game, "same"+string(rune('a'+i)), "Same"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.SearchPlayers(ctx, game, me, "same", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("limit 3 gave %d players", len(got))
	}
	if !(got[0].ID < got[1].ID && got[1].ID < got[2].ID) {
		t.Errorf("equal names are not ordered by ID: %v", got)
	}
}

func TestSearchPlayersRelations(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	me := newPlayer(t, r, game, "me")
	friend, sentTo, from, stranger := newPlayer(t, r, game, "p-friend"), newPlayer(t, r, game, "p-sent"),
		newPlayer(t, r, game, "p-from"), newPlayer(t, r, game, "p-stranger")
	makeFriends(t, r, game, me, friend)
	sent := sendRequest(t, r, game, me, sentTo)
	received := sendRequest(t, r, game, from, me)
	sendRequest(t, r, game, stranger, friend) // between two others: does not concern me

	want := map[string]PlayerSearchResult{
		friend:   {ID: friend, DisplayName: "p-friend", Relation: RelationFriend},
		sentTo:   {ID: sentTo, DisplayName: "p-sent", Relation: RelationRequestSent, RequestID: sent.ID},
		from:     {ID: from, DisplayName: "p-from", Relation: RelationRequestReceived, RequestID: received.ID},
		stranger: {ID: stranger, DisplayName: "p-stranger", Relation: RelationNone},
	}
	got := search(t, r, game, me, "p-")
	if len(got) != len(want) {
		t.Fatalf("got %d players, want %d: %+v", len(got), len(want), got)
	}
	for _, p := range got {
		if p != want[p.ID] {
			t.Errorf("%s: got %+v, want %+v", p.DisplayName, p, want[p.ID])
		}
	}
}

func TestSearchPlayersNothingFound(t *testing.T) {
	r, cleanup := testRepo(t)
	game := newGameID(t)
	defer cleanup(game)
	me := newPlayer(t, r, game, "me")
	newPlayer(t, r, game, "someone")
	got := search(t, r, game, me, "nobody")
	if got == nil || len(got) != 0 {
		t.Errorf("got %v, want an empty list (not nil)", got)
	}
}
