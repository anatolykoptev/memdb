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
			// Skip auth for health endpoints
			if r.URL.Path == "/health" || r.URL.Path == "/ready" {
				next.ServeHTTP(w, r)
				return
			}

			// Skip auth for CORS preflight
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			// Check internal service secret first (for service-to-service calls)
			if cfg.ServiceSecret != "" {
				if secret := r.Header.Get("X-Service-Secret"); secret != "" {
					if subtle.ConstantTimeCompare([]byte(secret), []byte(cfg.ServiceSecret)) == 1 {
						next.ServeHTTP(w, r)
						return
					}
					logger.Warn("invalid service secret",
						slog.String("path", r.URL.Path),
						slog.String("remote", r.RemoteAddr),
					)
				}
			}

			// Check Bearer token
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				writeAuthError(w, http.StatusUnauthorized, "missing Authorization header")
				return
			}

			token, ok := parseBearerToken(authHeader)
			if !ok {
				writeAuthError(w, http.StatusUnauthorized, "invalid Authorization header format, expected: Bearer <token>")
				return
			}

			if !validateToken(token, cfg.MasterKeyHash) {
				logger.Warn("auth failed: invalid token",
					slog.String("path", r.URL.Path),
					slog.String("remote", r.RemoteAddr),
				)
				writeAuthError(w, http.StatusForbidden, "invalid API key")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
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
