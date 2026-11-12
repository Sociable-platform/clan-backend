package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/service"
)

// adminSvc lets every request through as admin "a1"; each test sets the one call it exercises.
func adminSvc(f fakeService) fakeService {
	f.authAdmin = func(string) (string, error) { return "a1", nil }
	return f
}

func adminGet(t *testing.T, svc Service, path string) *httptest.ResponseRecorder {
	t.Helper()
	return call(t, svc, discardLog(), http.MethodGet, path, "", "admin-token")
}

func TestListAdminDevelopers(t *testing.T) {
	created := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	var got service.ListDevelopersInput
	svc := adminSvc(fakeService{listDevelopers: func(in service.ListDevelopersInput) (service.DeveloperPage, error) {
		got = in
		return service.DeveloperPage{
			Developers: []service.DeveloperSummary{{ID: "d1", Email: "dev@example.com", Name: "Dev", Status: "suspended", GameCount: 2, CreatedAt: created}},
			NextCursor: "abc",
		}, nil
	}})

	rec := adminGet(t, svc, "/api/v1/admin/developers?q=dev&status=suspended&limit=10&cursor=xyz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got != (service.ListDevelopersInput{Query: "dev", Status: "suspended", Limit: 10, Cursor: "xyz"}) {
		t.Fatalf("service got %+v", got)
	}
	want := `{"developers":[{"id":"d1","email":"dev@example.com","name":"Dev","status":"suspended","gameCount":2,"createdAt":"2026-11-06T10:00:00Z"}],"nextCursor":"abc"}`
	if strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("body\n%s\nwant\n%s", rec.Body.String(), want)
	}
}

func TestListAdminDevelopers_EmptyAndLastPage(t *testing.T) {
	svc := adminSvc(fakeService{listDevelopers: func(service.ListDevelopersInput) (service.DeveloperPage, error) {
		return service.DeveloperPage{}, nil
	}})
	rec := adminGet(t, svc, "/api/v1/admin/developers")
	if strings.TrimSpace(rec.Body.String()) != `{"developers":[],"nextCursor":null}` {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestListAdminDevelopers_BadLimit(t *testing.T) {
	svc := adminSvc(fakeService{listDevelopers: func(service.ListDevelopersInput) (service.DeveloperPage, error) {
		t.Fatal("service must not be called with an unparsable limit")
		return service.DeveloperPage{}, nil
	}})
	for _, limit := range []string{"abc", "0", "1.5", "-"} {
		rec := adminGet(t, svc, "/api/v1/admin/developers?limit="+limit)
		if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_limit" {
			t.Errorf("limit=%s: %d %s", limit, rec.Code, rec.Body.String())
		}
	}
}

func TestListAdminDevelopers_ServiceErrorsPassThrough(t *testing.T) {
	svc := adminSvc(fakeService{listDevelopers: func(service.ListDevelopersInput) (service.DeveloperPage, error) {
		return service.DeveloperPage{}, httpx.NewError(http.StatusBadRequest, "invalid_status", "Status must be active or suspended")
	}})
	rec := adminGet(t, svc, "/api/v1/admin/developers?status=paused")
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_status" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestGetAdminDeveloper(t *testing.T) {
	created := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	changed := time.Date(2026, 11, 12, 12, 0, 0, 0, time.FixedZone("x", 4*3600))
	var gotID string
	svc := adminSvc(fakeService{adminDeveloper: func(id string) (service.AdminDeveloper, error) {
		gotID = id
		return service.AdminDeveloper{
			ID: id, Email: "dev@example.com", Name: "Dev", Status: "active", CreatedAt: created,
			Games: []service.AdminGame{
				{ID: "g1", Name: "Farm", Description: "d", Status: "suspended", StatusReason: "abuse", StatusChangedAt: &changed, CreatedAt: created},
				{ID: "g2", Name: "Arena", Status: "active", CreatedAt: created},
			},
		}, nil
	}})

	rec := adminGet(t, svc, "/api/v1/admin/developers/d1")
	if rec.Code != http.StatusOK || gotID != "d1" {
		t.Fatalf("status %d, id %q: %s", rec.Code, gotID, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["statusReason"] != nil || body["statusChangedAt"] != nil {
		t.Errorf("a never-suspended developer must show null reason and time: %v", body)
	}
	games := body["games"].([]any)
	g1, g2 := games[0].(map[string]any), games[1].(map[string]any)
	if g1["status"] != "suspended" || g1["statusReason"] != "abuse" || g1["statusChangedAt"] != "2026-11-12T08:00:00Z" {
		t.Errorf("game 1 %v", g1)
	}
	if g2["statusReason"] != nil || g2["statusChangedAt"] != nil {
		t.Errorf("game 2 %v", g2)
	}
}

func TestGetAdminDeveloper_NoGamesIsAnEmptyList(t *testing.T) {
	svc := adminSvc(fakeService{adminDeveloper: func(id string) (service.AdminDeveloper, error) {
		return service.AdminDeveloper{ID: id}, nil
	}})
	if rec := adminGet(t, svc, "/api/v1/admin/developers/d1"); !strings.Contains(rec.Body.String(), `"games":[]`) {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestGetAdminDeveloper_NotFound(t *testing.T) {
	svc := adminSvc(fakeService{adminDeveloper: func(string) (service.AdminDeveloper, error) {
		return service.AdminDeveloper{}, httpx.NewError(http.StatusNotFound, "developer_not_found", "Developer not found")
	}})
	rec := adminGet(t, svc, "/api/v1/admin/developers/nope")
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "developer_not_found" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestListAuditLog(t *testing.T) {
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	var got service.AuditLogInput
	svc := adminSvc(fakeService{auditLog: func(in service.AuditLogInput) (service.AuditPage, error) {
		got = in
		return service.AuditPage{Entries: []service.AuditEntry{
			{ID: "e2", Actor: "a1", Action: "developer.suspended", TargetType: "developer", TargetID: "d1", Reason: "non_payment", Note: "unpaid", CreatedAt: at},
			{ID: "e1", Actor: "a1", Action: "developer.resumed", TargetType: "developer", TargetID: "d1", Reason: "other", CreatedAt: at.Add(-time.Hour)},
		}}, nil
	}})

	rec := adminGet(t, svc, "/api/v1/admin/audit-log?targetId=d1&limit=5&cursor=c")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got != (service.AuditLogInput{TargetID: "d1", Limit: 5, Cursor: "c"}) {
		t.Fatalf("service got %+v", got)
	}
	var body struct {
		Entries    []map[string]any `json:"entries"`
		NextCursor *string          `json:"nextCursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Entries) != 2 || body.NextCursor != nil {
		t.Fatalf("body %s", rec.Body.String())
	}
	e := body.Entries[0]
	if e["id"] != "e2" || e["targetId"] != "d1" || e["targetType"] != "developer" || e["note"] != "unpaid" || e["createdAt"] != "2026-11-12T12:00:00Z" {
		t.Errorf("entry 0 %v", e)
	}
	if body.Entries[1]["note"] != nil {
		t.Errorf("an entry without a note must show null: %v", body.Entries[1])
	}
}

func TestListAuditLog_EmptyAndErrors(t *testing.T) {
	svc := adminSvc(fakeService{auditLog: func(in service.AuditLogInput) (service.AuditPage, error) {
		if in.TargetID == "bad" {
			return service.AuditPage{}, httpx.NewError(http.StatusBadRequest, "invalid_target_id", "targetId must be a UUID")
		}
		return service.AuditPage{}, nil
	}})
	if rec := adminGet(t, svc, "/api/v1/admin/audit-log"); strings.TrimSpace(rec.Body.String()) != `{"entries":[],"nextCursor":null}` {
		t.Fatalf("empty: %s", rec.Body.String())
	}
	if rec := adminGet(t, svc, "/api/v1/admin/audit-log?targetId=bad"); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_target_id" {
		t.Fatalf("bad target: %d %s", rec.Code, rec.Body.String())
	}
	if rec := adminGet(t, svc, "/api/v1/admin/audit-log?limit=x"); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_limit" {
		t.Fatalf("bad limit: %d %s", rec.Code, rec.Body.String())
	}
}

// The three read routes sit behind requireAdmin: a rejected token never reaches the service.
func TestAdminReadRoutesNeedAnAdminToken(t *testing.T) {
	svc := fakeService{authAdmin: func(string) (string, error) {
		return "", httpx.NewError(http.StatusUnauthorized, "unauthorized", "no")
	}}
	for _, path := range []string{"/api/v1/admin/developers", "/api/v1/admin/developers/d1", "/api/v1/admin/audit-log"} {
		if rec := adminGet(t, svc, path); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with a rejected token: %d", path, rec.Code)
		}
		if rec := call(t, svc, discardLog(), http.MethodGet, path, "", ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a token: %d", path, rec.Code)
		}
	}
}
