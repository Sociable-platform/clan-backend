package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func corsServer(t *testing.T) http.Handler {
	t.Helper()
	mw, err := CORS([]string{"http://localhost:5173", " http://localhost:5174/ "})
	if err != nil {
		t.Fatal(err)
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return mw(ok)
}

func do(h http.Handler, method, origin string, preflight bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/x", nil)
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if preflight {
		r.Header.Set("Access-Control-Request-Method", "POST")
		r.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestCORSAllowedOrigin(t *testing.T) {
	rec := do(corsServer(t), http.MethodGet, "http://localhost:5173", false)
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Fatalf("status %d, allow-origin %q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
	// spaces and a trailing slash in the config are tolerated
	if rec := do(corsServer(t), http.MethodGet, "http://localhost:5174", false); rec.Code != http.StatusOK {
		t.Fatalf("second origin: status %d", rec.Code)
	}
}

func TestCORSPreflight(t *testing.T) {
	rec := do(corsServer(t), http.MethodOptions, "http://localhost:5173", true)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Methods") == "" || rec.Header().Get("Access-Control-Allow-Headers") == "" {
		t.Fatalf("missing preflight headers: %v", rec.Header())
	}
}

func TestCORSBlockedOrigin(t *testing.T) {
	for _, origin := range []string{"http://evil.example", "http://localhost:5173.evil.example", "null"} {
		rec := do(corsServer(t), http.MethodGet, origin, false)
		if rec.Code != http.StatusForbidden || rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("%s: status %d, allow-origin %q", origin, rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
		}
		var body ErrorBody
		if json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Error.Code != "origin_not_allowed" {
			t.Fatalf("%s: unexpected body %s", origin, rec.Body.String())
		}
	}
}

func TestCORSNoOriginPassesThrough(t *testing.T) {
	rec := do(corsServer(t), http.MethodGet, "", false)
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("status %d, allow-origin %q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSRejectsBadConfig(t *testing.T) {
	for _, bad := range []string{"*", "localhost:5173", "http://localhost:5173/app", "ftp://x.example"} {
		if _, err := CORS([]string{bad}); err == nil {
			t.Fatalf("%q should be rejected", bad)
		}
	}
}
