package server

// SSRF guard for the download-completion webhook.
//
// POST /api/v1/download takes a caller-supplied callbackUrl and the server
// POSTs to it once the book lands. That is a server-side request driven by
// request data, i.e. textbook SSRF: without a guard, anything holding an API
// token can make openbooks POST JSON at any address openbooks can reach -
// every other service on potatostack_potatostack, the host's loopback, and
// cloud/router metadata endpoints. "The API is internal" is not a defense;
// internal services are the usual SSRF target, and openbooks sits on the
// shared bridge with ~100 other containers.
//
// The policy has to allow private targets, because that is the legitimate use
// case here - the documented consumers are Readarr and the DAGs, which live on
// the same bridge (docs/openbooks/integration-api-spec-2026-09-01.md). So a
// blanket "reject RFC1918" would break the feature it protects. Instead:
//
//	link-local / metadata / unspecified / multicast  ->  ALWAYS denied
//	loopback + private ranges                        ->  only if the host is in
//	                                                     OPENBOOKS_CALLBACK_ALLOWED_HOSTS
//	public addresses                                 ->  allowed, unless an
//	                                                     allowlist is set, which
//	                                                     is then authoritative
//
// Enforcement happens at DIAL time (net.Dialer.Control), not only on the
// parsed URL, because a name that resolves to a public address at validation
// time can resolve to 127.0.0.1 on the next lookup - DNS rebinding defeats any
// check that only inspects the URL. Redirects are not followed, so a 302
// cannot walk a permitted host into a denied address.

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

const callbackAllowlistEnv = "OPENBOOKS_CALLBACK_ALLOWED_HOSTS"

// callbackAllowedHosts returns the operator allowlist, lowercased, hostnames
// without port. An empty result means "no allowlist configured".
func callbackAllowedHosts() map[string]bool {
	raw := strings.TrimSpace(os.Getenv(callbackAllowlistEnv))
	if raw == "" {
		return nil
	}
	hosts := map[string]bool{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		// Accept "host" and "host:port" in the allowlist; the port is not part
		// of the identity being allowed.
		if h, _, err := net.SplitHostPort(entry); err == nil {
			entry = h
		}
		hosts[strings.Trim(entry, "[]")] = true
	}
	if len(hosts) == 0 {
		return nil
	}
	return hosts
}

// hostAllowlisted reports whether u's hostname is explicitly permitted.
func hostAllowlisted(u *url.URL) bool {
	allow := callbackAllowedHosts()
	if len(allow) == 0 {
		return false
	}
	return allow[strings.ToLower(u.Hostname())]
}

// ipAlwaysDenied covers the ranges that have no legitimate webhook use and are
// the high-value SSRF targets: cloud/router metadata (169.254.0.0/16, and the
// IPv6 link-local block that carries the same), the unspecified address, and
// multicast.
func ipAlwaysDenied(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return true
	}
	return false
}

// ipNeedsAllowlist is true for addresses that are only reachable from inside
// this network - loopback and the RFC1918/RFC4193 ranges - plus IPv4-mapped
// forms of them. Those are permitted only for an allowlisted hostname.
func ipNeedsAllowlist(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsPrivate()
}

// checkCallbackAddr is the single decision point, applied to a RESOLVED
// address. allowlisted is whether the URL's hostname was explicitly permitted.
func checkCallbackAddr(ip net.IP, allowlisted bool) error {
	if ipAlwaysDenied(ip) {
		return fmt.Errorf("callback address %s is in a denied range "+
			"(link-local, metadata, unspecified or multicast)", ip)
	}
	if ipNeedsAllowlist(ip) && !allowlisted {
		return fmt.Errorf("callback address %s is loopback or private; add its "+
			"hostname to %s to allow it", ip, callbackAllowlistEnv)
	}
	return nil
}

// validateCallbackURL parses and vets a caller-supplied callbackUrl. It is the
// fast, friendly rejection at request time; newCallbackClient enforces the same
// rule again at dial time, which is the check that actually holds.
func validateCallbackURL(raw string) (*url.URL, error) {
	u, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("callbackUrl must be an absolute http(s) URL")
	}
	// Credentials in the URL would end up in logs and in the outbound request.
	if u.User != nil {
		return nil, fmt.Errorf("callbackUrl must not contain userinfo credentials")
	}
	allow := callbackAllowedHosts()
	allowlisted := len(allow) > 0 && allow[strings.ToLower(u.Hostname())]
	if len(allow) > 0 && !allowlisted {
		return nil, fmt.Errorf("callbackUrl host %q is not in %s",
			u.Hostname(), callbackAllowlistEnv)
	}
	// A literal IP can be decided here outright. A hostname cannot, because the
	// answer can change between now and the POST - that is what the dial-time
	// guard is for.
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		if err := checkCallbackAddr(ip, allowlisted); err != nil {
			return nil, err
		}
	}
	return u, nil
}

// newCallbackClient builds the HTTP client the webhook is sent with:
//
//   - Control runs after DNS resolution and before connect, so it sees the IP
//     that will actually be dialed. This is the rebinding-proof enforcement.
//   - CheckRedirect refuses to follow anything, so a permitted host cannot
//     redirect the POST into a denied address.
func newCallbackClient(u *url.URL) *http.Client {
	allowlisted := hostAllowlisted(u)
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("callback dial: unparsable address %q", address)
			}
			return checkCallbackAddr(net.ParseIP(host), allowlisted)
		},
	}
	return &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{DialContext: dialer.DialContext},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// redactCallbackURL is what goes in the log: scheme, host and path only. A
// callback URL is caller-supplied and routinely carries a token in its query
// string, so the raw value must never be logged.
func redactCallbackURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparsable callback url)"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
