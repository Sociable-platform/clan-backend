package httpx

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// CORS lets browser pages on the allowed origins call the service.
//
//   - A request without an Origin header (another server, curl, the game's own backend) passes through.
//   - A request from an allowed origin gets the CORS headers; a preflight (OPTIONS) is answered with 204.
//   - A request from any other origin is refused with 403 origin_not_allowed.
//
// Origins are written as scheme://host[:port], for example http://localhost:5173.
// "*" is not supported on purpose: every origin must be listed.
//
// publicPathPrefixes are the one exception: URLs under them are meant to be read by a
// page on any website (for example the published widget config, which a game's page loads).
// For those, GET and HEAD requests, and the preflight for them, are open to every origin and
// answered with "Access-Control-Allow-Origin: *". That is safe because such routes take no
// cookies or tokens, and there is no "Vary: Origin", so a cache can keep one copy for everyone.
// Every other method, and every other path, follows the rules above.
// A prefix starts and ends with "/", for example "/api/v1/public/".
func CORS(allowedOrigins []string, publicPathPrefixes ...string) (Middleware, error) {
	return newCORS(false, allowedOrigins, publicPathPrefixes)
}

// CORSWithCredentials is CORS for a service that also keeps a cookie in the browser (platform's
// refresh cookie). A request from an allowed origin is answered with
// "Access-Control-Allow-Credentials: true" as well; without it the browser neither sends the
// cookie from a page on another origin nor lets that page read the answer of a request that
// carried it.
//
// The public paths are not affected: they stay open to every origin with "*", and a browser never
// sends cookies to an answer like that.
func CORSWithCredentials(allowedOrigins []string, publicPathPrefixes ...string) (Middleware, error) {
	return newCORS(true, allowedOrigins, publicPathPrefixes)
}

func newCORS(credentials bool, allowedOrigins, publicPathPrefixes []string) (Middleware, error) {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		o = strings.TrimRight(strings.TrimSpace(o), "/")
		if o == "" {
			continue
		}
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" || u.RawQuery != "" {
			return nil, fmt.Errorf("cors: %q is not a valid origin, use scheme://host[:port] like http://localhost:5173", o)
		}
		allowed[o] = true
	}

	for _, p := range publicPathPrefixes {
		if len(p) < 2 || !strings.HasPrefix(p, "/") || !strings.HasSuffix(p, "/") {
			return nil, fmt.Errorf("cors: public path prefix %q must start and end with /, like /api/v1/public/", p)
		}
	}
	isPublicPath := func(urlPath string) bool {
		// A path with "..", "." or "//" in it is not a clean path; the router would redirect it
		// somewhere else, so it never counts as public.
		if path.Clean(urlPath) != urlPath {
			return false
		}
		for _, p := range publicPathPrefixes {
			if strings.HasPrefix(urlPath, p) {
				return true
			}
		}
		return false
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			h := w.Header()

			if isPublicPath(r.URL.Path) {
				readOnly := r.Method == http.MethodGet || r.Method == http.MethodHead
				asked := r.Header.Get("Access-Control-Request-Method")
				preflight := r.Method == http.MethodOptions && (asked == http.MethodGet || asked == http.MethodHead)
				if readOnly || preflight {
					h.Set("Access-Control-Allow-Origin", "*")
					h.Set("Access-Control-Expose-Headers", "ETag")
					if preflight {
						h.Set("Access-Control-Allow-Methods", "GET, HEAD")
						h.Set("Access-Control-Allow-Headers", "If-None-Match")
						h.Set("Access-Control-Max-Age", "600")
						w.WriteHeader(http.StatusNoContent)
						return
					}
					next.ServeHTTP(w, r)
					return
				}
			}

			h.Add("Vary", "Origin")
			if !allowed[origin] {
				WriteError(w, http.StatusForbidden, "origin_not_allowed", "This website is not allowed to call the API")
				return
			}
			h.Set("Access-Control-Allow-Origin", origin)
			if credentials {
				h.Set("Access-Control-Allow-Credentials", "true")
			}

			// Preflight: the browser asks which methods and headers it may use.
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Add("Vary", "Access-Control-Request-Method")
				h.Add("Vary", "Access-Control-Request-Headers")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}
