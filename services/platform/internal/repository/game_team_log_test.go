package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// insertLog writes a row with a chosen time, so the order and the pages do not depend on the clock.
func insertLog(t *testing.T, r *Repository, game, action, actor, subject, email string, at time.Time) {
	t.Helper()
	var subjectID, subjectEmail *string
	if subject != "" {
		subjectID = &subject
	}
	if email != "" {
		subjectEmail = &email
	}
	if _, err := r.pool.Exec(context.Background(),
		`insert into game_team_log (game_id, action, actor_id, subject_id, subject_email, created_at) values ($1, $2, $3, $4, $5, $6)`,
		game, action, actor, subjectID, subjectEmail, at); err != nil {
		t.Fatal(err)
	}
}

func TestTeamLogRecordsAndReadsNewestFirstWithNames(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	owner, game := newGame(t, r)
	member := newDeveloper(t, r, "member")

	steps := []TeamLogInput{
		{Action: TeamInviteSent, ActorID: owner, SubjectEmail: "someone@example.com"},
		{Action: TeamInviteAccepted, ActorID: member, SubjectID: member, SubjectEmail: "member@example.com"},
		{Action: TeamRoleChanged, ActorID: owner, SubjectID: member, Role: RoleOwner},
		{Action: TeamMemberRemoved, ActorID: owner, SubjectID: member},
	}
	for _, in := range steps {
		if err := r.AddTeamLog(ctx, game, in); err != nil {
			t.Fatalf("%s: %v", in.Action, err)
		}
	}

	got, err := r.TeamLogOfGame(ctx, game, nil, 50)
	if err != nil || len(got) != 4 {
		t.Fatalf("log = %+v, %v; want 4 rows", got, err)
	}
	var order []string
	for _, e := range got {
		order = append(order, e.Action)
	}
	want := []string{TeamMemberRemoved, TeamRoleChanged, TeamInviteAccepted, TeamInviteSent}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want newest first %v", order, want)
		}
	}
	removed, changed, invite := got[0], got[1], got[3]
	if removed.ActorID != owner || removed.ActorName != "Test" || removed.SubjectID != member || removed.SubjectName != "member" || removed.Role != "" {
		t.Errorf("removed = %+v, want the owner removing the member, no role", removed)
	}
	if changed.Role != RoleOwner || changed.SubjectID != member {
		t.Errorf("role change = %+v, want the new role owner for the member", changed)
	}
	// An invitation to an address with no account: no developer, no name, only the address.
	if invite.SubjectID != "" || invite.SubjectName != "" || invite.SubjectEmail != "someone@example.com" || invite.ActorID != owner {
		t.Errorf("invitation = %+v, want the address only", invite)
	}
	// With an account, the account's own email is shown.
	if got[2].SubjectEmail == "" || got[2].ActorID != member {
		t.Errorf("accepted = %+v", got[2])
	}
	if got[0].CreatedAt.IsZero() || got[0].ID == "" {
		t.Errorf("a row without an id or a time: %+v", got[0])
	}
}

func TestTeamLogPagesWithACursor(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	owner, game := newGame(t, r)
	base := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		insertLog(t, r, game, TeamInviteSent, owner, "", fmt.Sprintf("dev%d@example.com", i), base.Add(time.Duration(i)*time.Minute))
	}
	// Two rows at the very same moment: the id breaks the tie, so no row is skipped or shown twice.
	insertLog(t, r, game, TeamInviteSent, owner, "", "tie-a@example.com", base)
	insertLog(t, r, game, TeamInviteSent, owner, "", "tie-b@example.com", base)

	seen := map[string]bool{}
	var after *Cursor
	pages := 0
	for {
		page, err := r.TeamLogOfGame(ctx, game, after, 3)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, e := range page {
			if seen[e.ID] {
				t.Fatalf("row %s on two pages", e.ID)
			}
			seen[e.ID] = true
		}
		if len(page) < 3 {
			break
		}
		last := page[len(page)-1]
		after = &Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	if len(seen) != 7 || pages != 3 {
		t.Errorf("%d rows over %d pages, want 7 over 3", len(seen), pages)
	}
}

func TestTeamLogForAGameWithoutHistoryIsEmpty(t *testing.T) {
	r := testRepo(t)
	_, game := newGame(t, r)
	for _, id := range []string{game, unknownID} {
		got, err := r.TeamLogOfGame(context.Background(), id, nil, 10)
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("log of %s = %#v, %v; want an empty, non-nil list", id, got, err)
		}
	}
}

// The table refuses what the service would never write, so a bug cannot store a row that means nothing.
func TestTeamLogRefusesRowsThatMeanNothing(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	owner, game := newGame(t, r)

	for name, in := range map[string]TeamLogInput{
		"an action that does not exist": {Action: "promoted", ActorID: owner, SubjectID: owner},
		"nobody acted on":               {Action: TeamMemberRemoved, ActorID: owner},
		"a role that does not exist":    {Action: TeamRoleChanged, ActorID: owner, SubjectID: owner, Role: "admin"},
		"nobody who did it":             {Action: TeamMemberLeft, SubjectID: owner},
		"an actor who does not exist":   {Action: TeamMemberLeft, ActorID: unknownID, SubjectID: owner},
	} {
		if err := r.AddTeamLog(ctx, game, in); err == nil {
			t.Errorf("%s: the row was stored", name)
		}
	}
	if err := r.AddTeamLog(ctx, unknownID, TeamLogInput{Action: TeamMemberLeft, ActorID: owner, SubjectID: owner}); err == nil {
		t.Error("a row for a game that does not exist was stored")
	}
}

// A change and its row are one transaction: when either fails, neither is kept.
func TestTeamLogIsPartOfTheChange(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	owner, game := newGame(t, r)
	member := newDeveloper(t, r, "member")
	addMember(t, r, game, member, RoleMember)
	boom := errors.New("boom")

	// The change is made and logged, then something later in the transaction fails: both are undone.
	err := r.InTx(ctx, func(ctx context.Context) error {
		if err := r.RemoveGameMember(ctx, game, member); err != nil {
			return err
		}
		if err := r.AddTeamLog(ctx, game, TeamLogInput{Action: TeamMemberRemoved, ActorID: owner, SubjectID: member}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if rolesOf(t, r, game)[member] != RoleMember {
		t.Error("the removal survived a failed transaction")
	}
	if got, _ := r.TeamLogOfGame(ctx, game, nil, 10); len(got) != 0 {
		t.Errorf("a row of a failed transaction was kept: %+v", got)
	}

	// The same transaction, committed: the change and its row are both there.
	err = r.InTx(ctx, func(ctx context.Context) error {
		if err := r.RemoveGameMember(ctx, game, member); err != nil {
			return err
		}
		return r.AddTeamLog(ctx, game, TeamLogInput{Action: TeamMemberRemoved, ActorID: owner, SubjectID: member})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rolesOf(t, r, game)[member]; ok {
		t.Error("the removal was not kept")
	}
	if got, _ := r.TeamLogOfGame(ctx, game, nil, 10); len(got) != 1 || got[0].Action != TeamMemberRemoved {
		t.Errorf("log = %+v, want the one row", got)
	}
}

func TestTheLogGoesWithTheGame(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	owner, game := newGame(t, r)
	if err := r.AddTeamLog(ctx, game, TeamLogInput{Action: TeamInviteSent, ActorID: owner, SubjectEmail: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pool.Exec(ctx, `delete from games where id = $1`, game); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = r.pool.QueryRow(ctx, `select count(*) from game_team_log where game_id = $1`, game).Scan(&n)
	if n != 0 {
		t.Errorf("%d log rows outlived the game", n)
	}
}

// TEAM-310: the column is gone, so nothing in the repository can read it.
func TestGamesHasNoDeveloperIDColumn(t *testing.T) {
	r := testRepo(t)
	var n int
	err := r.pool.QueryRow(context.Background(),
		`select count(*) from information_schema.columns where table_schema = current_schema() and table_name = 'games' and column_name = 'developer_id'`).Scan(&n)
	if err != nil || n != 0 {
		t.Fatalf("games.developer_id columns = %d, %v; want none (migration 000018)", n, err)
	}
	// And a game is still created, owned through game_members alone.
	owner, game := newGame(t, r)
	if rolesOf(t, r, game)[owner] != RoleOwner {
		t.Error("the developer who registered the game is not its owner")
	}
}
