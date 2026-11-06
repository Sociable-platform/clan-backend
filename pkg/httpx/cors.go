package httpx

import (
	"fmt"
	"net/http"
	"net/url"
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
func CORS(allowedOrigins []string) (Middleware, error) {
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

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			h := w.Header()
			h.Add("Vary", "Origin")
			if !allowed[origin] {
				WriteError(w, http.StatusForbidden, "origin_not_allowed", "This website is not allowed to call the API")
				return
			}
			h.Set("Access-Control-Allow-Origin", origin)

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
