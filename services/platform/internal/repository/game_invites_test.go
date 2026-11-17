package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

var inviteNow = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

func inviteEmail(label string) string {
	return fmt.Sprintf("%s-%d@example.com", label, time.Now().UnixNano())
}

// invitesOf lists every row of game_invites of a game, expired or not, as (email -> id).
func invitesOf(t *testing.T, r *Repository, gameID string) map[string]string {
	t.Helper()
	rows, err := r.pool.Query(context.Background(), `select email, id::text from game_invites where game_id = $1`, gameID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var email, id string
		if err := rows.Scan(&email, &id); err != nil {
			t.Fatal(err)
		}
		out[email] = id
	}
	return out
}

func TestSaveGameInviteCreatesThenRenews(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	owner, game := newGame(t, r)
	email := inviteEmail("carol")

	first, err := r.SaveGameInvite(ctx, game, email, owner, inviteNow, inviteNow.Add(7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.GameID != game || first.Email != email || first.GameName == "" || first.InvitedByName == "" {
		t.Errorf("invite = %+v, want the names filled in", first)
	}
	other := newDeveloper(t, r, "second-owner")
	later := inviteNow.Add(3 * 24 * time.Hour)
	again, err := r.SaveGameInvite(ctx, game, email, other, later, later.Add(7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || !again.ExpiresAt.Equal(later.Add(7*24*time.Hour)) || again.InvitedByName != "second-owner" {
		t.Errorf("renewed = %+v, first %+v; want the same row with a new expiry and inviter", again, first)
	}
	if rows := invitesOf(t, r, game); len(rows) != 1 {
		t.Errorf("%d rows, want one per game and email", len(rows))
	}
	// An upper-case address is not stored: the table refuses what the service did not lower-case.
	if _, err := r.SaveGameInvite(ctx, game, "Carol@Example.com", owner, inviteNow, inviteNow.Add(time.Hour)); err == nil {
		t.Error("an address with capitals was stored")
	}
}

func TestInviteCountsAndPurge(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	owner, game := newGame(t, r)
	_, game2 := newGame(t, r)
	email := inviteEmail("dave")
	expiry := func(at time.Time) time.Time { return at.Add(7 * 24 * time.Hour) }

	// two for this address from this game (the second renews the first), one from another game, one for somebody else
	// that is two hours old, and one for this address that is two days old.
	for _, step := range []struct {
		game, email string
		at          time.Time
	}{
		{game, email, inviteNow.Add(-30 * time.Minute)},
		{game, email, inviteNow.Add(-10 * time.Minute)},
		{game2, email, inviteNow.Add(-5 * time.Minute)},
		{game, inviteEmail("erin"), inviteNow.Add(-2 * time.Hour)},
		{game, email, inviteNow.Add(-48 * time.Hour)},
	} {
		if _, err := r.SaveGameInvite(ctx, step.game, step.email, owner, step.at, expiry(step.at)); err != nil {
			t.Fatal(err)
		}
	}
	c, err := r.InviteCountsSince(ctx, game, email, inviteNow)
	if err != nil {
		t.Fatal(err)
	}
	// Last hour, this game: the two at -30m and -10m. Last day, this game and address: the same two (not -48h).
	// Last day, this address, any game: those two and the one from game2.
	if c.GameLastHour != 2 || c.PairLastDay != 2 || c.AddressLastDay != 3 {
		t.Errorf("counts = %+v, want {2 2 3}", c)
	}

	// Purge: the log of two days ago and the invites that expired go; the rest stays.
	if err := r.PurgeGameInvites(ctx, inviteNow.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if rows := invitesOf(t, r, game); len(rows) != 0 {
		t.Errorf("expired invites left: %v", rows)
	}
	if c, _ := r.InviteCountsSince(ctx, game, email, inviteNow); c.GameLastHour != 0 {
		t.Errorf("the old log was kept: %+v", c)
	}
}

func TestListingInvitesHidesTheExpiredAndOrdersNewestFirst(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	owner, game := newGame(t, r)
	email := inviteEmail("frank")
	_, game2 := newGame(t, r)

	if _, err := r.SaveGameInvite(ctx, game, email, owner, inviteNow.Add(-2*time.Hour), inviteNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SaveGameInvite(ctx, game2, email, owner, inviteNow.Add(-time.Hour), inviteNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SaveGameInvite(ctx, game, inviteEmail("expired"), owner, inviteNow.Add(-3*time.Hour), inviteNow); err != nil { // expires exactly now
		t.Fatal(err)
	}

	mine, err := r.GameInvitesForEmail(ctx, email, inviteNow)
	if err != nil || len(mine) != 2 || mine[0].GameID != game2 || mine[1].GameID != game {
		t.Fatalf("for the address: %+v, %v; want both, newest first", mine, err)
	}
	ofGame, err := r.GameInvitesOfGame(ctx, game, inviteNow)
	if err != nil || len(ofGame) != 1 || ofGame[0].Email != email {
		t.Fatalf("of the game: %+v, %v; want the live one only", ofGame, err)
	}
	if none, err := r.GameInvitesForEmail(ctx, inviteEmail("nobody"), inviteNow); err != nil || none == nil || len(none) != 0 {
		t.Errorf("nobody's invites = %#v, %v; want an empty, non-nil list", none, err)
	}
}

func TestEmailIsMember(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	owner, game := newGame(t, r)
	member := newDeveloper(t, r, "member")
	outsider := newDeveloper(t, r, "outsider")
	if _, err := r.pool.Exec(ctx, `insert into game_members (game_id, developer_id, role) values ($1, $2, 'member')`, game, member); err != nil {
		t.Fatal(err)
	}
	emailOf := func(id string) string {
		var e string
		if err := r.pool.QueryRow(ctx, `select email from developers where id = $1`, id).Scan(&e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	for _, tc := range []struct {
		name, email string
		want        bool
	}{{"the owner", emailOf(owner), true}, {"a member", emailOf(member), true}, {"a developer outside", emailOf(outsider), false}, {"nobody", inviteEmail("nobody"), false}} {
		if got, err := r.EmailIsMember(ctx, game, tc.email); err != nil || got != tc.want {
			t.Errorf("%s: %v, %v; want %v", tc.name, got, err, tc.want)
		}
	}
}

func TestAcceptGameInvite(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	owner, game := newGame(t, r)
	invitee := newDeveloper(t, r, "invitee")
	var email string
	if err := r.pool.QueryRow(ctx, `select email from developers where id = $1`, invitee).Scan(&email); err != nil {
		t.Fatal(err)
	}
	inv, err := r.SaveGameInvite(ctx, game, email, owner, inviteNow, inviteNow.Add(7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	// Somebody else's address, an ID that is not a UUID and an unknown ID: ErrNotFound, and the invite stays.
	stranger := newDeveloper(t, r, "stranger")
	for _, tc := range []struct{ name, id, email, dev string }{
		{"another address", inv.ID, inviteEmail("other"), stranger},
		{"not a uuid", "not-a-uuid", email, invitee},
		{"unknown id", unknownID, email, invitee},
	} {
		if _, err := r.AcceptGameInvite(ctx, tc.id, tc.dev, tc.email, inviteNow); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v, want ErrNotFound", tc.name, err)
		}
	}
	if len(invitesOf(t, r, game)) != 1 || len(rolesOf(t, r, game)) != 1 {
		t.Fatal("a refused accept changed something")
	}

	// A failing membership insert (a developer that does not exist) keeps the invite: both or neither.
	if _, err := r.AcceptGameInvite(ctx, inv.ID, unknownID, email, inviteNow); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("accept for a missing developer: %v, want an error that is not ErrNotFound", err)
	}
	if len(invitesOf(t, r, game)) != 1 {
		t.Fatal("the invite was deleted although the membership was not made")
	}

	g, err := r.AcceptGameInvite(ctx, inv.ID, invitee, email, inviteNow)
	if err != nil || g.ID != game || g.Role != RoleMember {
		t.Fatalf("accept = %+v, %v; want the game, as a member", g, err)
	}
	if roles := rolesOf(t, r, game); roles[invitee] != RoleMember || roles[owner] != RoleOwner || len(invitesOf(t, r, game)) != 0 {
		t.Errorf("roles %v, invites %v; want the member made and the invite gone", roles, invitesOf(t, r, game))
	}
	if _, err := r.AcceptGameInvite(ctx, inv.ID, invitee, email, inviteNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("accepting twice: %v, want ErrNotFound", err)
	}
}

func TestAcceptGameInviteExpiredMakesNoMember(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	owner, game := newGame(t, r)
	invitee := newDeveloper(t, r, "late")
	var email string
	_ = r.pool.QueryRow(ctx, `select email from developers where id = $1`, invitee).Scan(&email)
	inv, _ := r.SaveGameInvite(ctx, game, email, owner, inviteNow, inviteNow.Add(7*24*time.Hour))

	// At the very moment it expires it is already expired.
	if _, err := r.AcceptGameInvite(ctx, inv.ID, invitee, email, inv.ExpiresAt); !errors.Is(err, ErrInviteExpired) {
		t.Fatalf("accept at expiry: %v, want ErrInviteExpired", err)
	}
	if _, ok := rolesOf(t, r, game)[invitee]; ok || len(invitesOf(t, r, game)) != 0 {
		t.Errorf("an expired invite made a member or stayed: roles %v, invites %v", rolesOf(t, r, game), invitesOf(t, r, game))
	}
}

// An owner who accepts an invite (sent to them by a second owner, say) stays an owner: the existing role is kept.
func TestAcceptGameInviteKeepsAnExistingRole(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	owner, game := newGame(t, r)
	var email string
	_ = r.pool.QueryRow(ctx, `select email from developers where id = $1`, owner).Scan(&email)
	inv, _ := r.SaveGameInvite(ctx, game, email, owner, inviteNow, inviteNow.Add(time.Hour))

	g, err := r.AcceptGameInvite(ctx, inv.ID, owner, email, inviteNow)
	if err != nil || g.Role != RoleOwner {
		t.Fatalf("accept = %+v, %v; want the owner role kept", g, err)
	}
	if len(invitesOf(t, r, game)) != 0 {
		t.Error("the invite was not used up")
	}
}

func TestDeclineAndDeleteGameInvite(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	owner, game := newGame(t, r)
	_, game2 := newGame(t, r)
	email := inviteEmail("gina")
	inv, _ := r.SaveGameInvite(ctx, game, email, owner, inviteNow, inviteNow.Add(time.Hour))

	if _, err := r.DeclineGameInvite(ctx, inv.ID, inviteEmail("someone-else")); !errors.Is(err, ErrNotFound) {
		t.Errorf("decline by another address: %v, want ErrNotFound", err)
	}
	if _, err := r.DeleteGameInvite(ctx, game2, inv.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cancel through another game: %v, want ErrNotFound", err)
	}
	for _, bad := range []string{"not-a-uuid", unknownID} {
		if _, err := r.DeleteGameInvite(ctx, game, bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("cancel %q: %v, want ErrNotFound", bad, err)
		}
		if _, err := r.DeclineGameInvite(ctx, bad, email); !errors.Is(err, ErrNotFound) {
			t.Errorf("decline %q: %v, want ErrNotFound", bad, err)
		}
	}
	if len(invitesOf(t, r, game)) != 1 {
		t.Fatal("a refused call deleted the invite")
	}
	if declinedGame, err := r.DeclineGameInvite(ctx, inv.ID, email); err != nil || declinedGame != game {
		t.Fatalf("decline: %v", err)
	}
	if _, ok := rolesOf(t, r, game)[email]; ok || len(invitesOf(t, r, game)) != 0 {
		t.Error("declining left the invite or made a member")
	}

	inv2, _ := r.SaveGameInvite(ctx, game, email, owner, inviteNow, inviteNow.Add(time.Hour))
	if cancelled, err := r.DeleteGameInvite(ctx, game, inv2.ID); err != nil || cancelled != email || len(invitesOf(t, r, game)) != 0 {
		t.Errorf("cancel: %v, rows %v", err, invitesOf(t, r, game))
	}
}

func TestDeletingAGameDeletesItsInvites(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	owner, game := newGame(t, r)
	email := inviteEmail("hank")
	_, _ = r.SaveGameInvite(ctx, game, email, owner, inviteNow, inviteNow.Add(time.Hour))
	if _, err := r.pool.Exec(ctx, `delete from games where id = $1`, game); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = r.pool.QueryRow(ctx, `select (select count(*) from game_invites where email = $1) + (select count(*) from game_invite_log where email = $1)`, email).Scan(&n)
	if n != 0 {
		t.Errorf("%d invite rows outlived the game", n)
	}
}
