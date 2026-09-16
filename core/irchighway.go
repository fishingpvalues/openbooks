package core

import (
	"fmt"
	"strings"
	"time"

	"github.com/evan-buss/openbooks/irc"
)

// Specific irc.irchighway.net commands

// maxNickAttempts bounds the alternate nicknames Join tries when the server
// refuses the requested one during registration.
const maxNickAttempts = 5

// nickCandidates returns the nicknames to try during registration, in order:
// the configured nick first, then underscore-suffixed variants.
//
// PotatoStack v5.4.1: the nickname is a network-global resource, not a
// per-connection one. This stack collides with ITSELF systematically - the web
// UI opens a websocket session on every page load and the REST /api/v1 session
// (which the UI's own searches, the DAGs and the wanted poller use) is a second
// IRC connection built from the SAME configured nick - and any leftover client
// (a crashed process, TheLounge, another machine behind the same VPN exit)
// holds it too. The server answers the loser with 433 during registration and
// then drops the socket at its registration timeout, which the callers saw as a
// bare `api IRC connect: EOF` and a 502 on every search.
func nickCandidates(base string) []string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "openbooks"
	}
	out := make([]string, 0, maxNickAttempts)
	out = append(out, base)
	for i := 1; i < maxNickAttempts; i++ {
		out = append(out, base+strings.Repeat("_", i))
	}
	return out
}

// registrationComplete reports whether the line is the 001 welcome, the
// definitive "you are registered" signal.
func registrationComplete(line string) bool {
	return strings.Contains(line, " 001 ")
}

// registrationNickRejected reports whether the line is the server refusing the
// nickname we tried to register with:
//
//	432 <nick> :Erroneous nickname
//	433 <nick> :Nickname is already in use
//	436 <nick> :Nickname collision KILL
func registrationNickRejected(line string) bool {
	return strings.Contains(line, " 432 ") ||
		strings.Contains(line, " 433 ") ||
		strings.Contains(line, " 436 ")
}

// isPingRequest reports whether the line is a server PING.
func isPingRequest(line string) bool {
	return strings.HasPrefix(line, "PING ") || strings.HasPrefix(line, "PING :")
}

// Join connects to the irc.irchighway.net server and joins the #ebooks channel.
//
// PotatoStack patch (v4.5.0+local): the upstream code sleeps a fixed 2s and
// then joins, which races the server-side registration handshake. irchighway
// reverse-DNSes the connecting IP; for datacenter/VPN egress IPs (no PTR
// record) that lookup TIMES OUT, registration takes ~5s+, and the JOIN is sent
// unregistered - the server answers `451 JOIN :You have not registered` and
// the client never retries, so every search afterwards silently dies while the
// client looks connected. Instead, wait for the 001 welcome (the definitive
// "registration complete" signal) before joining, answering PINGs on the way.
// Bytes are read one at a time from the raw conn (no buffering) so the reader
// loop started after Join() still sees every remaining line.
//
// PotatoStack patch (v5.4.1): a nickname that is already taken is not a
// connection problem, and waiting for the registration deadline makes it look
// like one - the server closes the socket and Join returns EOF. Handle 432/433/
// 436 by retrying with the next nickCandidates entry and keep registering. The
// fallback is bounded (maxNickAttempts); once exhausted, the deadline below
// still applies.
func Join(irc *irc.Conn, address string, enableTLS bool) error {
	err := irc.Connect(address, enableTLS)
	if err != nil {
		return err
	}
	candidates := nickCandidates(irc.Username)
	nextNick := 1

	buf := make([]byte, 1)
	var line []byte
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := irc.Read(buf); err != nil {
			return err
		}
		if buf[0] != '\n' {
			line = append(line, buf[0])
			continue
		}
		text := strings.TrimSpace(string(line))
		line = line[:0]

		switch {
		case registrationComplete(text):
			irc.JoinChannel("ebooks")
			return nil
		case isPingRequest(text):
			irc.Pong(strings.TrimSpace(text[5:]))
		case registrationNickRejected(text) && nextNick < len(candidates):
			// ChangeNick records the new name on the Conn as well, so the
			// UI detail, the IRC log file name and the /stats listing show
			// the nickname the server actually gave us.
			irc.ChangeNick(candidates[nextNick])
			nextNick++
		}
	}
	// Registration never completed in time - join anyway and hope for the best.
	irc.JoinChannel("ebooks")
	return nil
}

// SearchBook sends a search query to the search bot
func SearchBook(irc *irc.Conn, searchBot string, query string) {
	searchBot = strings.TrimPrefix(searchBot, "@")
	irc.SendMessage(fmt.Sprintf("@%s %s", searchBot, query))
}

// DownloadBook sends the book string to the download bot
func DownloadBook(irc *irc.Conn, book string) {
	irc.SendMessage(book)
}

// Send a CTCP Version response
func SendVersionInfo(irc *irc.Conn, line string, version string) {
	// Line format is like ":messager PRIVMSG #channel: message"
	// we just want the messager without the colon
	sender := strings.Split(line, " ")[0][1:]
	// TODO: Figure out if there's an automated way to adjust this...
	irc.SendNotice(sender, fmt.Sprintf("\x01%s\x01", version))
}
