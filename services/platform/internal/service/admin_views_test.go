package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

func (f *fakeStore) ListDevelopers(_ context.Context, flt repository.DeveloperFilter) ([]repository.DeveloperSummary, error) {
	f.devFilter = flt
	if f.err != nil {
		return nil, f.err
	}
	return truncate(f.devRows, flt.Limit), nil
}

func (f *fakeStore) AdminDeveloperByID(_ context.Context, id string) (repository.AdminDeveloper, error) {
	if f.err != nil {
		return repository.AdminDeveloper{}, f.err
	}
	if f.adminDev.ID != id {
		return repository.AdminDeveloper{}, repository.ErrNotFound
	}
	return f.adminDev, nil
}

func (f *fakeStore) AdminGamesByDeveloper(context.Context, string) ([]repository.AdminGame, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.adminGames, nil
}

func (f *fakeStore) AuditEntries(_ context.Context, flt repository.AuditFilter) ([]repository.AuditEntry, error) {
	f.auditFilter = flt
	if f.err != nil {
		return nil, f.err
	}
	return truncate(f.auditRows, flt.Limit), nil
}

// truncate returns at most n rows, like a SQL limit.
func truncate[T any](rows []T, n int) []T {
	if len(rows) > n {
		return rows[:n]
	}
	return rows
}

const (
	devID1 = "00000000-0000-4000-8000-000000000001"
	devID2 = "00000000-0000-4000-8000-000000000002"
	devID3 = "00000000-0000-4000-8000-000000000003"
)

func summaries(n int) []repository.DeveloperSummary {
	base := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	ids := []string{devID3, devID2, devID1} // newest first
	out := make([]repository.DeveloperSummary, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, repository.DeveloperSummary{
			ID: ids[i], Email: "dev" + ids[i][len(ids[i])-1:] + "@example.com", Name: "Dev", Status: "active",
			GameCount: i, CreatedAt: base.Add(-time.Duration(i) * time.Hour),
		})
	}
	return out
}

func TestListDevelopers_PagesWithACursor(t *testing.T) {
	svc, store, _ := newTestService(t)
	store.devRows = summaries(3)
	ctx := context.Background()

	first, err := svc.ListDevelopers(ctx, ListDevelopersInput{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if store.devFilter.Limit != 3 {
		t.Fatalf("asked the store for %d rows, want limit+1 = 3", store.devFilter.Limit)
	}
	if len(first.Developers) != 2 || first.Developers[0].ID != devID3 || first.Developers[1].GameCount != 1 {
		t.Fatalf("first page %+v", first.Developers)
	}
	if first.NextCursor == "" {
		t.Fatal("a third row exists, so there must be a next cursor")
	}

	// The cursor points just after the last row shown.
	if _, err := svc.ListDevelopers(ctx, ListDevelopersInput{Limit: 2, Cursor: first.NextCursor}); err != nil {
		t.Fatal(err)
	}
	after := store.devFilter.After
	if after == nil || after.ID != devID2 || !after.CreatedAt.Equal(store.devRows[1].CreatedAt) {
		t.Fatalf("cursor decoded to %+v, want the second row (%s, %s)", after, devID2, store.devRows[1].CreatedAt)
	}
}

func TestListDevelopers_LastPageHasNoCursor(t *testing.T) {
	svc, store, _ := newTestService(t)
	store.devRows = summaries(2)

	page, err := svc.ListDevelopers(context.Background(), ListDevelopersInput{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Developers) != 2 || page.NextCursor != "" {
		t.Fatalf("got %d rows, cursor %q; want 2 rows and no cursor", len(page.Developers), page.NextCursor)
	}

	store.devRows = nil
	page, err = svc.ListDevelopers(context.Background(), ListDevelopersInput{})
	if err != nil || page.Developers == nil || len(page.Developers) != 0 || page.NextCursor != "" {
		t.Fatalf("empty result: %+v, %v (want an empty, non-nil list)", page, err)
	}
}

func TestListDevelopers_PassesFiltersAndDefaults(t *testing.T) {
	svc, store, _ := newTestService(t)

	if _, err := svc.ListDevelopers(context.Background(), ListDevelopersInput{Query: "  Ali ", Status: "suspended"}); err != nil {
		t.Fatal(err)
	}
	f := store.devFilter
	if f.Query != "Ali" || f.Status != "suspended" || f.After != nil || f.Limit != defaultPageSize+1 {
		t.Fatalf("filter %+v", f)
	}
}

func TestListDevelopers_RejectsBadInput(t *testing.T) {
	svc, store, _ := newTestService(t)
	store.devRows = summaries(1)
	tests := []struct {
		name string
		in   ListDevelopersInput
		code string
	}{
		{"negative limit", ListDevelopersInput{Limit: -1}, "invalid_limit"},
		{"limit over the maximum", ListDevelopersInput{Limit: maxPageSize + 1}, "invalid_limit"},
		{"unknown status", ListDevelopersInput{Status: "paused"}, "invalid_status"},
		{"search text too long", ListDevelopersInput{Query: strings.Repeat("a", maxSearchLen+1)}, "invalid_query"},
		{"cursor is not base64", ListDevelopersInput{Cursor: "!!!"}, "invalid_cursor"},
		{"cursor is not ours", ListDevelopersInput{Cursor: "eyJhIjoxfQ"}, "invalid_cursor"}, // {"a":1}
		{"cursor too long", ListDevelopersInput{Cursor: strings.Repeat("a", maxCursorLen+1)}, "invalid_cursor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.ListDevelopers(context.Background(), tt.in)
			wantError(t, err, http.StatusBadRequest, tt.code)
		})
	}
	if store.devFilter.Limit != 0 {
		t.Fatal("a bad request must not reach the store")
	}
}

func TestListDevelopers_StoreFailureIsNotAnAPIError(t *testing.T) {
	svc, store, _ := newTestService(t)
	store.err = errors.New("connection refused")
	if _, err := svc.ListDevelopers(context.Background(), ListDevelopersInput{}); err == nil || strings.Contains(err.Error(), "invalid") {
		t.Fatalf("want a plain internal error, got %v", err)
	}
}

func TestAdminDeveloper(t *testing.T) {
	svc, store, _ := newTestService(t)
	changed := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	store.adminDev = repository.AdminDeveloper{
		ID: devID1, Email: "dev@example.com", Name: "Dev", Status: "suspended", StatusReason: "non_payment", StatusChangedAt: &changed,
	}
	store.adminGames = []repository.AdminGame{
		{ID: "g1", Name: "Farm", Status: "suspended", StatusReason: "abuse", StatusChangedAt: &changed},
		{ID: "g2", Name: "Arena", Status: "active"},
	}

	d, err := svc.AdminDeveloper(context.Background(), devID1)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != "suspended" || d.StatusReason != "non_payment" || len(d.Games) != 2 ||
		d.Games[0].StatusReason != "abuse" || d.Games[1].Status != "active" {
		t.Fatalf("unexpected detail %+v", d)
	}

	store.adminGames = nil
	d, err = svc.AdminDeveloper(context.Background(), devID1)
	if err != nil || d.Games == nil || len(d.Games) != 0 {
		t.Fatalf("no games: %+v, %v (want an empty, non-nil list)", d.Games, err)
	}
}

func TestAdminDeveloper_NotFound(t *testing.T) {
	svc, store, _ := newTestService(t)
	store.adminDev = repository.AdminDeveloper{ID: devID1}
	for name, id := range map[string]string{"unknown": devID2, "malformed": "not-a-uuid", "empty": ""} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.AdminDeveloper(context.Background(), id)
			wantError(t, err, http.StatusNotFound, "developer_not_found")
		})
	}
}

func TestAuditLog_FilterPagingAndMapping(t *testing.T) {
	svc, store, _ := newTestService(t)
	base := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	store.auditRows = []repository.AuditEntry{
		{ID: devID3, Actor: "a1", Action: "developer.resumed", TargetType: "developer", TargetID: devID1, Reason: "other", CreatedAt: base},
		{ID: devID2, Actor: "a1", Action: "developer.suspended", TargetType: "developer", TargetID: devID1, Reason: "non_payment", Note: "unpaid", CreatedAt: base.Add(-time.Hour)},
		{ID: devID1, Actor: "a1", Action: "game.suspended", TargetType: "game", TargetID: devID2, Reason: "abuse", CreatedAt: base.Add(-2 * time.Hour)},
	}

	page, err := svc.AuditLog(context.Background(), AuditLogInput{TargetID: devID1, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if store.auditFilter.TargetID != devID1 || store.auditFilter.Limit != 3 {
		t.Fatalf("filter %+v", store.auditFilter)
	}
	if len(page.Entries) != 2 || page.Entries[0].Action != "developer.resumed" || page.Entries[1].Note != "unpaid" {
		t.Fatalf("entries %+v", page.Entries)
	}
	if page.NextCursor == "" {
		t.Fatal("want a next cursor")
	}
	if _, err := svc.AuditLog(context.Background(), AuditLogInput{Limit: 2, Cursor: page.NextCursor}); err != nil {
		t.Fatal(err)
	}
	if a := store.auditFilter.After; a == nil || a.ID != devID2 || store.auditFilter.TargetID != "" {
		t.Fatalf("second request filter %+v", store.auditFilter)
	}
}

func TestAuditLog_RejectsBadInput(t *testing.T) {
	svc, _, _ := newTestService(t)
	tests := []struct {
		name string
		in   AuditLogInput
		code string
	}{
		{"target is not a uuid", AuditLogInput{TargetID: "abc"}, "invalid_target_id"},
		{"limit over the maximum", AuditLogInput{Limit: 101}, "invalid_limit"},
		{"bad cursor", AuditLogInput{Cursor: "x"}, "invalid_cursor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.AuditLog(context.Background(), tt.in)
			wantError(t, err, http.StatusBadRequest, tt.code)
		})
	}
}
