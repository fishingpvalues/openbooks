package core

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// List of file extensions that I've encountered.
// Some of them aren't eBooks, but they were returned
// in previous search results.
var fileTypes = [...]string{
	"epub",
	"mobi",
	"azw3",
	"html",
	"rtf",
	"pdf",
	"cdr",
	"lit",
	"cbr",
	"doc",
	"htm",
	"jpg",
	"txt",
	"rar", // Compressed extensions should always be last 2 items
	"zip",
}

// BookDetail contains the details of a single Book found on the IRC server
type BookDetail struct {
	Server string `json:"server"`
	Author string `json:"author"`
	Title  string `json:"title"`
	Format string `json:"format"`
	Size   string `json:"size"`
	Full   string `json:"full"`
}

type ParseError struct {
	Line  string `json:"line"`
	Error error  `json:"error"`
}

func (p *ParseError) MarshalJSON() ([]byte, error) {
	item := struct {
		Line  string `json:"line"`
		Error string `json:"error"`
	}{
		Line:  p.Line,
		Error: p.Error.Error(),
	}
	return json.Marshal(item)
}

func (p ParseError) String() string {
	return fmt.Sprintf("Error: %s. Line: %s.", p.Error, p.Line)
}

// indexExtCI returns the byte index of the first "." + ext in line, ignoring
// ASCII case, or -1.
//
// PotatoStack v5.4.5: the bots do not agree on casing. "!Bsk LeGuin, Ursula K.
// - El mundo de Rocannon.PDF ::INFO:: 429.59KB" used to fail with "unable to
// parse title" because fileTypes holds lowercase names and the search compared
// bytes. Only ASCII is lowered here: strings.ToLower can change the byte length
// for some Unicode, which would invalidate the offsets the caller slices with.
func indexExtCI(line, ext string) int {
	for i := 0; i+len(ext)+1 <= len(line); i++ {
		if line[i] != '.' {
			continue
		}
		match := true
		for j := 0; j < len(ext); j++ {
			c := line[i+1+j]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != ext[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// hashLike reports whether s looks like the DCC file hash a few bots put
// between the server name and the author: 20 to 28 characters of base64 with at
// least one digit ("aeEcHkB1cpn6xAUQKhfedg", "tpturc+xuQRM7VT+XxKCQQ"). The
// window is deliberately narrow so it cannot swallow a single-token author
// ("!Bsk Tolkien - The Silmarillion.epub").
func hashLike(s string) bool {
	if len(s) < 20 || len(s) > 28 {
		return false
	}
	digit := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			digit = true
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '+', c == '/':
		default:
			return false
		}
	}
	return digit
}

// stripLeadingHash removes a "<hash> - " field sitting between the server and
// the author, keeping the "!Server " prefix so getServer still works. Without
// it the hash reads as the author and the title swallows the author.
func stripLeadingHash(line string) string {
	firstSpace := strings.Index(line, " ")
	if firstSpace == -1 {
		return line
	}
	sep := strings.Index(line[firstSpace:], " - ")
	if sep == -1 {
		return line
	}
	sep += firstSpace
	if !hashLike(line[firstSpace+1 : sep]) {
		return line
	}
	return line[:firstSpace+1] + line[sep+len(" - "):]
}

// parenFormat returns the index of a "(FORMAT)" token after start whose inner
// text is a known file type, plus that type. Bots that omit the dotted
// extension report the format in parentheses before the size instead
// ("... Ficciones [eng]  (AZW3) 419.4 KB - [...]").
func parenFormat(line string, start int) (int, string) {
	for i := start; i < len(line); i++ {
		if line[i] != '(' {
			continue
		}
		end := strings.IndexByte(line[i:], ')')
		if end == -1 {
			return -1, ""
		}
		inner := strings.ToLower(strings.TrimSpace(line[i+1 : i+end]))
		for _, ext := range fileTypes {
			if inner == ext {
				return i, ext
			}
		}
		// Audio results carry their own list (see audioFormats): m4b, mp3 and
		// friends are deliberately not in fileTypes, so without this the
		// audiobook hits - 14 of the 17 lines still failing after v5.4.5 - keep
		// landing in the Parsing Errors panel.
		for _, ext := range audioFormats {
			if inner == ext {
				return i, ext
			}
		}
	}
	return -1, ""
}

// trimLangMarker drops a trailing "[eng]" language marker that sits between the
// title and the parenthesised format. It only touches short bracket tokens, so
// a title that genuinely ends in brackets survives.
func trimLangMarker(title string) string {
	if !strings.HasSuffix(title, "]") {
		return title
	}
	open := strings.LastIndex(title, "[")
	if open == -1 {
		return title
	}
	inner := strings.TrimSpace(title[open+1 : len(title)-1])
	if inner == "" || len(inner) > 8 || strings.ContainsAny(inner, "[]") {
		return title
	}
	return strings.TrimSpace(title[:open])
}

// spacedSize matches a size the bots write with a space ("419.4 KB") on lines
// that carry no " ::INFO:: " block at all.
//
// The leading \b matters: without it the "4B" inside a parenthesised format
// token matches first, and every audiobook row reports a 4-byte size.
var spacedSize = regexp.MustCompile(`\b(\d+(?:\.\d+)?)\s*([kKmMgG]?[bB])\b`)

// ParseSearchFile converts a single search file into an array of BookDetail
func ParseSearchFile(filePath string) ([]BookDetail, []ParseError, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()

	books, errs := ParseSearchV2(file)
	return books, errs, nil
}

func ParseSearch(reader io.Reader) ([]BookDetail, []ParseError) {
	var books []BookDetail
	var parseErrors []ParseError

	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "!") {
			dat, err := parseLine(line)
			if err != nil {
				parseErrors = append(parseErrors, ParseError{Line: line, Error: err})
			} else {
				books = append(books, dat)
			}
		}
	}

	sort.Slice(books, func(i, j int) bool { return books[i].Server < books[j].Server })

	return books, parseErrors
}

// Parse line extracts data from a single line
func parseLine(line string) (BookDetail, error) {

	//First check if it follows the correct format. Some servers don't include file info...
	if !strings.Contains(line, "::INFO::") {
		return BookDetail{}, errors.New("invalid line format. ::INFO:: not found")
	}

	var book BookDetail
	book.Full = line[:strings.Index(line, " ::INFO:: ")]
	var tmp int

	// Get Server
	if tmp = strings.Index(line, " "); tmp == -1 {
		return BookDetail{}, errors.New("could not parse server")
	}
	book.Server = line[1:tmp] // Skip the "!"
	line = line[tmp+1:]

	// Get the Author
	if tmp = strings.Index(line, " - "); tmp == -1 {
		return BookDetail{}, errors.New("could not parse author")
	}
	book.Author = line[:tmp]
	line = line[tmp+len(" - "):]

	// Get the Title
	for _, ext := range fileTypes { //Loop through each possible file extension we've got on record
		tmp = strings.Index(line, "."+ext) // check if it contains our extension
		if tmp == -1 {
			continue
		}
		book.Format = ext
		if ext == "rar" || ext == "zip" { // If the extension is .rar or .zip the actual format is contained in ()
			for _, ext2 := range fileTypes[:len(fileTypes)-2] { // Range over the eBook formats (exclude archives)
				if strings.Contains(line[:tmp], ext2) {
					book.Format = ext2
				}
			}
		}
		book.Title = line[:tmp]
		line = line[tmp+len(ext)+1:]
	}

	if book.Title == "" { // Got through the entire loop without finding a single match
		return BookDetail{}, errors.New("could not parse title")
	}

	// Get the Size
	if tmp = strings.Index(line, "::INFO:: "); tmp == -1 {
		return BookDetail{}, errors.New("could not parse size")
	}

	line = strings.TrimSpace(line)
	splits := strings.Split(line, " ")

	if len(splits) >= 2 {
		book.Size = splits[1]
	}

	return book, nil
}

func ParseSearchV2(reader io.Reader) ([]BookDetail, []ParseError) {
	books := make([]BookDetail, 0)
	parseErrors := make([]ParseError, 0)

	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "!") {
			dat, err := parseLineV2(line)
			if err != nil {
				parseErrors = append(parseErrors, ParseError{Line: line, Error: err})
			} else {
				books = append(books, dat)
			}
		}
	}

	sort.Slice(books, func(i, j int) bool { return books[i].Server < books[j].Server })

	return books, parseErrors
}

func parseLineV2(line string) (BookDetail, error) {
	getServer := func(line string) (string, error) {
		if line[0] != '!' {
			return "", errors.New("result lines must start with '!'\n")
		}

		firstSpace := strings.Index(line, " ")
		if firstSpace == -1 {
			return "", errors.New("unable parse server name")
		}

		return line[1:firstSpace], nil
	}

	getAuthor := func(line string) (string, error) {
		firstSpace := strings.Index(line, " ")
		dashChar := strings.Index(line, " - ")
		if dashChar == -1 || dashChar < firstSpace+len(" ") {
			// No " - " author separator. Real search results (and the test
			// fixture) contain author-less lines - e.g.
			//   !Bsk TheGreatGatsby.epub ::INFO:: 237.78KB
			// Returning "" (instead of an error) keeps them in the result
			// set. Hard-failing here dropped ~5% of live search results.
			return "", nil
		}
		author := line[firstSpace+len(" ") : dashChar]

		// Handles case with weird author characters %\w% ("%F77FE9FF1CCD% Michael Haag")
		if strings.Contains(author, "%") {
			split := strings.SplitAfterN(author, " ", 2)
			return split[1], nil
		}

		return author, nil
	}

	getTitle := func(line string) (string, string, int) {
		title := ""
		fileFormat := ""
		endIndex := -1

		// Title start: after the " - " author separator when present,
		// otherwise right after the server name (author-less lines).
		// The old code sliced line[2:endTitle] for these, dropping the
		// first two characters of the title.
		dashIdx := strings.Index(line, " - ")
		titleStart := dashIdx + len(" - ")
		if dashIdx == -1 {
			if sp := strings.Index(line, " "); sp != -1 {
				titleStart = sp + 1
			}
		}

		// Get the Title
		for _, ext := range fileTypes { //Loop through each possible file extension we've got on record
			endTitle := indexExtCI(line, ext) // check if it contains our extension
			if endTitle == -1 {
				continue
			}
			fileFormat = ext
			if ext == "rar" || ext == "zip" { // If the extension is .rar or .zip the actual format is contained in ()
				for _, ext2 := range fileTypes[:len(fileTypes)-2] { // Range over the eBook formats (exclude archives)
					if strings.Contains(strings.ToLower(line[:endTitle]), ext2) {
						fileFormat = ext2
					}
				}
			}
			title = line[titleStart:endTitle]
			endIndex = endTitle
		}

		if endIndex == -1 {
			// No dotted extension anywhere on the line. Some bots report the
			// format in parentheses and put a language marker in brackets
			// after the title instead.
			if at, ext := parenFormat(line, titleStart); at != -1 {
				fileFormat = ext
				title = trimLangMarker(strings.TrimSpace(line[titleStart:at]))
				endIndex = at
			}
		}

		return title, fileFormat, endIndex
	}

	getSize := func(line string, after int) (string, int) {
		const delimiter = " ::INFO:: "
		infoIndex := strings.LastIndex(line, delimiter)

		if infoIndex != -1 {
			// Handle cases when there is additional info after the file size (ex ::HASH:: )
			parts := strings.Split(line[infoIndex+len(delimiter):], " ")
			// Trailing punctuation is common on these lines ("429.59KB.").
			return strings.Trim(parts[0], ".,;:"), infoIndex
		}

		// No INFO block. Bots in that shape still report a size ("419.4 KB"),
		// and only a match AFTER the title may be used: Full is the line the
		// Download button sends to the bot, so cutting inside a title would
		// produce a broken request.
		if m := spacedSize.FindStringSubmatchIndex(line); m != nil && m[0] >= after {
			return line[m[2]:m[3]] + line[m[4]:m[5]], m[0]
		}

		return "N/A", len(line)
	}

	server, err := getServer(line)
	if err != nil {
		return BookDetail{}, err
	}

	// Some bots prefix the DCC file hash ("!Ashurbanipal <hash> - Author - ...").
	// Full is rebuilt from the ORIGINAL line below, so the hash - which is part
	// of the request the bot expects - survives the author/title fix.
	original := line
	line = stripLeadingHash(line)
	hashOffset := len(original) - len(line)

	author, err := getAuthor(line)
	if err != nil {
		return BookDetail{}, err
	}

	title, format, titleIndex := getTitle(line)
	if titleIndex == -1 {
		return BookDetail{}, errors.New("unable to parse title")
	}

	size, endIndex := getSize(line, titleIndex)

	fullEnd := endIndex + hashOffset
	if fullEnd > len(original) {
		fullEnd = len(original)
	}

	return BookDetail{
		Server: server,
		Author: author,
		Title:  title,
		Format: format,
		Size:   size,
		Full:   strings.TrimSpace(original[:fullEnd]),
	}, nil
}

// PotatoStack v5.3.0: quality-control helpers for the search layer.
//
// The size unit and the format class are properties of the IRC search bot's
// output format, so the parsing lives next to the parser: the bot reports
// sizes like "237.78KB" / "1.2MB" / "N/A" (1024 scale - verified against
// DCC transfer sizes, the KB value times 1024 matches the landed file), and
// the extension in a result line is the file type.

// audioFormats are the extensions openbooks treats as audiobooks for the
// quality filters (prefer=audiobook) and the sidecar logic. The IRC bot's
// parser (fileTypes above) does not list them - audio results arrive with an
// empty Format field, which is why the class check must also match on the
// raw extension (FormatOrExt in the server layer).
var audioFormats = []string{
	"mp3", "m4b", "m4a", "m4p", "ogg", "oga", "flac", "aac", "wav", "wma",
}

// ParseSizeToBytes converts a size the IRC search bot reports into bytes.
// The bot's unit scale is 1024. Unparseable or missing sizes ("", "N/A")
// return 0, which callers must treat as "size unknown" (never a reason to
// reject a result). The IRC leg is spaceless ("237.78KB"); other sources
// (Prowlarr) may space the unit ("900 MB"), so all whitespace is stripped
// before the suffix match - otherwise a spaced size parses as unknown and
// the size cap silently stops applying to that source's results.
func ParseSizeToBytes(s string) int64 {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" || s == "N/A" {
		return 0
	}
	s = strings.ReplaceAll(s, " ", "")
	// Longest suffix first: GB before MB before KB.
	for _, u := range []struct {
		suffix string
		factor int64
	}{
		{"GB", 1024 * 1024 * 1024},
		{"MB", 1024 * 1024},
		{"KB", 1024},
	} {
		if strings.HasSuffix(s, u.suffix) {
			f, err := strconv.ParseFloat(s[:len(s)-len(u.suffix)], 64)
			if err != nil {
				return 0
			}
			return int64(f * float64(u.factor))
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// IsAudioFormat reports whether ext (lowercased, no dot) is an audiobook
// extension.
func IsAudioFormat(ext string) bool {
	for _, a := range audioFormats {
		if ext == a {
			return true
		}
	}
	return false
}

// IsEbookFormat reports whether ext (lowercased, no dot) is an ebook
// extension. It is the parser's fileTypes list (minus the "jpg" the bot
// sometimes reports for image books) plus fb2, which the library pipeline
// handles but the parser does not list.
func IsEbookFormat(ext string) bool {
	for _, f := range fileTypes {
		if f == "jpg" {
			continue
		}
		if ext == f {
			return true
		}
	}
	return ext == "fb2"
}
