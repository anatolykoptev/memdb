package stealth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
)

// ErrSSRFBlocked wraps every error the built-in default-deny guards return
// when a fetch target resolves to loopback, private (RFC1918 / RFC4193 ULA),
// link-local (including the cloud-metadata address 169.254.169.254), or the
// unspecified address — the address classes an SSRF payload targets to reach
// internal infrastructure from a caller-supplied URL (an advertiser-provided
// website, a redirect Location header, a scraped image src).
//
// This is a deliberately dependency-free, stdlib-only floor: a BrowserClient
// is fail-closed BY CONSTRUCTION (a forgotten guard config denies internal
// targets), and the public option signatures stay free of any framework or
// bogdanfinn/fhttp type. A consumer that wants the fleet's fuller SSRF policy
// (CGNAT, NAT64/6to4, alt-encoded-IP rejection) wires go-kit/httputil's
// SSRFGuards()/CheckURL through WithDialControl / WithRedirectGuard /
// WithRequestURLGuard, which OVERRIDE these defaults.
//
// A caller-supplied guard's error need NOT wrap this sentinel itself: the With*
// setters tag it (see tagGuardErr2), so errors.Is(err, ErrSSRFBlocked) holds
// for any configured guard's rejection, built-in or framework-wired. That is
// what makes doWithRetry treat the rejection as non-retryable — a blocked
// target is about the URL/address, not the proxy, so every retry re-blocks
// identically.
var ErrSSRFBlocked = errors.New("stealth: SSRF-blocked address")

// maxSSRFRedirectHops caps how many redirect hops the default redirect guard
// follows before refusing. Installing any custom CheckRedirect REPLACES
// net/http's (and tls-client's) own built-in 10-hop cap, so the guard closure
// must re-own it — otherwise a self-redirecting target hangs the caller until
// its request timeout instead of failing fast.
const maxSSRFRedirectHops = 10

// isBlockedIP reports whether ip must never be dialed as a fetch target. A
// nil IP is treated as blocked (fail closed). Go's net.IP predicates already
// unwrap IPv4-mapped-IPv6 (::ffff:a.b.c.d) before matching. This is the
// stdlib floor; it intentionally covers less than go-kit's IsBlockedIP (no
// CGNAT / NAT64 / 6to4) — a consumer wiring go-kit's policy gets those.
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() {
		return true
	}
	// Covers 169.254.169.254 (link-local unicast) plus the rest of
	// 169.254.0.0/16 and IPv6 fe80::/10.
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	return false
}

// defaultDenyDial is the built-in connect-time (tier-1) guard. It inspects
// the ALREADY-RESOLVED network/address pair net.Dialer.Control receives —
// after DNS lookup, immediately before connect(2) — which is what defeats
// DNS-rebinding: the check reads the literal address about to be dialed, not
// the hostname. Wired onto both backends' dialers unless overridden by
// WithDialControl or cleared by WithoutSSRFGuard.
func defaultDenyDial(network, address string) error {
	switch network {
	case "unix", "unixgram", "unixpacket":
		// A Unix-domain-socket dial names a local filesystem path, not a
		// resolvable host:port — not an SSRF-to-internal-IP vector.
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address // no port present — unexpected for a tcp/udp dial target
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// A resolved dial target that isn't a literal IP is unexpected; fail
		// closed rather than let an unparseable address through.
		return fmt.Errorf("%w: cannot parse dial address %q (%s)", ErrSSRFBlocked, address, network)
	}
	if isBlockedIP(ip) {
		return fmt.Errorf("%w: %s (%s)", ErrSSRFBlocked, ip, network)
	}
	return nil
}

// defaultDenyURL is the built-in pre-resolve (tier-3, and the per-hop body of
// tier-2) guard. It enforces the http/https scheme allowlist and refuses any
// URL whose host is — or resolves to — a blocked address. It is necessarily
// weaker against DNS-rebind than defaultDenyDial (DNS can change between this
// resolution and the backend's own), but it is the ONLY tier that guards a
// PROXIED fetch, whose dial control sees only the proxy's address.
func defaultDenyURL(ctx context.Context, u *url.URL) error {
	if u == nil {
		return fmt.Errorf("%w: nil URL", ErrSSRFBlocked)
	}
	if s := strings.ToLower(u.Scheme); s != "http" && s != "https" {
		return fmt.Errorf("%w: scheme %q not allowed (http/https only)", ErrSSRFBlocked, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrSSRFBlocked)
	}
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("%w: %s", ErrSSRFBlocked, ip)
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("stealth: resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("%w: %q resolved to no addresses", ErrSSRFBlocked, host)
	}
	for _, a := range addrs {
		if isBlockedIP(a.IP) {
			return fmt.Errorf("%w: %s resolves to %s", ErrSSRFBlocked, host, a.IP)
		}
	}
	return nil
}

// defaultDenyRedirect is the built-in per-hop (tier-2) guard. It re-owns the
// hop cap net/http drops once CheckRedirect is overridden, then applies
// defaultDenyURL to each hop's target. Signature matches
// http.Client.CheckRedirect exactly.
func defaultDenyRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxSSRFRedirectHops {
		return fmt.Errorf("stealth: stopped after %d redirects", maxSSRFRedirectHops)
	}
	return defaultDenyURL(req.Context(), req.URL)
}

// adaptControl bridges a stdlib-typed dial guard func(network, address) error
// (what WithDialControl accepts and what go-kit's SSRFGuards() dial closure
// is) into net.Dialer.Control's func(network, address string, c
// syscall.RawConn) error — the guard ignores the RawConn.
func adaptControl(fn func(network, address string) error) func(network, address string, c syscall.RawConn) error {
	if fn == nil {
		return nil
	}
	return func(network, address string, _ syscall.RawConn) error {
		return fn(network, address)
	}
}
