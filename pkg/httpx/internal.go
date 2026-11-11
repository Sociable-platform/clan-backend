package httpx

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
)

// InternalTokenHeader is the header that carries the shared secret of the /internal endpoints.
const InternalTokenHeader = "X-Internal-Token"

// RequireInternal returns middleware that lets a request through only when it sends the shared
// secret in X-Internal-Token. Other services use it; players and developers never do.
//
// With an empty token the internal endpoints are switched off: every request gets the same JSON
// 404 as an unknown route, so nobody can tell that they exist.
func RequireInternal(token string) Middleware {
	if token == "" {
		return func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				WriteError(w, http.StatusNotFound, "not_found", "Route not found")
			})
		}
	}
	// Comparing fixed-length hashes keeps the comparison time independent of the secret's length.
	want := sha256.Sum256([]byte(token))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := sha256.Sum256([]byte(r.Header.Get(InternalTokenHeader)))
			if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
				WriteError(w, http.StatusUnauthorized, "invalid_internal_token",
					"Send the internal token in the X-Internal-Token header")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
