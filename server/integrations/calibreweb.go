package integrations

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// CalibreWebClient talks to Calibre-Web (crocodilestick/calibre-web-automated
// fork). Live-verified 2026-09-01 on v4.0.6.
//
// Calibre-Web has NO real REST API - it is OPDS-only. /api/ is 404 and there
// is no token auth for HTTP (CALIBRE_DB_PASSWORD is the sqlite db password,
// not an HTTP credential). The public read surface is the OPDS feeds, which
// this client parses: the root navigation feed, the search feed, and the
// detail feeds it links to.
//
// The base URL is the CWA container's OPDS root. On potatostack CWA listens
// on container port 8083 (CWA_PORT_OVERRIDE) published as host 8084; from
// the gluetun netns the durable address is the bridge IP :8083.
type CalibreWebClient struct {
	*base
}

// NewCalibreWeb builds a client. baseURL empty => disabled. No auth header is
// sent (OPDS is unauthenticated).
func NewCalibreWeb(baseURL string) *CalibreWebClient {
	return &CalibreWebClient{newBase("calibre-web", baseURL, "", "")}
}

func (c *CalibreWebClient) Enabled() bool { return c.baseURL != "" }

// OPDSFeed is the parsed OPDS atom feed: the entry links we care about.
type OPDSFeed struct {
	// Root navigation links (e.g. the "search" and "books" feeds) from the
	// root feed's <link> elements.
	Links []OPDSLink `json:"links"`
	// Entries from the feed (search results or a collection).
	Entries []OPDSEntry `json:"entries"`
}

// OPDSLink is an OPDS <link> (rel, href, type, title).
type OPDSLink struct {
	Rel   string `json:"rel"`
	Href  string `json:"href"`
	Type  string `json:"type"`
	Title string `json:"title,omitempty"`
}

// OPDSEntry is one OPDS entry (a book or collection).
type OPDSEntry struct {
	Title string `json:"title"`
	ID    string `json:"id,omitempty"`
	// Media: the epub/pdf/... content link (rel=alternate, type a book MIME).
	MediaHref string `json:"mediaHref,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
	Author    string `json:"author,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

// rawOPDS is the minimal atom/OPDS structure to unmarshal.
type rawOPDS struct {
	Links []struct {
		Rel   string `xml:"rel,attr"`
		Href  string `xml:"href,attr"`
		Type  string `xml:"type,attr"`
		Title string `xml:"title,attr"`
	} `xml:"link"`
	Entries []struct {
		ID      string `xml:"id"`
		Title   string `xml:"title"`
		Author  string `xml:"author>name"`
		Summary string `xml:"summary"`
		Links   []struct {
			Rel  string `xml:"rel,attr"`
			Href string `xml:"href,attr"`
			Type string `xml:"type,attr"`
		} `xml:"link"`
	} `xml:"entry"`
}

func (c *CalibreWebClient) fetchFeed(ctx context.Context, path string, q url.Values) (*OPDSFeed, error) {
	u, err := c.join(path, q)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calibre-web: %s: %w", path, err)
	}
	defer resp.Body.Close()
	if !statusOK(resp.StatusCode) {
		return nil, fmt.Errorf("calibre-web %s: HTTP %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBytes))
	if err != nil {
		return nil, fmt.Errorf("calibre-web: read: %w", err)
	}
	var feed rawOPDS
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("calibre-web: decode OPDS: %w", err)
	}
	out := &OPDSFeed{}
	for _, l := range feed.Links {
		out.Links = append(out.Links, OPDSLink{Rel: l.Rel, Href: l.Href, Type: l.Type, Title: l.Title})
	}
	for _, e := range feed.Entries {
		oe := OPDSEntry{ID: e.ID, Title: e.Title, Author: e.Author, Summary: e.Summary}
		for _, l := range e.Links {
			if l.Rel == "alternate" && strings.HasPrefix(l.Type, "application/epub") ||
				strings.HasPrefix(l.Type, "application/pdf") ||
				strings.HasPrefix(l.Type, "application/x-mobipocket") {
				oe.MediaHref = l.Href
				oe.MediaType = l.Type
				break
			}
		}
		out.Entries = append(out.Entries, oe)
	}
	return out, nil
}

// RootFeed returns the OPDS root navigation feed (the links to search +
// collection feeds).
func (c *CalibreWebClient) RootFeed(ctx context.Context) (*OPDSFeed, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	return c.fetchFeed(ctx, "/opds/", nil)
}

// SearchOPDS runs an OPDS search for term and returns the matching entries.
// The search feed is typically /opds/search or /opds/?searchTerm=; this
// probes the query-param form first and falls back to the path form.
func (c *CalibreWebClient) SearchOPDS(ctx context.Context, term string) (*OPDSFeed, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	q := url.Values{}
	q.Set("searchTerm", term)
	if feed, err := c.fetchFeed(ctx, "/opds/", q); err == nil && len(feed.Entries) > 0 {
		return feed, nil
	}
	return c.fetchFeed(ctx, "/opds/search?q="+url.QueryEscape(term), nil)
}
