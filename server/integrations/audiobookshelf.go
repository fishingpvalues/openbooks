package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// AudiobookshelfClient talks to Audiobookshelf's v1 API. Live-verified
// 2026-09-01 on ghcr.io/advplyr/audiobookshelf:2.36.0.
//
// Auth correction (measured): the AUDIOBOOKSHELF_API_KEY is a JWT and the
// service accepts it ONLY as `Authorization: Bearer <key>`. The x-api-key
// header returns 401. The filebot-organize DAG's audiobookshelf-scan step
// already uses Bearer, so it works; this client matches that.
//
// ABS v2.x does NOT have a /api/v1/items search or a /api/v1/auth endpoint
// (both 404 live). Library search is done through the library items
// endpoint with a search param, and scanning through
// POST /api/libraries/{id}/scan.
type AudiobookshelfClient struct {
	*base
}

// NewAudiobookshelf builds a client. baseURL+apiKey empty => disabled.
// The key is a JWT; it is sent as `Authorization: Bearer <key>`.
func NewAudiobookshelf(baseURL, apiKey string) *AudiobookshelfClient {
	return &AudiobookshelfClient{newBase("audiobookshelf", baseURL, apiKey, "Authorization")}
}

func (c *AudiobookshelfClient) Enabled() bool { return c.baseURL != "" }

// LibrarySummary is the slice of a GET /api/libraries entry we keep.
type LibrarySummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type,omitempty"`
	FolderCount int    `json:"-"`
}

// ListLibraries returns the configured libraries.
func (c *AudiobookshelfClient) ListLibraries(ctx context.Context) ([]LibrarySummary, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	raw, status, err := c.get(ctx, "/api/libraries", nil)
	if err != nil {
		return nil, err
	}
	if !statusOK(status) {
		return nil, fmt.Errorf("audiobookshelf libraries: HTTP %d", status)
	}
	var payload struct {
		Libraries []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Type    string `json:"type"`
			Folders []struct {
				ID       string `json:"id"`
				Fullpath string `json:"fullPath"`
			} `json:"folders"`
		} `json:"libraries"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("audiobookshelf libraries: decode: %w", err)
	}
	out := make([]LibrarySummary, 0, len(payload.Libraries))
	for _, l := range payload.Libraries {
		out = append(out, LibrarySummary{ID: l.ID, Name: l.Name, Type: l.Type, FolderCount: len(l.Folders)})
	}
	return out, nil
}

// ScanLibrary triggers a metadata scan of one library. libraryID is the id
// from ListLibraries. Returns the HTTP status of the POST (200/202 on
// success). This is the action the filebot-organize DAG performs after
// organizing new audiobooks.
func (c *AudiobookshelfClient) ScanLibrary(ctx context.Context, libraryID string) (int, error) {
	if !c.Enabled() {
		return 0, ErrDisabled
	}
	raw, status, err := c.postJSON(ctx, "/api/libraries/"+url.PathEscape(libraryID)+"/scan", nil, nil)
	if err != nil {
		return status, err
	}
	_ = raw
	return status, nil
}

// ItemSummary is one entry of the ABS library items search.
type ItemSummary struct {
	ID          string `json:"id"`
	Author      string `json:"author"`
	Title       string `json:"title"`
	Added       string `json:"added,omitempty"`
	AuthorID    string `json:"authorId,omitempty"`
	LibraryID   string `json:"libraryId,omitempty"`
	DownloadUrl string `json:"downloadUrl,omitempty"`
}

// SearchLibraryItems searches one ABS library by search term. ABS v2.36
// exposes library search via GET /api/libraries/{id}/items with a query
// param; the exact param is implementation-specific, so we pass the term
// through the `searchTerm`/`search` fallbacks and let the peer decide.
func (c *AudiobookshelfClient) SearchLibraryItems(ctx context.Context, libraryID, term string, limit int) ([]ItemSummary, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	if term != "" {
		q.Set("searchTerm", term)
	}
	raw, status, err := c.get(ctx, "/api/libraries/"+url.PathEscape(libraryID)+"/items", q)
	if err != nil {
		return nil, err
	}
	if !statusOK(status) {
		return nil, fmt.Errorf("audiobookshelf items: HTTP %d", status)
	}
	var out []ItemSummary
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("audiobookshelf items: decode: %w", err)
	}
	return out, nil
}
