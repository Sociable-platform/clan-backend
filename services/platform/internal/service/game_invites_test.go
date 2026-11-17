package service

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

type fakeInviteLog struct {
	gameID, email string
	at            time.Time
}

// The invitation methods of fakeStore follow the SQL of repository/game_invites.go.

func (f *fakeStore) PurgeGameInvites(_ context.Context, now time.Time) error {
	if f.err != nil {
		return f.err
	}
	kept := f.invites[:0:0]
	for _, i := range f.invites {
		if i.ExpiresAt.After(now) {
			kept = append(kept, i)
		}
	}
	f.invites = kept
	logs := f.inviteLog[:0:0]
	for _, l := range f.inviteLog {
		if l.at.After(now.Add(-24 * time.Hour)) {
			logs = append(logs, l)
		}
	}
	f.inviteLog = logs
	return nil
}

func (f *fakeStore) EmailIsMember(_ context.Context, gameID, email string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	for _, d := range f.byEmail {
		if d.Email != email {
			continue
		}
		for _, g := range f.games {
			if g.ID == gameID {
				if _, ok := f.gameAs(g, d.ID); ok {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func (f *fakeStore) InviteCountsSince(_ context.Context, gameID, email string, now time.Time) (repository.InviteCounts, error) {
	if f.err != nil {
		return repository.InviteCounts{}, f.err
	}
	var c repository.InviteCounts
	for _, l := range f.inviteLog {
		if l.gameID == gameID && l.at.After(now.Add(-time.Hour)) {
			c.GameLastHour++
		}
		if l.gameID == gameID && l.email == email && l.at.After(now.Add(-24*time.Hour)) {
			c.PairLastDay++
		}
		if l.email == email && l.at.After(now.Add(-24*time.Hour)) {
			c.AddressLastDay++
		}
	}
	return c, nil
}

func (f *fakeStore) SaveGameInvite(_ context.Context, gameID, email, invitedBy string, now, expiresAt time.Time) (repository.GameInvite, error) {
	if f.err != nil {
		return repository.GameInvite{}, f.err
	}
	f.inviteLog = append(f.inviteLog, fakeInviteLog{gameID, email, now})
	name := ""
	for _, d := range f.byEmail {
		if d.ID == invitedBy {
			name = d.Name
		}
	}
	gameName := ""
	for _, g := range f.games {
		if g.ID == gameID {
			gameName = g.Name
		}
	}
	for i, inv := range f.invites {
		if inv.GameID == gameID && inv.Email == email { // renewed: the same row
			f.invites[i].InvitedByName, f.invites[i].CreatedAt, f.invites[i].ExpiresAt = name, now, expiresAt
			return f.invites[i], nil
		}
	}
	inv := repository.GameInvite{ID: fmt.Sprintf("00000000-0000-4000-9000-%012d", len(f.invites)+1+len(f.inviteLog)*1000), GameID: gameID, GameName: gameName,
		Email: email, InvitedByName: name, CreatedAt: now, ExpiresAt: expiresAt}
	f.invites = append(f.invites, inv)
	return inv, nil
}

func (f *fakeStore) GameInvitesOfGame(_ context.Context, gameID string, now time.Time) ([]repository.GameInvite, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := []repository.GameInvite{}
	for i := len(f.invites) - 1; i >= 0; i-- {
		if inv := f.invites[i]; inv.GameID == gameID && inv.ExpiresAt.After(now) {
			out = append(out, inv)
		}
	}
	return out, nil
}

func (f *fakeStore) GameInvitesForEmail(_ context.Context, email string, now time.Time) ([]repository.GameInvite, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := []repository.GameInvite{}
	for i := len(f.invites) - 1; i >= 0; i-- {
		if inv := f.invites[i]; inv.Email == email && inv.ExpiresAt.After(now) {
			out = append(out, inv)
		}
	}
	return out, nil
}

// removeInvite deletes the invites for which match is true and reports whether there was one.
func (f *fakeStore) removeInvite(match func(repository.GameInvite) bool) (repository.GameInvite, bool) {
	for i, inv := range f.invites {
		if match(inv) {
			f.invites = append(f.invites[:i:i], f.invites[i+1:]...)
			return inv, true
		}
	}
	return repository.GameInvite{}, false
}

func (f *fakeStore) DeleteGameInvite(_ context.Context, gameID, inviteID string) error {
	if f.err != nil {
		return f.err
	}
	if _, ok := f.removeInvite(func(i repository.GameInvite) bool { return i.ID == inviteID && i.GameID == gameID }); !ok {
		return repository.ErrNotFound
	}
	return nil
}

func (f *fakeStore) DeclineGameInvite(_ context.Context, inviteID, email string) error {
	if f.err != nil {
		return f.err
	}
	if _, ok := f.removeInvite(func(i repository.GameInvite) bool { return i.ID == inviteID && i.Email == email }); !ok {
		return repository.ErrNotFound
	}
	return nil
}

func (f *fakeStore) AcceptGameInvite(_ context.Context, inviteID, developerID, email string, now time.Time) (repository.Game, error) {
	if f.err != nil {
		return repository.Game{}, f.err
	}
	inv, ok := f.removeInvite(func(i repository.GameInvite) bool { return i.ID == inviteID && i.Email == email })
	if !ok {
		return repository.Game{}, repository.ErrNotFound
	}
	if !inv.ExpiresAt.After(now) {
		return repository.Game{}, repository.ErrInviteExpired
	}
	for _, g := range f.games {
		if g.ID != inv.GameID {
			continue
		}
		if as, member := f.gameAs(g, developerID); member { // on conflict do nothing
			return as, nil
		}
		if f.members == nil {
			f.members = map[string]string{}
		}
		f.members[g.ID+"|"+developerID] = repository.RoleMember
		as, _ := f.gameAs(g, developerID)
		return as, nil
	}
	return repository.Game{}, repository.ErrNotFound
}

// sentInvite is an email the service asked the sender to send.
type sentInvite struct {
	to, inviter, game string
	hasAccount        bool
}

type fakeInviteSender struct{ sent []sentInvite }

func (s *fakeInviteSender) SendGameInvite(to, inviter, game string, hasAccount bool) {
	s.sent = append(s.sent, sentInvite{to, inviter, game, hasAccount})
}

// inviteSetup is a service with a game owned by alice, bob as a second developer, a clock the test moves and a
// sender that records the emails.
func inviteSetup(t *testing.T) (svc *Service, alice, bob Developer, game Game, now *time.Time, sent *fakeInviteSender) {
	t.Helper()
	svc, alice, bob, game = aliceGame(t)
	clock := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return clock }
	sent = &fakeInviteSender{}
	svc.WithInvites(sent)
	return svc, alice, bob, game, &clock, sent
}

func TestInviteToGame_StoresTheInviteAndEmailsTheAddress(t *testing.T) {
	svc, alice, bob, game, now, sent := inviteSetup(t)
	ctx := context.Background()

	inv, err := svc.InviteToGame(ctx, alice.ID, game.ID, "  Carol@Example.COM ")
	if err != nil {
		t.Fatal(err)
	}
	if inv.Email != "carol@example.com" || inv.GameID != game.ID || inv.GameName != "Space Raiders" || inv.InvitedByName != "Alice" {
		t.Errorf("invite = %+v", inv)
	}
	if !inv.ExpiresAt.Equal(now.Add(7 * 24 * time.Hour)) {
		t.Errorf("expires %v, want 7 days from now", inv.ExpiresAt)
	}
	// An address with no account and one with an account get an email each, worded for each.
	if _, err := svc.InviteToGame(ctx, alice.ID, game.ID, "BOB@example.com"); err != nil {
		t.Fatal(err)
	}
	want := []sentInvite{
		{"carol@example.com", "Alice", "Space Raiders", false},
		{"bob@example.com", "Alice", "Space Raiders", true},
	}
	if len(sent.sent) != 2 || sent.sent[0] != want[0] || sent.sent[1] != want[1] {
		t.Errorf("emails = %+v, want %+v", sent.sent, want)
	}
	if list, err := svc.GameInvites(ctx, alice.ID, game.ID); err != nil || len(list) != 2 || list[0].Email != "bob@example.com" {
		t.Errorf("list = %+v, %v; want both, newest first", list, err)
	}
	_ = bob
}

func TestInviteToGame_OnlyOwnersInviteAndAStrangerSeesNoGame(t *testing.T) {
	svc, alice, bob, game, _, sent := inviteSetup(t)
	ctx := context.Background()
	svc.store.(*fakeStore).members = map[string]string{game.ID + "|" + bob.ID: "member"}

	_, err := svc.InviteToGame(ctx, bob.ID, game.ID, "carol@example.com")
	wantError(t, err, http.StatusForbidden, "owner_required")
	_, err = svc.InviteToGame(ctx, aStranger, game.ID, "carol@example.com")
	wantError(t, err, http.StatusNotFound, "game_not_found")
	_, err = svc.GameInvites(ctx, bob.ID, game.ID)
	wantError(t, err, http.StatusForbidden, "owner_required")
	wantError(t, svc.CancelGameInvite(ctx, bob.ID, game.ID, "00000000-0000-4000-9000-000000000001"), http.StatusForbidden, "owner_required")
	if len(sent.sent) != 0 || len(svc.store.(*fakeStore).invites) != 0 {
		t.Errorf("a refused invite left something behind: %+v, %+v", sent.sent, svc.store.(*fakeStore).invites)
	}
	_ = alice
}

func TestInviteToGame_RefusesABadAddressAndSomebodyAlreadyOnTheGame(t *testing.T) {
	svc, alice, bob, game, _, sent := inviteSetup(t)
	ctx := context.Background()
	svc.store.(*fakeStore).members = map[string]string{game.ID + "|" + bob.ID: "member"}

	for _, bad := range []string{"", "not an email", "carol@localhost", "Carol <carol@example.com>"} {
		_, err := svc.InviteToGame(ctx, alice.ID, game.ID, bad)
		wantError(t, err, http.StatusBadRequest, "invalid_email")
	}
	_, err := svc.InviteToGame(ctx, alice.ID, game.ID, "bob@example.com") // a member
	wantError(t, err, http.StatusConflict, "already_member")
	_, err = svc.InviteToGame(ctx, alice.ID, game.ID, "ALICE@example.com") // the owner herself
	wantError(t, err, http.StatusConflict, "already_member")
	if len(sent.sent) != 0 || len(svc.store.(*fakeStore).invites) != 0 {
		t.Errorf("a refused invite left something behind")
	}
}

func TestInviteToGame_InvitingAgainRenewsTheInvite(t *testing.T) {
	svc, alice, _, game, now, sent := inviteSetup(t)
	ctx := context.Background()

	first, err := svc.InviteToGame(ctx, alice.ID, game.ID, "carol@example.com")
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(5 * 24 * time.Hour)
	second, err := svc.InviteToGame(ctx, alice.ID, game.ID, "carol@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || !second.ExpiresAt.Equal(now.Add(7*24*time.Hour)) {
		t.Errorf("renewed invite = %+v, first %+v; want the same invite with a new expiry", second, first)
	}
	if list, _ := svc.GameInvites(ctx, alice.ID, game.ID); len(list) != 1 {
		t.Errorf("%d pending invites, want one per game and email", len(list))
	}
	if len(sent.sent) != 2 {
		t.Errorf("%d emails, want one for each time", len(sent.sent))
	}
}

func TestInviteToGame_IsRateLimited(t *testing.T) {
	t.Run("per game and address: three a day, and a day later it works again", func(t *testing.T) {
		svc, alice, _, game, now, _ := inviteSetup(t)
		ctx := context.Background()
		for i := 0; i < 3; i++ {
			if _, err := svc.InviteToGame(ctx, alice.ID, game.ID, "carol@example.com"); err != nil {
				t.Fatalf("invite %d: %v", i+1, err)
			}
		}
		_, err := svc.InviteToGame(ctx, alice.ID, game.ID, "carol@example.com")
		wantError(t, err, http.StatusTooManyRequests, "rate_limited")
		if _, err := svc.InviteToGame(ctx, alice.ID, game.ID, "dave@example.com"); err != nil {
			t.Errorf("another address is not limited by that: %v", err)
		}
		*now = now.Add(24*time.Hour + time.Minute)
		if _, err := svc.InviteToGame(ctx, alice.ID, game.ID, "carol@example.com"); err != nil {
			t.Errorf("a day later: %v", err)
		}
	})

	t.Run("per game: twenty an hour to anybody", func(t *testing.T) {
		svc, alice, _, game, now, _ := inviteSetup(t)
		ctx := context.Background()
		for i := 0; i < 20; i++ {
			if _, err := svc.InviteToGame(ctx, alice.ID, game.ID, "dev"+strconv.Itoa(i)+"@example.com"); err != nil {
				t.Fatalf("invite %d: %v", i+1, err)
			}
		}
		_, err := svc.InviteToGame(ctx, alice.ID, game.ID, "one-more@example.com")
		wantError(t, err, http.StatusTooManyRequests, "rate_limited")
		*now = now.Add(time.Hour + time.Minute)
		if _, err := svc.InviteToGame(ctx, alice.ID, game.ID, "one-more@example.com"); err != nil {
			t.Errorf("an hour later: %v", err)
		}
	})

	t.Run("per address: five a day from all games together", func(t *testing.T) {
		svc, alice, _, _, _, _ := inviteSetup(t)
		ctx := context.Background()
		for i := 0; i < 5; i++ {
			g, err := svc.RegisterGame(ctx, alice.ID, RegisterGameInput{Name: "Game " + strconv.Itoa(i)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.InviteToGame(ctx, alice.ID, g.ID, "carol@example.com"); err != nil {
				t.Fatalf("game %d: %v", i+1, err)
			}
		}
		g, _ := svc.RegisterGame(ctx, alice.ID, RegisterGameInput{Name: "Game six"})
		_, err := svc.InviteToGame(ctx, alice.ID, g.ID, "carol@example.com")
		wantError(t, err, http.StatusTooManyRequests, "rate_limited")
	})

	t.Run("an invite that was refused is not counted", func(t *testing.T) {
		svc, alice, _, game, _, _ := inviteSetup(t)
		ctx := context.Background()
		for i := 0; i < 5; i++ {
			_, _ = svc.InviteToGame(ctx, alice.ID, game.ID, "alice@example.com") // already_member
		}
		if n := len(svc.store.(*fakeStore).inviteLog); n != 0 {
			t.Errorf("%d log entries for refused invites", n)
		}
	})
}

func TestInviteIsAcceptedOnlyByTheAddressItWasSentTo(t *testing.T) {
	svc, alice, bob, game, _, _ := inviteSetup(t)
	ctx := context.Background()
	carol, err := svc.Register(ctx, RegisterInput{Email: "carol@example.com", Password: "correct horse", Name: "Carol"})
	if err != nil {
		t.Fatal(err)
	}
	inv, err := svc.InviteToGame(ctx, alice.ID, game.ID, "Carol@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// Bob has the invite's ID, but not the address: it is a 404, the same as an invite that does not exist.
	_, err = svc.AcceptInvite(ctx, bob.ID, inv.ID)
	wantError(t, err, http.StatusNotFound, "invite_not_found")
	wantError(t, svc.DeclineInvite(ctx, bob.ID, inv.ID), http.StatusNotFound, "invite_not_found")
	if mine, _ := svc.MyInvites(ctx, bob.ID); len(mine) != 0 {
		t.Errorf("Bob sees %+v", mine)
	}
	if _, err := svc.MemberGame(ctx, bob.ID, game.ID); err == nil {
		t.Fatal("Bob got into the game")
	}

	mine, err := svc.MyInvites(ctx, carol.ID)
	if err != nil || len(mine) != 1 || mine[0].ID != inv.ID || mine[0].GameName != "Space Raiders" || mine[0].InvitedByName != "Alice" {
		t.Fatalf("Carol's invites = %+v, %v", mine, err)
	}
	g, err := svc.AcceptInvite(ctx, carol.ID, inv.ID)
	if err != nil || g.ID != game.ID || g.Role != "member" {
		t.Fatalf("accept = %+v, %v; want the game, as a member", g, err)
	}
	if again, err := svc.MemberGame(ctx, carol.ID, game.ID); err != nil || again.Role != "member" {
		t.Errorf("Carol is not a member afterwards: %+v, %v", again, err)
	}
	// The invite is used up: gone from both lists, and accepting it again is a 404.
	if mine, _ := svc.MyInvites(ctx, carol.ID); len(mine) != 0 {
		t.Errorf("Carol still has %+v", mine)
	}
	if list, _ := svc.GameInvites(ctx, alice.ID, game.ID); len(list) != 0 {
		t.Errorf("the game still lists %+v", list)
	}
	_, err = svc.AcceptInvite(ctx, carol.ID, inv.ID)
	wantError(t, err, http.StatusNotFound, "invite_not_found")
}

func TestInviteToDeclineAndExpire_CreateNothing(t *testing.T) {
	svc, alice, bob, game, now, _ := inviteSetup(t)
	ctx := context.Background()

	declined, _ := svc.InviteToGame(ctx, alice.ID, game.ID, "bob@example.com")
	if err := svc.DeclineInvite(ctx, bob.ID, declined.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MemberGame(ctx, bob.ID, game.ID); err == nil {
		t.Error("declining made Bob a member")
	}
	_, err := svc.AcceptInvite(ctx, bob.ID, declined.ID)
	wantError(t, err, http.StatusNotFound, "invite_not_found")

	expiring, _ := svc.InviteToGame(ctx, alice.ID, game.ID, "bob@example.com")
	*now = now.Add(7 * 24 * time.Hour) // the moment it expires
	if mine, _ := svc.MyInvites(ctx, bob.ID); len(mine) != 0 {
		t.Errorf("an expired invite is listed: %+v", mine)
	}
	if list, _ := svc.GameInvites(ctx, alice.ID, game.ID); len(list) != 0 {
		t.Errorf("an expired invite is listed for the owner: %+v", list)
	}
	_, err = svc.AcceptInvite(ctx, bob.ID, expiring.ID)
	wantError(t, err, http.StatusGone, "invite_expired")
	if _, err := svc.MemberGame(ctx, bob.ID, game.ID); err == nil {
		t.Error("an expired invite made Bob a member")
	}
}

func TestInviteToGame_CleansUpExpiredInvitesAndOldLog(t *testing.T) {
	svc, alice, _, game, now, _ := inviteSetup(t)
	ctx := context.Background()
	if _, err := svc.InviteToGame(ctx, alice.ID, game.ID, "carol@example.com"); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(8 * 24 * time.Hour)
	if _, err := svc.InviteToGame(ctx, alice.ID, game.ID, "dave@example.com"); err != nil {
		t.Fatal(err)
	}
	st := svc.store.(*fakeStore)
	if len(st.invites) != 1 || st.invites[0].Email != "dave@example.com" || len(st.inviteLog) != 1 {
		t.Errorf("after 8 days: invites %+v, log %+v; want only Dave's", st.invites, st.inviteLog)
	}
}

func TestCancelGameInvite(t *testing.T) {
	svc, alice, bob, game, _, _ := inviteSetup(t)
	ctx := context.Background()
	other, _ := svc.RegisterGame(ctx, alice.ID, RegisterGameInput{Name: "Other"})
	inv, _ := svc.InviteToGame(ctx, alice.ID, game.ID, "bob@example.com")

	// Another game of the same owner has no such invite, and neither does a made up ID.
	wantError(t, svc.CancelGameInvite(ctx, alice.ID, other.ID, inv.ID), http.StatusNotFound, "invite_not_found")
	wantError(t, svc.CancelGameInvite(ctx, alice.ID, game.ID, "not-a-uuid"), http.StatusNotFound, "invite_not_found")
	wantError(t, svc.CancelGameInvite(ctx, aStranger, game.ID, inv.ID), http.StatusNotFound, "game_not_found")

	if err := svc.CancelGameInvite(ctx, alice.ID, game.ID, inv.ID); err != nil {
		t.Fatal(err)
	}
	_, err := svc.AcceptInvite(ctx, bob.ID, inv.ID)
	wantError(t, err, http.StatusNotFound, "invite_not_found") // cancelled before Bob answered
	wantError(t, svc.CancelGameInvite(ctx, alice.ID, game.ID, inv.ID), http.StatusNotFound, "invite_not_found")
}

func TestAcceptInvite_AMalformedIDIsNotFound(t *testing.T) {
	svc, _, bob, _, _, _ := inviteSetup(t)
	_, err := svc.AcceptInvite(context.Background(), bob.ID, "not-a-uuid")
	wantError(t, err, http.StatusNotFound, "invite_not_found")
	wantError(t, svc.DeclineInvite(context.Background(), bob.ID, strings.Repeat("x", 40)), http.StatusNotFound, "invite_not_found")
}

func TestInvites_StoreFailureIsAnInternalError(t *testing.T) {
	svc, alice, _, game, _, _ := inviteSetup(t)
	svc.store.(*fakeStore).err = fmt.Errorf("db down")
	if _, err := svc.MyInvites(context.Background(), alice.ID); err == nil || strings.Contains(err.Error(), "invite_") {
		t.Errorf("MyInvites = %v, want a plain wrapped error", err)
	}
	_ = game
}
