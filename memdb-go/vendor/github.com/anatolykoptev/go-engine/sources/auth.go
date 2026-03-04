package sources

import "net/http"

// AuthMethod applies authentication to an outgoing HTTP request.
//
// Implementations must be safe for concurrent use — they are applied
// each time a request is built inside [APIClient].
type AuthMethod interface {
	// Apply mutates req to add authentication credentials.
	Apply(req *http.Request)
}

// bearerAuth implements AuthMethod via a Bearer token.
type bearerAuth struct{ token string }

// BearerAuth returns an [AuthMethod] that sets the Authorization header
// to "Bearer <token>" on every request.
func BearerAuth(token string) AuthMethod {
	return &bearerAuth{token: token}
}

// Apply sets the Authorization header to "Bearer <token>".
func (b *bearerAuth) Apply(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+b.token)
}

// noAuth is a no-op AuthMethod.
type noAuth struct{}

// NoAuthMethod returns an [AuthMethod] that performs no authentication.
// Use this as a safe default when a source does not require credentials.
func NoAuthMethod() AuthMethod {
	return &noAuth{}
}

// Apply is a no-op; the request is left unmodified.
func (*noAuth) Apply(_ *http.Request) {}
