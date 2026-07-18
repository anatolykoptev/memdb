package stealth

import (
	"context"
	"log/slog"
)

// SmartFetchMiddleware returns a middleware that detects Cloudflare challenges
// and delegates to ox-browser /fetch-smart for automatic solving.
// On CF detection, the original response is replaced with ox-browser's solved response.
// Non-CF responses pass through unchanged.
func SmartFetchMiddleware(oxClient *OxBrowserClient) Middleware {
	return func(next Handler) Handler {
		return func(req *Request) (*Response, error) {
			resp, err := next(req)
			if err != nil {
				return resp, err
			}

			cfErr := DetectCloudflare(resp)
			if cfErr == nil {
				return resp, nil
			}

			if cfErr.Type == ChallengeBlock {
				return resp, cfErr
			}

			slog.Debug("smartfetch: CF detected, delegating to ox-browser",
				slog.String("url", req.URL),
				slog.String("type", string(cfErr.Type)),
			)

			oxResp, oxErr := oxClient.FetchSmart(context.Background(), req.URL)
			if oxErr != nil {
				slog.Warn("smartfetch: ox-browser failed, returning original",
					slog.Any("error", oxErr))
				return resp, cfErr
			}

			if oxResp.Error != "" {
				return resp, cfErr
			}

			return &Response{
				Body:       []byte(oxResp.Body),
				StatusCode: oxResp.Status,
				Headers:    resp.Headers,
			}, nil
		}
	}
}
