package server

// Regression pins for the download-callback SSRF guard. The failure this
// prevents is silent: a callbackUrl pointing at another service on the bridge
// is accepted, the POST succeeds, and nothing in the log looks wrong.

import (
	"net"
	"net/url"
	"strings"
	"testing"
)

func TestValidateCallbackURL(t *testing.T) {
	cases := []struct {
		name      string
		allowlist string
		raw       string
		wantErr   string
	}{
		{name: "public host", raw: "https://example.org/hook"},
		{name: "public literal ip", raw: "http://93.184.216.34/hook"},

		{name: "loopback denied", raw: "http://127.0.0.1:8080/hook",
			wantErr: "loopback or private"},
		{name: "loopback v6 denied", raw: "http://[::1]:8080/hook",
			wantErr: "loopback or private"},
		{name: "rfc1918 denied", raw: "http://192.168.178.200:8123/api",
			wantErr: "loopback or private"},
		{name: "bridge denied", raw: "http://172.22.0.7:9696/api",
			wantErr: "loopback or private"},
		// The one that matters most: cloud/router metadata is refused even if
		// somebody allowlists it, because it is never a webhook sink.
		{name: "metadata denied", raw: "http://169.254.169.254/latest/meta-data",
			allowlist: "169.254.169.254", wantErr: "denied range"},
		{name: "unspecified denied", raw: "http://0.0.0.0:80/", wantErr: "denied range"},

		{name: "allowlisted loopback ok", allowlist: "127.0.0.1",
			raw: "http://127.0.0.1:8080/hook"},
		{name: "allowlisted service name ok", allowlist: "dagu,readarr",
			raw: "http://dagu:8080/webhook"},
		{name: "allowlist is authoritative", allowlist: "dagu",
			raw: "https://example.org/hook", wantErr: "is not in"},

		{name: "userinfo denied", raw: "https://user:pass@example.org/hook",
			wantErr: "userinfo"},
		{name: "no scheme", raw: "example.org/hook", wantErr: "absolute http(s)"},
		{name: "wrong scheme", raw: "file:///etc/passwd", wantErr: "absolute http(s)"},
		{name: "gopher scheme", raw: "gopher://127.0.0.1:6379/_INFO",
			wantErr: "absolute http(s)"},
		{name: "empty", raw: "", wantErr: "absolute http(s)"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.allowlist != "" {
				t.Setenv(callbackAllowlistEnv, tc.allowlist)
			} else {
				t.Setenv(callbackAllowlistEnv, "")
			}
			_, err := validateCallbackURL(tc.raw)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateCallbackURL(%q) = %v, want accepted", tc.raw, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateCallbackURL(%q) accepted, want error %q", tc.raw, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateCallbackURL(%q) = %v, want error containing %q",
					tc.raw, err, tc.wantErr)
			}
		})
	}
}

// checkCallbackAddr is what the dialer calls, so a hostname that resolves to a
// blocked address at connect time is still refused - that is the DNS-rebinding
// path a URL-only check cannot see.
func TestCheckCallbackAddrIsTheDialTimeGate(t *testing.T) {
	if err := checkCallbackAddr(net.ParseIP("127.0.0.1"), false); err == nil {
		t.Fatal("loopback accepted at dial time")
	}
	if err := checkCallbackAddr(net.ParseIP("127.0.0.1"), true); err != nil {
		t.Fatalf("allowlisted loopback refused at dial time: %v", err)
	}
	if err := checkCallbackAddr(net.ParseIP("169.254.169.254"), true); err == nil {
		t.Fatal("metadata address accepted at dial time even though allowlisted")
	}
	if err := checkCallbackAddr(nil, true); err == nil {
		t.Fatal("unparsable address accepted")
	}
}

func TestCallbackClientRefusesRedirects(t *testing.T) {
	u, _ := url.Parse("https://example.org/hook")
	c := newCallbackClient(u)
	if c.CheckRedirect == nil {
		t.Fatal("callback client follows redirects; a 302 could reach a denied address")
	}
}

func TestRedactCallbackURL(t *testing.T) {
	got := redactCallbackURL("https://user:pw@hook.example.org/path?token=secret#frag")
	if strings.Contains(got, "secret") || strings.Contains(got, "pw") {
		t.Fatalf("redactCallbackURL leaked credentials or query: %q", got)
	}
	if got != "https://hook.example.org/path" {
		t.Fatalf("redactCallbackURL = %q", got)
	}
}
