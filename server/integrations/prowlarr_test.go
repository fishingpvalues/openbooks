package integrations

import (
	"encoding/json"
	"testing"
)

// goldenAgePayload is a trimmed capture of live Prowlarr GET /api/v1/search
// records (2026-09-02, type=book): age is an INT (whole days) and ageHours a
// float. The first live run of the v5.2.0 unified search failed decoding the
// leg because Age was typed string - "cannot unmarshal number into Go struct
// field BookSearchResult.age of type string". This test pins the wire shape
// so a future Prowlarr drift (or a re-typing) fails the build, not the
// production search leg.
// goldenAgePayload is a verbatim-shape record from live Prowlarr GET
// /api/v1/search (2026-09-02, type=book, 646 records): age INT (whole
// days), ageHours float, indexerFlags a STRING array, magnetUrl (not
// magnetUri), protocol a single string. The first live run of the v5.2.0
// unified search failed the leg's decode on exactly these - this fixture
// is the regression pin.
const goldenAgePayload = `[
  {
    "guid": "abc",
    "indexer": "1",
    "indexerId": 1,
    "title": "Mrs Davis S01E07 Great Gatsby 2001 A Space Odyssey 1080p",
    "sortTitle": "mrs davis",
    "publishDate": "2023-09-03T20:13:30Z",
    "age": 1095,
    "ageHours": 26280.000589500276,
    "ageMinutes": 1576800.03537,
    "size": 3468186091,
    "magnetUrl": "magnet:?xt=urn:btih:AAAA",
    "protocol": "torrent",
    "indexerFlags": ["freeleech"],
    "seeders": 10,
    "leechers": 2
  }
]`

func TestBookSearchResultWireShape(t *testing.T) {
	var results []BookSearchResult
	if err := json.Unmarshal([]byte(goldenAgePayload), &results); err != nil {
		t.Fatalf("unmarshal live-shaped Prowlarr payload: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	got := results[0]
	if got.Age != 1095 {
		t.Errorf("age = %d, want 1095 (int on the wire)", got.Age)
	}
	if got.AgeHours != 26280.000589500276 {
		t.Errorf("ageHours = %v, want the float value", got.AgeHours)
	}
	if got.Size != 3468186091 {
		t.Errorf("size = %d, want 3468186091", got.Size)
	}
	if got.Title == "" || got.PublishDate != "2023-09-03T20:13:30Z" {
		t.Errorf("title/publishDate = %q/%q", got.Title, got.PublishDate)
	}
	if got.MagnetURL != "magnet:?xt=urn:btih:AAAA" {
		t.Errorf("magnetUrl = %q, want the magnet link (wire field magnetUrl)", got.MagnetURL)
	}
	if got.Protocol != "torrent" {
		t.Errorf("protocol = %q, want torrent", got.Protocol)
	}
	if len(got.IndexerFlags) != 1 || got.IndexerFlags[0] != "freeleech" {
		t.Errorf("indexerFlags = %v, want [freeleech] (string array)", got.IndexerFlags)
	}
}

// TestBookSearchResultAgeAbsent: the field is omitempty-tolerant; a record
// without age (some indexers omit it) must decode with zero, not fail.
func TestBookSearchResultAgeAbsent(t *testing.T) {
	const payload = `[{"indexer":"1","title":"x","size":10}]`
	var results []BookSearchResult
	if err := json.Unmarshal([]byte(payload), &results); err != nil {
		t.Fatalf("unmarshal age-absent payload: %v", err)
	}
	if results[0].Age != 0 || results[0].AgeHours != 0 {
		t.Errorf("absent age decoded as %+v, want zeros", results[0])
	}
}
