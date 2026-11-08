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

// publicServer is a CORS server where everything under /api/v1/public/ is open to any website.
func publicServer(t *testing.T) http.Handler {
	t.Helper()
	mw, err := CORS([]string{"http://localhost:5173"}, "/api/v1/public/")
	if err != nil {
		t.Fatal(err)
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return mw(ok)
}

// doPath is like do, with a path of its own. askedMethod is the method a preflight asks about.
func doPath(h http.Handler, method, path, origin, askedMethod string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if askedMethod != "" {
		r.Header.Set("Access-Control-Request-Method", askedMethod)
		r.Header.Set("Access-Control-Request-Headers", "if-none-match")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

const publicPath = "/api/v1/public/games/g1/widget-config"

func TestCORSPublicPathIsOpenToAnyWebsite(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, origin := range []string{"https://mygame.example", "http://localhost:5173", "null"} {
			rec := doPath(publicServer(t), method, publicPath, origin, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("%s from %s: status %d", method, origin, rec.Code)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
				t.Fatalf("%s from %s: allow-origin %q, want *", method, origin, got)
			}
			if rec.Header().Get("Access-Control-Expose-Headers") != "ETag" {
				t.Fatalf("the page must be able to read ETag, got %q", rec.Header().Get("Access-Control-Expose-Headers"))
			}
			if rec.Header().Get("Vary") != "" {
				t.Fatalf("a cache may keep one copy for every site, got Vary %q", rec.Header().Get("Vary"))
			}
		}
	}
}

func TestCORSPublicPathPreflight(t *testing.T) {
	for _, asked := range []string{http.MethodGet, http.MethodHead} {
		rec := doPath(publicServer(t), http.MethodOptions, publicPath, "https://mygame.example", asked)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("preflight for %s: status %d", asked, rec.Code)
		}
		h := rec.Header()
		if h.Get("Access-Control-Allow-Origin") != "*" || h.Get("Access-Control-Allow-Methods") != "GET, HEAD" ||
			h.Get("Access-Control-Allow-Headers") != "If-None-Match" {
			t.Fatalf("preflight headers: %v", h)
		}
	}
}

// Only reading is open: anything else on a public path, and every other path, keeps the list.
func TestCORSPublicPathDoesNotOpenAnythingElse(t *testing.T) {
	stranger := "https://mygame.example"
	for _, tt := range []struct{ name, method, path, asked string }{
		{"POST on a public path", http.MethodPost, publicPath, ""},
		{"DELETE on a public path", http.MethodDelete, publicPath, ""},
		{"preflight for POST on a public path", http.MethodOptions, publicPath, http.MethodPost},
		{"preflight for PUT on a public path", http.MethodOptions, publicPath, http.MethodPut},
		{"another path", http.MethodGet, "/api/v1/games", ""},
		{"a path that only starts like the prefix", http.MethodGet, "/api/v1/publicly/x", ""},
		{"the prefix without its last slash", http.MethodGet, "/api/v1/public", ""},
		{"a path that climbs out of the prefix", http.MethodGet, "/api/v1/public/../games", ""},
		{"a path with a double slash", http.MethodGet, "/api/v1/public//x", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := doPath(publicServer(t), tt.method, tt.path, stranger, tt.asked)
			if rec.Code != http.StatusForbidden || rec.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatalf("status %d, allow-origin %q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
			}
		})
	}
}

func TestCORSPublicPathStillAnswersListedOriginsOnOtherPaths(t *testing.T) {
	rec := doPath(publicServer(t), http.MethodGet, "/api/v1/games", "http://localhost:5173", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Fatalf("status %d, allow-origin %q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSPublicPathWithoutOriginPassesThrough(t *testing.T) {
	rec := doPath(publicServer(t), http.MethodGet, publicPath, "", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("status %d, allow-origin %q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSRejectsBadPublicPrefix(t *testing.T) {
	for _, bad := range []string{"", "/", "api/v1/public/", "/api/v1/public", "*"} {
		if _, err := CORS([]string{"http://localhost:5173"}, bad); err == nil {
			t.Fatalf("prefix %q should be rejected", bad)
		}
	}
}
