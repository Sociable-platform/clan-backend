package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

func (f *fakeStore) SetDeveloperStatus(_ context.Context, id string, c repository.StatusChange) (repository.AdminDeveloper, bool, error) {
	f.statusCalls, f.statusTarget, f.statusChange = f.statusCalls+1, id, c
	if f.err != nil {
		return repository.AdminDeveloper{}, false, f.err
	}
	if f.statusDev.ID != id {
		return repository.AdminDeveloper{}, false, repository.ErrNotFound
	}
	return f.statusDev, !f.statusUnchanged, nil
}

func (f *fakeStore) SetGameStatus(_ context.Context, id string, c repository.StatusChange) (repository.AdminGame, bool, error) {
	f.statusCalls, f.statusTarget, f.statusChange = f.statusCalls+1, id, c
	if f.err != nil {
		return repository.AdminGame{}, false, f.err
	}
	if f.statusGame.ID != id {
		return repository.AdminGame{}, false, repository.ErrNotFound
	}
	return f.statusGame, !f.statusUnchanged, nil
}

func TestSetDeveloperStatus_Suspend(t *testing.T) {
	svc, store, _ := newTestService(t)
	changed := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	store.statusDev = repository.AdminDeveloper{ID: devID1, Email: "dev@example.com", Status: "suspended", StatusReason: "non_payment", StatusChangedAt: &changed}
	store.adminGames = []repository.AdminGame{{ID: "g1", Name: "Farm", Status: "active"}}

	d, err := svc.SetDeveloperStatus(context.Background(), "admin-1", devID1,
		SetStatusInput{Status: "suspended", Reason: " non_payment ", Note: "  Invoice 1042 unpaid  "})
	if err != nil {
		t.Fatal(err)
	}
	want := repository.StatusChange{Actor: "admin-1", Action: "developer.suspended", Status: "suspended", Reason: "non_payment", Note: "Invoice 1042 unpaid"}
	if store.statusChange != want || store.statusTarget != devID1 {
		t.Fatalf("store got %+v for %s, want %+v for %s", store.statusChange, store.statusTarget, want, devID1)
	}
	if d.Status != "suspended" || d.StatusReason != "non_payment" || len(d.Games) != 1 || d.Games[0].Status != "active" {
		t.Fatalf("result %+v", d)
	}
}

func TestSetDeveloperStatus_ResumeNeedsNoReason(t *testing.T) {
	svc, store, _ := newTestService(t)
	store.statusDev = repository.AdminDeveloper{ID: devID1, Status: "active"}

	if _, err := svc.SetDeveloperStatus(context.Background(), "admin-1", devID1, SetStatusInput{Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if c := store.statusChange; c.Action != "developer.resumed" || c.Status != "active" || c.Reason != "" {
		t.Fatalf("change %+v", c)
	}
	// A reason is still accepted when resuming, if it is a known one.
	if _, err := svc.SetDeveloperStatus(context.Background(), "admin-1", devID1, SetStatusInput{Status: "active", Reason: "other"}); err != nil {
		t.Fatal(err)
	}
}

func TestSetGameStatus(t *testing.T) {
	svc, store, _ := newTestService(t)
	store.statusGame = repository.AdminGame{ID: devID2, Name: "Farm", Status: "suspended", StatusReason: "abuse"}

	g, err := svc.SetGameStatus(context.Background(), "system:billing", devID2, SetStatusInput{Status: "suspended", Reason: "abuse"})
	if err != nil {
		t.Fatal(err)
	}
	if c := store.statusChange; c.Actor != "system:billing" || c.Action != "game.suspended" {
		t.Fatalf("change %+v", c)
	}
	if g.ID != devID2 || g.Status != "suspended" || g.StatusReason != "abuse" {
		t.Fatalf("result %+v", g)
	}

	if _, err := svc.SetGameStatus(context.Background(), "admin-1", devID2, SetStatusInput{Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if store.statusChange.Action != "game.resumed" {
		t.Fatalf("action %q", store.statusChange.Action)
	}
}

func TestSetStatus_RejectsBadInputBeforeTheStore(t *testing.T) {
	svc, store, _ := newTestService(t)
	store.statusDev = repository.AdminDeveloper{ID: devID1}
	store.statusGame = repository.AdminGame{ID: devID1}

	tests := []struct {
		name string
		in   SetStatusInput
		code string
	}{
		{"suspend without a reason", SetStatusInput{Status: "suspended"}, "reason_required"},
		{"suspend with a blank reason", SetStatusInput{Status: "suspended", Reason: "   "}, "reason_required"},
		{"unknown reason", SetStatusInput{Status: "suspended", Reason: "bored"}, "invalid_reason"},
		{"unknown reason when resuming", SetStatusInput{Status: "active", Reason: "bored"}, "invalid_reason"},
		{"no status", SetStatusInput{Reason: "abuse"}, "invalid_status"},
		{"unknown status", SetStatusInput{Status: "paused", Reason: "abuse"}, "invalid_status"},
		{"status in another case", SetStatusInput{Status: "Suspended", Reason: "abuse"}, "invalid_status"},
		{"note too long", SetStatusInput{Status: "suspended", Reason: "abuse", Note: strings.Repeat("é", maxStatusNoteLen+1)}, "invalid_note"},
	}
	for _, tt := range tests {
		t.Run("developer: "+tt.name, func(t *testing.T) {
			_, err := svc.SetDeveloperStatus(context.Background(), "admin-1", devID1, tt.in)
			wantError(t, err, http.StatusBadRequest, tt.code)
		})
		t.Run("game: "+tt.name, func(t *testing.T) {
			_, err := svc.SetGameStatus(context.Background(), "admin-1", devID1, tt.in)
			wantError(t, err, http.StatusBadRequest, tt.code)
		})
	}
	if store.statusCalls != 0 {
		t.Fatalf("the store was called %d times for invalid requests", store.statusCalls)
	}
}

func TestSetStatus_NoteAtTheLimitIsAccepted(t *testing.T) {
	svc, store, _ := newTestService(t)
	store.statusDev = repository.AdminDeveloper{ID: devID1}
	in := SetStatusInput{Status: "suspended", Reason: "other", Note: strings.Repeat("é", maxStatusNoteLen)} // counted in characters, not bytes
	if _, err := svc.SetDeveloperStatus(context.Background(), "admin-1", devID1, in); err != nil {
		t.Fatal(err)
	}
}

func TestSetStatus_NotFound(t *testing.T) {
	svc, store, _ := newTestService(t)
	store.statusDev = repository.AdminDeveloper{ID: devID1}
	store.statusGame = repository.AdminGame{ID: devID1}
	in := SetStatusInput{Status: "suspended", Reason: "abuse"}

	for name, id := range map[string]string{"unknown": devID2, "malformed": "not-a-uuid"} {
		t.Run("developer "+name, func(t *testing.T) {
			_, err := svc.SetDeveloperStatus(context.Background(), "admin-1", id, in)
			wantError(t, err, http.StatusNotFound, "developer_not_found")
		})
		t.Run("game "+name, func(t *testing.T) {
			_, err := svc.SetGameStatus(context.Background(), "admin-1", id, in)
			wantError(t, err, http.StatusNotFound, "game_not_found")
		})
	}
}

func TestSetStatus_NoActorIsAnInternalError(t *testing.T) {
	svc, store, _ := newTestService(t)
	store.statusDev = repository.AdminDeveloper{ID: devID1}
	_, err := svc.SetDeveloperStatus(context.Background(), "", devID1, SetStatusInput{Status: "suspended", Reason: "abuse"})
	var e *httpx.Error
	if err == nil || errors.As(err, &e) || store.statusCalls != 0 {
		t.Fatalf("want a plain internal error and no store call, got %v (calls %d)", err, store.statusCalls)
	}
}

func TestSetStatus_StoreFailureIsNotAnAPIError(t *testing.T) {
	svc, store, _ := newTestService(t)
	store.err = errors.New("connection refused")
	in := SetStatusInput{Status: "suspended", Reason: "abuse"}
	var e *httpx.Error
	if _, err := svc.SetDeveloperStatus(context.Background(), "admin-1", devID1, in); err == nil || errors.As(err, &e) {
		t.Fatalf("developer: want a plain internal error, got %v", err)
	}
	if _, err := svc.SetGameStatus(context.Background(), "admin-1", devID1, in); err == nil || errors.As(err, &e) {
		t.Fatalf("game: want a plain internal error, got %v", err)
	}
}
