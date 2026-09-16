package core

import (
	"strings"
	"testing"
)

// These are real IRC bot lines, taken from the le-guin search that produced 138
// parse errors out of ~1000 result lines on 2026-09-16 - about 14% of the
// catalogue never reached the UI. Two shapes caused almost all of it: an
// UPPERCASE file extension (the extension search compared bytes against the
// lowercase fileTypes list) and a bot that prefixes the DCC hash and reports
// the format in parentheses instead of a dotted extension.
func TestParserHandlesBotLineShapes(t *testing.T) {
	cases := []struct {
		name   string
		line   string
		author string
		title  string
		format string
		size   string
		inFull string
	}{
		{
			name:   "uppercase extension",
			line:   "!Bsk LeGuin, Ursula K. - El mundo de Rocannon.PDF ::INFO:: 429.59KB.",
			author: "LeGuin, Ursula K.",
			title:  "El mundo de Rocannon",
			format: "pdf",
			size:   "429.59KB",
		},
		{
			name:   "uppercase extension, no INFO block",
			line:   "!Horla Le Guin & Ursula K. - Winter's King (v1.0).RTF.",
			author: "Le Guin & Ursula K.",
			title:  "Winter's King (v1.0)",
			format: "rtf",
			size:   "N/A",
		},
		{
			name:   "hash prefix and parenthesised format",
			line:   "!Ashurbanipal aeEcHkB1cpn6xAUQKhfedg - Jorge Luis Borges & Anthony Kerrigan - Ficciones [eng]  (AZW3) 419.4 KB - [Fiction, Short Stories (Single Author)].",
			author: "Jorge Luis Borges & Anthony Kerrigan",
			title:  "Ficciones",
			format: "azw3",
			size:   "419.4KB",
			// Full is what the Download button sends to the bot, so the hash has
			// to survive the author/title fix even though the parser no longer
			// reads it as the author.
			inFull: "!Ashurbanipal aeEcHkB1cpn6xAUQKhfedg - Jorge Luis Borges & Anthony Kerrigan - Ficciones [eng]  (AZW3)",
		},
		{
			name:   "parenthesised audio format",
			line:   "!Ashurbanipal l5W4yDYozHUWCTOo+sN6hQ - Terry Pratchett - The Fifth Elephant (Penguin Audio) (audiobook) - Discworld [24] (M4B) 755.3 MB - [Science Fiction].",
			author: "Terry Pratchett",
			title:  "The Fifth Elephant (Penguin Audio) (audiobook) - Discworld",
			format: "m4b",
			size:   "755.3MB",
		},
		{
			name:   "audiobook with a narrator note",
			line:   "!Ashurbanipal qW9Nv0IXFk0+FfVo9kaDaA - Ursula K. Le Guin - Always Coming Home (audiobook) (Narrated by: Yareli Arizmendi, Isabella Star LeBlanc)  (M4B) 150.1 MB - [Audiobooks].",
			author: "Ursula K. Le Guin",
			title:  "Always Coming Home (audiobook) (Narrated by: Yareli Arizmendi, Isabella Star LeBlanc)",
			format: "m4b",
			size:   "150.1MB",
		},
	}

	for _, c := range cases {
		books, errs := ParseSearchV2(strings.NewReader(c.line))
		if len(errs) != 0 {
			t.Errorf("%s: unexpected parse error %v", c.name, errs)
			continue
		}
		if len(books) != 1 {
			t.Errorf("%s: %d results, want 1", c.name, len(books))
			continue
		}
		b := books[0]
		if b.Author != c.author || b.Title != c.title || b.Format != c.format || b.Size != c.size {
			t.Errorf("%s: got author=%q title=%q format=%q size=%q, want %q %q %q %q",
				c.name, b.Author, b.Title, b.Format, b.Size, c.author, c.title, c.format, c.size)
		}
		if c.inFull != "" && b.Full != c.inFull {
			t.Errorf("%s: Full = %q, want %q", c.name, b.Full, c.inFull)
		}
	}
}

// A single-token author must not be mistaken for a DCC hash - the hash window
// is 20-28 base64 characters with a digit, and "Tolkien" is neither.
func TestParserKeepsShortAuthors(t *testing.T) {
	books, errs := ParseSearchV2(strings.NewReader("!Bsk Tolkien - The Silmarillion.epub ::INFO:: 820.83KB"))
	if len(errs) != 0 || len(books) != 1 {
		t.Fatalf("errs=%v books=%d", errs, len(books))
	}
	if books[0].Author != "Tolkien" || books[0].Title != "The Silmarillion" {
		t.Errorf("got author=%q title=%q, want Tolkien / The Silmarillion", books[0].Author, books[0].Title)
	}
}

// The hash window is narrow on purpose: a 13-character author name that merely
// contains digits must stay the author, and the case-insensitive extension
// search must not resurrect the author-less regression (a line with no " - "
// and no INFO block still parses).
func TestParserHashWindowIsNarrow(t *testing.T) {
	books, errs := ParseSearchV2(strings.NewReader(
		"!Bsk SomeAuthor12345 - A Book.epub ::INFO:: 1.00MB\n" +
			"!Bsk The Great Gatsby.EPUB ::INFO:: 254.73KB"))
	if len(errs) != 0 || len(books) != 2 {
		t.Fatalf("errs=%v books=%d", errs, len(books))
	}
	byTitle := map[string]BookDetail{}
	for _, b := range books {
		byTitle[b.Title] = b
	}
	book, ok := byTitle["A Book"]
	if !ok {
		t.Fatalf("titles = %v", byTitle)
	}
	if book.Author != "SomeAuthor12345" {
		t.Errorf("13-character token must stay the author, got %q", book.Author)
	}
	gatsby, ok := byTitle["The Great Gatsby"]
	if !ok {
		t.Fatalf("author-less line did not parse: %v", byTitle)
	}
	if gatsby.Author != "" || gatsby.Format != "epub" {
		t.Errorf("author-less parse = %+v, want empty author and epub", gatsby)
	}
}
