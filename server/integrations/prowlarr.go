package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// ProwlarrClient talks to Prowlarr's v1 API. Live-verified 2026-09-01 on
// lscr.io/linuxserver/prowlarr:2.5.2.5491 (X-Api-Key header, v1 paths).
//
// Prowlarr is NOT a Torznab index: /api/torznab/* is 404. It is the INDEXER
// HUB - it aggregates the stack's trackers/indexers and exposes them through
// GET /api/v1/search. That is the endpoint a client uses to search all
// indexers at once, and it is the counterpart of openbooks' own /torznab
// endpoint (openbooks as a book indexer; Prowlarr as the aggregator that can
// also search openbooks).
type ProwlarrClient struct {
	*base
}

// NewProwlarr builds a ProwlarrClient. baseURL+apiKey empty => disabled.
func NewProwlarr(baseURL, apiKey string) *ProwlarrClient {
	return &ProwlarrClient{newBase("prowlarr", baseURL, apiKey, "X-Api-Key")}
}

// Enabled reports whether the client is configured.
func (c *ProwlarrClient) Enabled() bool { return c.baseURL != "" }

// BookSearchResult is the slice of a Prowlarr release we keep. Prowlarr's
// /api/v1/search returns every release across every indexer; a popular query
// can return multi-MB of releases. We keep the fields an arr app (or a
// caller) needs to act on a release and drop the rest.
// BookSearchResult mirrors ONE record of live Prowlarr GET /api/v1/search.
// The field names are the measured wire shape (2026-09-02: 646 records,
// type=book) and NOT the Radarr/Sonarr spelling - Prowlarr's own search
// endpoint differs: magnetUrl (not magnetUri), a single protocol string
// (not downloadProtocols), and indexerFlags as a string array (e.g.
// ["freeleech"]), not an object. The first live run of the v5.2.0
// unified search failed the leg's decode on exactly these shapes, so this
// struct is golden-fixture tested (TestBookSearchResultWireShape).
type BookSearchResult struct {
	Indexer           string   `json:"indexer"`
	IndexerID         int      `json:"indexerId,omitempty"`
	Title             string   `json:"title"`
	SeasonEpisodeInfo string   `json:"seasonEpisodeInfo,omitempty"`
	MagnetURL         string   `json:"magnetUrl,omitempty"`
	DownloadURL       string   `json:"downloadUrl,omitempty"`
	Protocol          string   `json:"protocol,omitempty"`
	Size              int64    `json:"size"`
	IndexerFlags      []string `json:"indexerFlags,omitempty"`
	PublishDate       string   `json:"publishDate,omitempty"`
	AgeHours    float64 `json:"ageHours,omitempty"`
	AgeDays     float64 `json:"ageDays,omitempty"`
	// Age is whole days, an INT on the wire (verified against live
	// Prowlarr /api/v1/search 2026-09-02: 1095 for a 2023-09
	// release, consistent with ageHours=26280). A string here
	// fails the whole search leg's decode - the first live run of
	// the v5.2.0 unified search caught it.
	Age         int     `json:"age,omitempty"`
	GUID        string  `json:"guid,omitempty"`
}

// SearchBooks searches Prowlarr's configured indexers for books. type=book
// (or t:bookish) narrows to book-ish indexers; omit type to search all.
// limit caps the number of releases returned.
func (c *ProwlarrClient) SearchBooks(ctx context.Context, query, searchType string, limit int) ([]BookSearchResult, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	q := url.Values{}
	q.Set("query", query)
	if searchType != "" {
		q.Set("type", searchType)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	raw, status, err := c.get(ctx, "/api/v1/search", q)
	if err != nil {
		return nil, err
	}
	if !statusOK(status) {
		return nil, fmt.Errorf("prowlarr search: HTTP %d", status)
	}
	// Prowlarr returns a bare JSON array of releases.
	var results []BookSearchResult
	if err := json.Unmarshal(raw, &results); err != nil {
		return nil, fmt.Errorf("prowlarr search: decode: %w", err)
	}
	return results, nil
}

// IndexerSummary is the fields we keep from GET /api/v1/indexer.
type IndexerSummary struct {
	ID             int    `json:"id"`
	Name           string `json:"name"`
	Implementation string `json:"implementation"`
	Protocols      string `json:"protocols"`
	Private        bool   `json:"private"`
	SupportsSearch bool   `json:"supportsSearch"`
}

// ListIndexers returns the configured indexers.
func (c *ProwlarrClient) ListIndexers(ctx context.Context) ([]IndexerSummary, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	raw, status, err := c.get(ctx, "/api/v1/indexer", nil)
	if err != nil {
		return nil, err
	}
	if !statusOK(status) {
		return nil, fmt.Errorf("prowlarr indexers: HTTP %d", status)
	}
	var out []IndexerSummary
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("prowlarr indexers: decode: %w", err)
	}
	return out, nil
}
