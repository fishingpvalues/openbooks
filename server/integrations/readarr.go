package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ReadarrClient talks to Readarr's v1 API (the ebook *arr app). Readarr is
// NOT deployed on potatostack as of 2026-09-01 (no container, no compose
// entry, no config dir) - READARR_API_KEY is staged in .env but has no
// service to consume it. This client is therefore DORMANT by default: its
// base URL is empty on this stack until Readarr is deployed and a URL is
// configured. It is built now so the integration surface is complete the
// moment Readarr lands, and so a standalone operator can point it at any
// Readarr instance.
//
// The contract below is derived from the Readarr v1 OpenAPI (see
// docs/openbooks/integration-api-spec-2026-09-01.md): base /api/v1, auth via
// the X-Api-Key header, the *arr download-client contract (the arr app owns
// the completed-download import URL; the client posts to it).
type ReadarrClient struct {
	*base
}

// NewReadarr builds a client. baseURL+apiKey empty => disabled (the
// potatostack default, since Readarr is not deployed).
func NewReadarr(baseURL, apiKey string) *ReadarrClient {
	return &ReadarrClient{newBase("readarr", baseURL, apiKey, "X-Api-Key")}
}

func (c *ReadarrClient) Enabled() bool { return c.baseURL != "" }

// BookMatch is the slice of a Readarr book-lookup result we keep.
type BookMatch struct {
	ID       int    `json:"id"`
	Title    string `json:"title"`
	Author   string `json:"author,omitempty"`
	AuthorID int    `json:"authorId,omitempty"`
	Year     int    `json:"year,omitempty"`
	Images   []struct {
		Cover string `json:"cover"`
	} `json:"images,omitempty"`
}

// Lookup searches Readarr's book metadata sources (the "do I already have
// this book" / "what editions exist" query).
func (c *ReadarrClient) Lookup(ctx context.Context, query string) ([]BookMatch, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	q := url.Values{}
	q.Set("title", query)
	raw, status, err := c.get(ctx, "/api/v1/book/lookup", q)
	if err != nil {
		return nil, err
	}
	if !statusOK(status) {
		return nil, fmt.Errorf("readarr lookup: HTTP %d", status)
	}
	var out []BookMatch
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("readarr lookup: decode: %w", err)
	}
	return out, nil
}

// BookSummary is one entry of GET /api/v1/book.
type BookSummary struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	Author string `json:"author,omitempty"`
	Year   int    `json:"year,omitempty"`
	Images []struct {
		Cover string `json:"cover"`
	} `json:"images,omitempty"`
	Status  string `json:"status,omitempty"`
	Quality string `json:"quality,omitempty"`
}

// HasBook reports whether Readarr already tracks a book matching query
// (i.e. it is in the wanted/owned catalogue). It lists books and filters
// client-side, since Readarr's book list has no direct text-match param for
// a free-form title.
func (c *ReadarrClient) HasBook(ctx context.Context, query string) (bool, *BookSummary, error) {
	if !c.Enabled() {
		return false, nil, ErrDisabled
	}
	q := url.Values{}
	q.Set("term", query)
	raw, status, err := c.get(ctx, "/api/v1/book", q)
	if err != nil {
		return false, nil, err
	}
	if !statusOK(status) {
		return false, nil, fmt.Errorf("readarr book list: HTTP %d", status)
	}
	var out []BookSummary
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, nil, fmt.Errorf("readarr book list: decode: %w", err)
	}
	for i := range out {
		b := &out[i]
		if b.Title != "" && (strings.Contains(strings.ToLower(b.Title), strings.ToLower(query)) ||
			strings.Contains(strings.ToLower(query), strings.ToLower(b.Title))) {
			return true, b, nil
		}
	}
	return false, nil, nil
}
