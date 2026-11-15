package middleware

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// EvalHarnessHeader is sent by every benchmark, evaluation and smoke harness
// in this repository. Only an instance running with MEMDB_ENV=eval accepts it.
const EvalHarnessHeader = "X-MemDB-Eval-Harness"

// EvalHarnessGuard rejects requests that carry EvalHarnessHeader unless the
// instance is an evaluation instance (isEval). It exists so a harness pointed
// at the wrong instance cannot write benchmark data into a real store, even
// if the harness's own /health check is missing or bypassed. The marker is
// structural, so no user-name matching is needed.
func EvalHarnessGuard(logger *slog.Logger, isEval bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if isEval {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(EvalHarnessHeader) != "" {
				logger.WarnContext(r.Context(), "eval harness request rejected on non-eval instance",
					slog.String("path", r.URL.Path),
					slog.String("method", r.Method),
					slog.String("remote", r.RemoteAddr),
				)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code":    403,
					"message": "evaluation harness traffic is not accepted: this instance is not MEMDB_ENV=eval",
					"data":    nil,
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
