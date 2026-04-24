package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// AuthConfig holds authentication settings.
type AuthConfig struct {
	Enabled       bool
	MasterKeyHash string // SHA-256 hex digest of the master API key
	ServiceSecret string // Internal service-to-service secret (bypass full auth)
}

// Auth returns middleware that validates Bearer token authentication.
// When enabled, all requests except health checks require a valid token.
// Supports two auth methods:
//  1. Bearer token: Authorization: Bearer <api-key> — validated against MasterKeyHash (SHA-256)
//  2. Service secret: X-Service-Secret: <secret> — for internal service-to-service calls
func Auth(logger *slog.Logger, cfg AuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !cfg.Enabled {
			return next // auth disabled, pass through
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isAuthExempt(r) {
				next.ServeHTTP(w, r)
				return
			}

			if ok, granted := checkServiceSecret(r, cfg.ServiceSecret); ok {
				if granted {
					next.ServeHTTP(w, r)
					return
				}
				logger.Warn("invalid service secret",
					slog.String("path", r.URL.Path),
					slog.String("remote", r.RemoteAddr),
				)
			}

			if !checkBearerToken(w, r, logger, cfg.MasterKeyHash) {
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// isAuthExempt returns true for paths that skip auth (health, internal APIs, CORS preflight).
func isAuthExempt(r *http.Request) bool {
	if r.Method == http.MethodOptions {
		return true
	}
	// /metrics is exempt: bound to 127.0.0.1 only in docker-compose,
	// reached via internal `backend` network from Prometheus. No PII in
	// OTel instruments. Keeping auth would require Prometheus to send a
	// header it doesn't support natively.
	//
	// /debug/pprof/ is exempt: pprofHandler (server_routes.go) is the sole
	// authentication gate for this subtree, enforcing X-Service-Secret.
	// If you add /debug/pprof/ here you change the security model — do not
	// remove this exemption without also updating pprofHandler.
	return r.URL.Path == "/health" || r.URL.Path == "/ready" ||
		r.URL.Path == "/metrics" ||
		strings.HasPrefix(r.URL.Path, "/v1/") ||
		strings.HasPrefix(r.URL.Path, "/debug/pprof/")
}

// checkServiceSecret checks the X-Service-Secret / X-Internal-Service headers.
// Returns (presented, valid): presented=true if a secret header was sent, valid=true if it matches.
func checkServiceSecret(r *http.Request, expected string) (presented bool, valid bool) {
	if expected == "" {
		return false, false
	}
	secret := r.Header.Get("X-Service-Secret")
	if secret == "" {
		secret = r.Header.Get("X-Internal-Service")
	}
	if secret == "" {
		return false, false
	}
	return true, subtle.ConstantTimeCompare([]byte(secret), []byte(expected)) == 1
}

// checkBearerToken validates the Authorization: Bearer header and writes an error response on failure.
// Returns true if auth succeeded.
func checkBearerToken(w http.ResponseWriter, r *http.Request, logger *slog.Logger, masterKeyHash string) bool {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		writeAuthError(w, http.StatusUnauthorized, "missing Authorization header")
		return false
	}

	token, ok := parseBearerToken(authHeader)
	if !ok {
		writeAuthError(w, http.StatusUnauthorized, "invalid Authorization header format, expected: Bearer <token>")
		return false
	}

	if !validateToken(token, masterKeyHash) {
		logger.Warn("auth failed: invalid token",
			slog.String("path", r.URL.Path),
			slog.String("remote", r.RemoteAddr),
		)
		writeAuthError(w, http.StatusForbidden, "invalid API key")
		return false
	}

	return true
}

// parseBearerToken extracts the token from "Bearer <token>" header value.
func parseBearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

// validateToken checks if the provided token matches the stored hash.
// The hash is SHA-256 hex digest of the expected key.
func validateToken(token, expectedHash string) bool {
	if expectedHash == "" {
		return false
	}
	hash := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(hash[:])
	return subtle.ConstantTimeCompare([]byte(tokenHash), []byte(expectedHash)) == 1
}

func writeAuthError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"code":%d,"message":"%s","data":null}`, code, message)
}
