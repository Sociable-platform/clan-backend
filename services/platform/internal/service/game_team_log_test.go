package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

type fakeTeamLogRow struct {
	id     string
	gameID string
	in     repository.TeamLogInput
	at     time.Time
}

var errLogDown = errors.New("log down")

func (f *fakeStore) AddTeamLog(_ context.Context, gameID string, in repository.TeamLogInput) error {
	if f.addLogErr != nil {
		return f.addLogErr
	}
	n := len(f.teamLog) + 1
	f.teamLog = append(f.teamLog, fakeTeamLogRow{
		id: fmt.Sprintf("00000000-0000-4000-b000-%012d", n), gameID: gameID, in: in, at: time.Unix(1_900_000_000+int64(n), 0),
	})
	return nil
}

func (f *fakeStore) developerByID(id string) (repository.Developer, bool) {
	for _, d := range f.byEmail {
		if d.ID == id {
			return d, true
		}
	}
	return repository.Developer{}, false
}

// TeamLogOfGame follows the SQL: newest first, after the cursor, at most limit rows, with the names joined in.
func (f *fakeStore) TeamLogOfGame(_ context.Context, gameID string, after *repository.Cursor, limit int) ([]repository.TeamLogEntry, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := []repository.TeamLogEntry{}
	for i := len(f.teamLog) - 1; i >= 0; i-- {
		row := f.teamLog[i]
		if row.gameID != gameID {
			continue
		}
		if after != nil && !(row.at.Before(after.CreatedAt) || (row.at.Equal(after.CreatedAt) && row.id < after.ID)) {
			continue
		}
		a, _ := f.developerByID(row.in.ActorID)
		e := repository.TeamLogEntry{
			ID: row.id, Action: row.in.Action, ActorID: row.in.ActorID, ActorName: a.Name, ActorEmail: a.Email,
			SubjectEmail: row.in.SubjectEmail, Role: row.in.Role, CreatedAt: row.at,
		}
		if s, ok := f.developerByID(row.in.SubjectID); ok {
			e.SubjectID, e.SubjectName, e.SubjectEmail = s.ID, s.Name, s.Email
		}
		out = append(out, e)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// actions lists what the log of a game says, oldest first, as "action actor>subject".
func actions(t *testing.T, svc *Service, game Game) []string {
	t.Helper()
	var out []string
	for _, r := range svc.store.(*fakeStore).teamLog {
		if r.gameID != game.ID {
			continue
		}
		subject := r.in.SubjectID
		if subject == "" {
			subject = r.in.SubjectEmail
		}
		line := fmt.Sprintf("%s %s>%s", r.in.Action, r.in.ActorID, subject)
		if r.in.Role != "" {
			line += " " + r.in.Role
		}
		out = append(out, line)
	}
	return out
}

func wantActions(t *testing.T, svc *Service, game Game, want ...string) {
	t.Helper()
	got := actions(t, svc, game)
	if len(got) != len(want) {
		t.Fatalf("log = %q\nwant  %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("log[%d] = %q, want %q (log: %q)", i, got[i], want[i], got)
		}
	}
}

func TestEveryTeamChangeIsLoggedWithWhoDidIt(t *testing.T) {
	svc, alice, bob, game, _, _ := inviteSetup(t)
	ctx := context.Background()
	carol, err := svc.Register(ctx, RegisterInput{Email: "carol@example.com", Password: "correct horse", Name: "Carol"})
	if err != nil {
		t.Fatal(err)
	}
	dave, err := svc.Register(ctx, RegisterInput{Email: "dave@example.com", Password: "correct horse", Name: "Dave"})
	if err != nil {
		t.Fatal(err)
	}

	// invite, decline, invite again, accept
	inv, _ := svc.InviteToGame(ctx, alice.ID, game.ID, "bob@example.com")
	if err := svc.DeclineInvite(ctx, bob.ID, inv.ID); err != nil {
		t.Fatal(err)
	}
	inv, _ = svc.InviteToGame(ctx, alice.ID, game.ID, "bob@example.com")
	if _, err := svc.AcceptInvite(ctx, bob.ID, inv.ID); err != nil {
		t.Fatal(err)
	}
	// invite and cancel
	inv2, _ := svc.InviteToGame(ctx, alice.ID, game.ID, "carol@example.com")
	if err := svc.CancelGameInvite(ctx, alice.ID, game.ID, inv2.ID); err != nil {
		t.Fatal(err)
	}
	// invite and accept Carol and Dave, promote, remove, leave, transfer
	for _, d := range []Developer{carol, dave} {
		i, _ := svc.InviteToGame(ctx, alice.ID, game.ID, d.Email)
		if _, err := svc.AcceptInvite(ctx, d.ID, i.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.SetMemberRole(ctx, alice.ID, game.ID, bob.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveGameMember(ctx, alice.ID, game.ID, carol.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.LeaveGame(ctx, dave.ID, game.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TransferOwnership(ctx, alice.ID, game.ID, bob.ID); err == nil {
		t.Fatal("Bob is an owner already, the transfer should be refused")
	}
	if err := svc.RemoveGameMember(ctx, bob.ID, game.ID, alice.ID); err != nil { // Bob removes Alice
		t.Fatal(err)
	}

	wantActions(t, svc, game,
		"invite_sent "+alice.ID+">bob@example.com",
		"invite_declined "+bob.ID+">bob@example.com",
		"invite_sent "+alice.ID+">bob@example.com",
		"invite_accepted "+bob.ID+">"+bob.ID,
		"invite_sent "+alice.ID+">carol@example.com",
		"invite_cancelled "+alice.ID+">carol@example.com",
		"invite_sent "+alice.ID+">carol@example.com",
		"invite_accepted "+carol.ID+">"+carol.ID,
		"invite_sent "+alice.ID+">dave@example.com",
		"invite_accepted "+dave.ID+">"+dave.ID,
		"role_changed "+alice.ID+">"+bob.ID+" owner",
		"member_removed "+alice.ID+">"+carol.ID,
		"member_left "+dave.ID+">"+dave.ID,
		"member_removed "+bob.ID+">"+alice.ID,
	)
}

func TestHandingOverTheGameIsLogged(t *testing.T) {
	svc, alice, bob, carol, game := teamSetup(t)
	ctx := context.Background()
	_ = carol
	if _, err := svc.TransferOwnership(ctx, alice.ID, game.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	wantActions(t, svc, game, "ownership_transferred "+alice.ID+">"+bob.ID)
}

func TestAnOwnerRemovingThemselvesIsRecordedAsLeaving(t *testing.T) {
	svc, alice, bob, _, game := teamSetup(t)
	ctx := context.Background()
	if _, err := svc.SetMemberRole(ctx, alice.ID, game.ID, bob.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveGameMember(ctx, alice.ID, game.ID, alice.ID); err != nil {
		t.Fatal(err)
	}
	wantActions(t, svc, game, "role_changed "+alice.ID+">"+bob.ID+" owner", "member_left "+alice.ID+">"+alice.ID)
}

// What changed nothing is not in the log: a refused change, a role somebody has already, an invitation that was
// refused, and an expired invitation that nobody joined with.
func TestWhatChangedNothingIsNotLogged(t *testing.T) {
	svc, alice, bob, carol, game := teamSetup(t)
	ctx := context.Background()

	_ = svc.LeaveGame(ctx, alice.ID, game.ID)                          // last_owner
	_ = svc.RemoveGameMember(ctx, bob.ID, game.ID, carol.ID)           // owner_required
	_, _ = svc.SetMemberRole(ctx, alice.ID, game.ID, bob.ID, "member") // the role he has already
	_, _ = svc.SetMemberRole(ctx, alice.ID, game.ID, bob.ID, "admin")  // invalid_role
	_, _ = svc.InviteToGame(ctx, alice.ID, game.ID, "bob@example.com") // already_member
	_, _ = svc.InviteToGame(ctx, alice.ID, game.ID, "not an email")    // invalid_email
	_, _ = svc.TransferOwnership(ctx, alice.ID, game.ID, aStranger)    // member_not_found
	_ = svc.RemoveGameMember(ctx, alice.ID, game.ID, aStranger)        // member_not_found
	wantActions(t, svc, game)
}

func TestAnExpiredInvitationIsNotLoggedAsAccepted(t *testing.T) {
	svc, alice, bob, game, now, _ := inviteSetup(t)
	ctx := context.Background()
	inv, _ := svc.InviteToGame(ctx, alice.ID, game.ID, "bob@example.com")
	*now = now.Add(8 * 24 * time.Hour)

	_, err := svc.AcceptInvite(ctx, bob.ID, inv.ID)
	wantError(t, err, http.StatusGone, "invite_expired")

	wantActions(t, svc, game, "invite_sent "+alice.ID+">bob@example.com") // only the invitation itself
	if len(svc.store.(*fakeStore).invites) != 0 {
		t.Error("the expired invitation was kept, not deleted")
	}
}

// A change and its row are one transaction: when the row cannot be written the change fails as a whole.
func TestAFailedLogFailsTheChange(t *testing.T) {
	svc, alice, _, game, _, _ := inviteSetup(t)
	ctx := context.Background()
	svc.store.(*fakeStore).addLogErr = errLogDown

	_, err := svc.InviteToGame(ctx, alice.ID, game.ID, "carol@example.com")

	if !errors.Is(err, errLogDown) {
		t.Fatalf("err = %v, want the log's error (an internal one, not an answer for the client)", err)
	}
	if n := len(svc.store.(*fakeStore).teamLog); n != 0 {
		t.Errorf("%d rows of a failed change", n)
	}
}

func TestAdminTeamLogReadsTheHistoryNewestFirstWithNames(t *testing.T) {
	svc, alice, bob, game, _, _ := inviteSetup(t)
	ctx := context.Background()
	inv, _ := svc.InviteToGame(ctx, alice.ID, game.ID, "bob@example.com")
	_, _ = svc.AcceptInvite(ctx, bob.ID, inv.ID)
	_, _ = svc.InviteToGame(ctx, alice.ID, game.ID, "nobody@example.com") // no account
	_, _ = svc.SetMemberRole(ctx, alice.ID, game.ID, bob.ID, "owner")

	page, err := svc.AdminTeamLog(ctx, game.ID, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 4 || page.NextCursor != "" {
		t.Fatalf("page = %+v", page)
	}
	got := page.Entries
	if got[0].Action != "role_changed" || got[0].Role != "owner" || got[0].Actor.Name != "Alice" || got[0].Subject.Name != "Bob" || got[0].Subject.DeveloperID != bob.ID {
		t.Errorf("newest = %+v, want Alice making Bob an owner", got[0])
	}
	if got[1].Action != "invite_sent" || got[1].Subject.DeveloperID != "" || got[1].Subject.Email != "nobody@example.com" || got[1].Role != "" {
		t.Errorf("second = %+v, want an invitation to an address with no account", got[1])
	}
	if got[2].Action != "invite_accepted" || got[2].Actor.Name != "Bob" || got[2].Actor.Email != "bob@example.com" {
		t.Errorf("third = %+v, want Bob accepting", got[2])
	}
	if got[3].Action != "invite_sent" || got[3].Actor.Name != "Alice" || got[3].Subject.Email != "bob@example.com" {
		t.Errorf("oldest = %+v, want Alice inviting Bob", got[3])
	}
}

func TestAdminTeamLogPages(t *testing.T) {
	svc, alice, _, game, _, _ := inviteSetup(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := svc.InviteToGame(ctx, alice.ID, game.ID, fmt.Sprintf("dev%d@example.com", i)); err != nil {
			t.Fatal(err)
		}
	}

	first, err := svc.AdminTeamLog(ctx, game.ID, 2, "")
	if err != nil || len(first.Entries) != 2 || first.NextCursor == "" {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := svc.AdminTeamLog(ctx, game.ID, 2, first.NextCursor)
	if err != nil || len(second.Entries) != 2 || second.NextCursor == "" {
		t.Fatalf("second = %+v, %v", second, err)
	}
	third, err := svc.AdminTeamLog(ctx, game.ID, 2, second.NextCursor)
	if err != nil || len(third.Entries) != 1 || third.NextCursor != "" {
		t.Fatalf("third = %+v, %v; want the last one and no cursor", third, err)
	}
	seen := map[string]bool{}
	for _, p := range []TeamLogPage{first, second, third} {
		for _, e := range p.Entries {
			if seen[e.ID] {
				t.Errorf("entry %s on two pages", e.ID)
			}
			seen[e.ID] = true
		}
	}
	if len(seen) != 5 {
		t.Errorf("%d entries over the pages, want 5", len(seen))
	}
}

func TestAdminTeamLogErrors(t *testing.T) {
	svc, _, _, game, _, _ := inviteSetup(t)
	ctx := context.Background()

	for _, bad := range []string{"not-a-uuid", aStranger} {
		_, err := svc.AdminTeamLog(ctx, bad, 0, "")
		wantError(t, err, http.StatusNotFound, "game_not_found")
	}
	_, err := svc.AdminTeamLog(ctx, game.ID, 1000, "")
	wantError(t, err, http.StatusBadRequest, "invalid_limit")
	_, err = svc.AdminTeamLog(ctx, game.ID, 0, "garbage")
	wantError(t, err, http.StatusBadRequest, "invalid_cursor")
	page, err := svc.AdminTeamLog(ctx, game.ID, 0, "")
	if err != nil || page.Entries == nil || len(page.Entries) != 0 {
		t.Errorf("a game with no history = %+v, %v; want an empty, non-nil list", page, err)
	}
}
