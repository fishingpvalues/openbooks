package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// httpTimeout is the per-call budget. Long enough for a Prowlarr fan-out
// search through the VPN (measured: a book search across all indexers can
// run 30s+ and return multi-megabyte bodies); short enough that a dead peer
// cannot hang the request.
const httpTimeout = 60 * time.Second

// maxRespBytes caps how much of a peer response body we read. Prowlarr
// /api/v1/search returns every release across every indexer (multi-MB for a
// popular query); we truncate so one busy peer cannot blow the caller's
// memory. JSON callers receive a truncated-but-valid slice only if the peer
// happens to stop at a clean boundary - the callers here marshal into
// structs with the fields we need, and a hard cap protects against a
// misbehaving peer.
const maxRespBytes = 8 * 1024 * 1024

// base is the shared plumbing for a single peer client.
type base struct {
	name    string // peer name for log/err messages, e.g. "prowlarr"
	baseURL string // e.g. http://172.22.0.7:9696
	token   string // optional; empty = no auth header
	header  string // which header carries the token ("X-Api-Key" / "Authorization"); empty = none
	client  *http.Client
}

func newBase(name, baseURL, token, header string) *base {
	return &base{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		header:  header,
		client:  &http.Client{Timeout: httpTimeout},
	}
}

func (b *base) enabled() bool { return b.baseURL != "" }

// url joins the base URL with path (and optional query). The peer base URL
// may carry a path prefix; path is appended after it.
func (b *base) join(path string, q url.Values) (string, error) {
	if !b.enabled() {
		return "", ErrDisabled
	}
	u, err := url.Parse(b.baseURL)
	if err != nil {
		return "", fmt.Errorf("%s: bad base URL %q: %w", b.name, b.baseURL, err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	if q != nil {
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

// get issues a GET and returns the raw body (already capped) plus status.
func (b *base) get(ctx context.Context, path string, q url.Values) ([]byte, int, error) {
	u, err := b.join(path, q)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	b.authorize(req)
	return b.do(req)
}

// postJSON issues a POST with a JSON body and returns the raw response.
func (b *base) postJSON(ctx context.Context, path string, body, out interface{}) ([]byte, int, error) {
	u, err := b.join(path, nil)
	if err != nil {
		return nil, 0, err
	}
	var r io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("%s: encode body: %w", b.name, err)
		}
		r = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, r)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	b.authorize(req)
	raw, status, err := b.do(req)
	if err != nil {
		return raw, status, err
	}
	if out != nil && status >= 200 && status < 300 {
		_ = json.Unmarshal(raw, out)
	}
	return raw, status, nil
}

func (b *base) authorize(req *http.Request) {
	if b.token == "" || b.header == "" {
		return
	}
	if b.header == "Authorization" {
		req.Header.Set("Authorization", "Bearer "+b.token)
	} else {
		req.Header.Set(b.header, b.token)
	}
}

func (b *base) do(req *http.Request) ([]byte, int, error) {
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %s %s: %w", b.name, req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	// Read a bounded body. io.LimitReader caps the read but the peer must
	// still finish the stream; the deferred Close handles that.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBytes))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("%s: read body: %w", b.name, err)
	}
	return body, resp.StatusCode, nil
}

// statusOK reports whether a peer status is a 2xx success.
func statusOK(status int) bool { return status >= 200 && status < 300 }

// Ping is the reachability probe: any HTTP status proves the peer is up
// and routable (401/404 mean "service is running, that path wants auth or
// does not exist"), while a transport error means the peer is actually
// unreachable (DNS, connection refused, timeout - e.g. a moved bridge IP).
// This is what /api/v1/integrations?probe=1 reports; it must NOT be an
// authenticated API call, because a peer that answers with 401 (Calibre-Web
// /opds/ does exactly that without a session cookie) would be misread as
// down.
func (b *base) Ping(ctx context.Context) (int, error) {
	if !b.enabled() {
		return 0, ErrDisabled
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL, nil)
	if err != nil {
		return 0, fmt.Errorf("%s: bad base URL %q: %w", b.name, b.baseURL, err)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", b.name, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10)) // drain, bounded
	return resp.StatusCode, nil
}
