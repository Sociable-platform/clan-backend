package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

func adminPut(t *testing.T, svc Service, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return call(t, svc, discardLog(), http.MethodPut, path, body, "admin-token")
}

func TestSetDeveloperStatus(t *testing.T) {
	changed := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	var gotActor, gotID string
	var gotIn service.SetStatusInput
	svc := adminSvc(fakeService{setDeveloperStatus: func(actor, id string, in service.SetStatusInput) (service.AdminDeveloper, error) {
		gotActor, gotID, gotIn = actor, id, in
		return service.AdminDeveloper{
			ID: id, Email: "dev@example.com", Name: "Dev", Status: "suspended", StatusReason: "non_payment", StatusChangedAt: &changed,
			CreatedAt: changed, Games: []service.AdminGame{{ID: "g1", Name: "Farm", Status: "active", CreatedAt: changed}},
		}, nil
	}})

	rec := adminPut(t, svc, "/api/v1/admin/developers/d1/status", `{"status":"suspended","reason":"non_payment","note":"unpaid"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if gotActor != "a1" || gotID != "d1" || gotIn != (service.SetStatusInput{Status: "suspended", Reason: "non_payment", Note: "unpaid"}) {
		t.Fatalf("service got actor %q, id %q, %+v (the actor must be the admin from the token)", gotActor, gotID, gotIn)
	}
	body := rec.Body.String()
	for _, want := range []string{`"status":"suspended"`, `"statusReason":"non_payment"`, `"statusChangedAt":"2026-11-12T12:00:00Z"`, `"games":[{"id":"g1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %s: %s", want, body)
		}
	}
	if strings.Contains(body, "unpaid") || strings.Contains(body, `"note"`) {
		t.Errorf("the note must not come back: %s", body)
	}
}

func TestSetGameStatus(t *testing.T) {
	changed := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	var gotActor, gotID string
	var gotIn service.SetStatusInput
	svc := adminSvc(fakeService{setGameStatus: func(actor, id string, in service.SetStatusInput) (service.AdminGame, error) {
		gotActor, gotID, gotIn = actor, id, in
		return service.AdminGame{ID: id, Name: "Farm", Status: "active", CreatedAt: changed, StatusChangedAt: &changed}, nil
	}})

	rec := adminPut(t, svc, "/api/v1/admin/games/g1/status", `{"status":"active"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if gotActor != "a1" || gotID != "g1" || gotIn != (service.SetStatusInput{Status: "active"}) {
		t.Fatalf("service got actor %q, id %q, %+v", gotActor, gotID, gotIn)
	}
	if !strings.Contains(rec.Body.String(), `"statusReason":null`) || !strings.Contains(rec.Body.String(), `"status":"active"`) {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestSetStatus_BadBody(t *testing.T) {
	svc := adminSvc(fakeService{
		setDeveloperStatus: func(string, string, service.SetStatusInput) (service.AdminDeveloper, error) {
			t.Fatal("service must not be called")
			return service.AdminDeveloper{}, nil
		},
		setGameStatus: func(string, string, service.SetStatusInput) (service.AdminGame, error) {
			t.Fatal("service must not be called")
			return service.AdminGame{}, nil
		},
	})
	for _, path := range []string{"/api/v1/admin/developers/d1/status", "/api/v1/admin/games/g1/status"} {
		for name, body := range map[string]string{
			"not json":      `x`,
			"unknown field": `{"status":"active","developerId":"other"}`,
			"empty body":    ``,
		} {
			rec := adminPut(t, svc, path, body)
			if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_json" {
				t.Errorf("%s %s: %d %s", path, name, rec.Code, rec.Body.String())
			}
		}
	}
}

func TestSetStatus_ServiceErrorsPassThrough(t *testing.T) {
	svc := adminSvc(fakeService{
		setDeveloperStatus: func(string, string, service.SetStatusInput) (service.AdminDeveloper, error) {
			return service.AdminDeveloper{}, httpx.NewError(http.StatusBadRequest, "reason_required", "A reason is required")
		},
		setGameStatus: func(string, string, service.SetStatusInput) (service.AdminGame, error) {
			return service.AdminGame{}, httpx.NewError(http.StatusNotFound, "game_not_found", "Game not found")
		},
	})
	if rec := adminPut(t, svc, "/api/v1/admin/developers/d1/status", `{"status":"suspended"}`); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "reason_required" {
		t.Errorf("developer: %d %s", rec.Code, rec.Body.String())
	}
	if rec := adminPut(t, svc, "/api/v1/admin/games/g1/status", `{"status":"active"}`); rec.Code != http.StatusNotFound || errorCode(t, rec) != "game_not_found" {
		t.Errorf("game: %d %s", rec.Code, rec.Body.String())
	}
}

// The status routes need an admin token, and a developer is refused before anything is changed.
func TestSetStatus_NeedsAnAdminToken(t *testing.T) {
	svc := fakeService{authAdmin: func(string) (string, error) {
		return "", httpx.NewError(http.StatusUnauthorized, "unauthorized", "no")
	}}
	for _, path := range []string{"/api/v1/admin/developers/d1/status", "/api/v1/admin/games/g1/status"} {
		if rec := adminPut(t, svc, path, `{"status":"active"}`); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with a rejected token: %d", path, rec.Code)
		}
		if rec := call(t, svc, discardLog(), http.MethodPut, path, `{"status":"active"}`, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a token: %d", path, rec.Code)
		}
	}
}
