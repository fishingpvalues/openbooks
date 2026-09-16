package core

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/evan-buss/openbooks/irc"
)

// Specific irc.irchighway.net commands

// maxNickAttempts bounds the alternate nicknames Join tries when the server
// refuses the requested one during registration.
const maxNickAttempts = 5

// registrationDeadline is how long the registration handshake may take. The
// server's own registration timeout is shorter than this, so a refusal ends the
// wait with a read error rather than with the deadline.
const registrationDeadline = 20 * time.Second

// nickCandidates returns the nicknames to try during registration, in order:
// the configured nick first, then RANDOM-suffixed alternatives.
//
// PotatoStack v5.4.1: the nickname is a network-global resource, not a
// per-connection one. This stack collides with ITSELF systematically - the web
// UI opens a websocket session on every page load and the REST /api/v1 session
// (which the UI's own searches, the DAGs and the wanted poller use) is a second
// IRC connection built from the SAME configured nick - and any leftover client
// (a crashed process, TheLounge, another machine behind the same VPN exit)
// holds it too.
//
// PotatoStack v5.4.4: the suffixes are random. v5.4.1 used a deterministic
// `_`, `__`, `___` ladder, which the stack's own sessions pick just as
// predictably as the base nick - the second session lost the same race every
// time, and the third could exhaust the ladder and fail outright.
func nickCandidates(base string) []string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "openbooks"
	}
	out := make([]string, 0, maxNickAttempts)
	out = append(out, base)
	for i := 1; i < maxNickAttempts; i++ {
		out = append(out, base+"_"+randomSuffix())
	}
	return out
}

// randomSuffix returns four characters of entropy for a candidate nickname.
func randomSuffix() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	raw := make([]byte, 4)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand does not fail in practice; a constant suffix is still
		// better than no candidate at all.
		return "alt0"
	}
	for i, b := range raw {
		raw[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(raw)
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

// register drives the registration handshake on an already connected socket.
//
// It returns nil once the 001 welcome arrives, the read error when the socket
// dies, and an error when the deadline passes without either. A nickname
// refusal moves to the next candidate on the SAME socket - that is what a
// colliding session with a DIFFERENT user@host looks like, and irchighway keeps
// the socket open for it - and advances *nick so the caller reconnects with the
// following candidate if this socket dies instead.
func register(irc *irc.Conn, candidates []string, nick *int) error {
	deadline := time.Now().Add(registrationDeadline)
	buf := make([]byte, 1)
	var line []byte

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
			return nil
		case isPingRequest(text):
			irc.Pong(strings.TrimSpace(text[5:]))
		case registrationNickRejected(text) && *nick+1 < len(candidates):
			// ChangeNick records the new name on the Conn as well, so the
			// UI detail, the IRC log file name and the /stats listing show
			// the nickname the server actually gave us.
			*nick++
			irc.ChangeNick(candidates[*nick])
		}
	}
	return fmt.Errorf("registration did not complete within %s", registrationDeadline)
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
// 436 by retrying with the next nickCandidates entry and keep registering.
//
// PotatoStack patch (v5.4.4): irchighway does NOT always keep the socket open
// after refusing a nickname. Measured 2026-09-16 against the live server: when
// the colliding session has the SAME user@host - which is exactly what this
// stack does to itself, the UI's websocket session and the REST api session
// being two connections from one configured nick - the server answers
// `433 * <nick> :Nickname is already in use.` and closes the socket immediately
// (0.1s, not the registration timeout). The v5.4.1 in-place rename therefore
// never got a chance to register, and every REST search answered
// `502 {"error":"api IRC connect: EOF"}` while the UI searched happily. A dead
// socket mid-registration now dials again with the next candidate. The
// candidate ladder is random-suffixed for the same reason (see nickCandidates).
func Join(irc *irc.Conn, address string, enableTLS bool) error {
	candidates := nickCandidates(irc.Username)
	nick := 0
	var lastErr error

	for {
		irc.Username = candidates[nick]
		if err := irc.Connect(address, enableTLS); err != nil {
			lastErr = err
			if nick+1 >= len(candidates) {
				break
			}
			nick++
			continue
		}

		err := register(irc, candidates, &nick)
		if err == nil {
			irc.JoinChannel("ebooks")
			return nil
		}
		lastErr = err
		irc.Disconnect()
		if nick+1 >= len(candidates) {
			break
		}
		nick++
	}

	if lastErr == nil {
		lastErr = errors.New("IRC registration did not complete")
	}
	return lastErr
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
