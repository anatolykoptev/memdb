package stealth

import (
	"fmt"
	"strings"

	"github.com/anatolykoptev/go-stealth/internal/uri"
)

// ChallengeType identifies the kind of Cloudflare challenge.
type ChallengeType string

const (
	ChallengeJS           ChallengeType = "js_challenge"
	ChallengeTurnstile    ChallengeType = "managed_challenge"
	ChallengeBlock        ChallengeType = "block"
	ChallengeManagedAt200 ChallengeType = "managed_challenge_200"
)

// CloudflareError indicates a Cloudflare challenge or block was detected.
type CloudflareError struct {
	Type       ChallengeType
	StatusCode int
	RayID      string
}

func (e *CloudflareError) Error() string {
	return fmt.Sprintf("cloudflare %s (HTTP %d, ray %s)", e.Type, e.StatusCode, e.RayID)
}

// DetectCloudflare inspects a Response for Cloudflare challenge markers.
// Returns nil if the response is not a Cloudflare challenge.
//
// Signal precedence (under the server~cloudflare gate):
//  1. Block markers (any status) — a block page that also mentions
//     challenge-platform or carries cf-mitigated must stay a block, not be
//     promoted to a solvable challenge.
//  2. cf-mitigated: challenge header (any status) — the documented,
//     status-independent challenge-page signal. Top precedence among challenge
//     signals.
//  3. Body markers (any status): turnstile, then challenge-platform/_cf_chl_opt.
//
// Status is a kind hint, not a gate: 200 → ChallengeManagedAt200, otherwise
// ChallengeJS. The fix is additive (recognise more challenges), never promotive
// (a 403 WAF block carrying neither cf-mitigated nor block/body markers returns
// nil — promoting unknown 403s would burn solver time on hard blocks).
//
// Cloudflare moved the JS/managed-challenge status from 503 to 403 on
// 2023-03-01; cf-mitigated: challenge is set on all challenge-page types
// regardless of status and is the only valid value for that header.
//   - https://developers.cloudflare.com/cloudflare-challenges/challenge-types/challenge-pages/detect-response/
//   - https://community.cloudflare.com/t/community-update-status-code-for-javascript-challenge-changing-from-503-to-403/445724
//   - https://developers.cloudflare.com/rules/custom-errors/reference/error-page-types/
func DetectCloudflare(resp *Response) *CloudflareError {
	server := strings.ToLower(resp.Headers["server"])
	if !strings.Contains(server, "cloudflare") {
		return nil
	}

	body := strings.ToLower(string(resp.Body))
	rayID := resp.Headers["cf-ray"]
	cfMitigated := strings.ToLower(resp.Headers["cf-mitigated"])
	isChallengeHeader := strings.Contains(cfMitigated, "challenge")

	// 1. Block markers take top precedence so a block page that also mentions
	//    challenge-platform or carries cf-mitigated stays a block.
	if strings.Contains(body, "you have been blocked") ||
		strings.Contains(body, "cf-error-details") ||
		strings.Contains(body, `cf-error-code">1020`) {
		return &CloudflareError{Type: ChallengeBlock, StatusCode: resp.StatusCode, RayID: rayID}
	}

	// 2. cf-mitigated: challenge — status-independent, documented challenge signal.
	if isChallengeHeader {
		return &CloudflareError{Type: challengeKind(resp.StatusCode), StatusCode: resp.StatusCode, RayID: rayID}
	}

	// 3. Body markers — status-independent. Turnstile before generic challenge.
	if strings.Contains(body, "turnstile-wrapper") || strings.Contains(body, "cf-turnstile") {
		return &CloudflareError{Type: ChallengeTurnstile, StatusCode: resp.StatusCode, RayID: rayID}
	}

	if strings.Contains(body, "challenge-platform") || strings.Contains(body, "_cf_chl_opt") {
		return &CloudflareError{Type: challengeKind(resp.StatusCode), StatusCode: resp.StatusCode, RayID: rayID}
	}

	return nil
}

// challengeKind maps the response status to the challenge kind: 200 is
// ChallengeManagedAt200 (preserving the legacy 200-branch semantics), any other
// status is ChallengeJS. Status is a kind hint, not a detection gate.
func challengeKind(statusCode int) ChallengeType {
	if statusCode == 200 {
		return ChallengeManagedAt200
	}
	return ChallengeJS
}

// CloudflareDetectMiddleware inspects responses for Cloudflare challenges.
// If a challenge is detected, it returns a *CloudflareError (use errors.As to extract).
// Non-challenge responses pass through unchanged.
func CloudflareDetectMiddleware(next Handler) Handler {
	return func(req *Request) (*Response, error) {
		resp, err := next(req)
		if err != nil {
			return resp, err
		}
		if cfErr := DetectCloudflare(resp); cfErr != nil {
			return resp, cfErr
		}
		return resp, nil
	}
}

// CookieProvider obtains Cloudflare clearance cookies from an external source.
type CookieProvider interface {
	// GetCookie returns a cached cf_clearance cookie for the domain.
	// Returns empty string if no cached cookie is available.
	GetCookie(domain string) string

	// Solve attempts to solve a Cloudflare challenge and returns the cookie string.
	// The cookie string should be in "cf_clearance=value" format.
	Solve(domain string, challenge *CloudflareError) (string, error)
}

// CloudflareCookieMiddleware returns a middleware that:
//  1. Injects cached cf_clearance cookies from the provider before each request.
//  2. On Cloudflare challenge response, calls provider.Solve() to get a cookie and retries once.
func CloudflareCookieMiddleware(provider CookieProvider) Middleware {
	return func(next Handler) Handler {
		return func(req *Request) (*Response, error) {
			domain := uri.ExtractHost(req.URL)

			if cookie := provider.GetCookie(domain); cookie != "" {
				if req.Headers == nil {
					req.Headers = make(map[string]string)
				}
				req.Headers["cookie"] = appendCookie(req.Headers["cookie"], cookie)
			}

			resp, err := next(req)
			if err != nil {
				return resp, err
			}

			cfErr := DetectCloudflare(resp)
			if cfErr == nil {
				return resp, nil
			}

			cookie, solveErr := provider.Solve(domain, cfErr)
			if solveErr != nil {
				return resp, fmt.Errorf("%w: solve failed: %w", cfErr, solveErr)
			}
			if cookie == "" {
				return resp, cfErr
			}

			if req.Headers == nil {
				req.Headers = make(map[string]string)
			}
			req.Headers["cookie"] = cookie
			return next(req)
		}
	}
}

// appendCookie appends a cookie to an existing cookie header value.
func appendCookie(existing, newCookie string) string {
	if existing == "" {
		return newCookie
	}
	return existing + "; " + newCookie
}
