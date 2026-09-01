package core

import (
	"fmt"
	"strings"
	"time"

	"github.com/evan-buss/openbooks/irc"
)

// Specific irc.irchighway.net commands

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
func Join(irc *irc.Conn, address string, enableTLS bool) error {
	err := irc.Connect(address, enableTLS)
	if err != nil {
		return err
	}
	buf := make([]byte, 1)
	var line []byte
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := irc.Read(buf); err != nil {
			return err
		}
		if buf[0] == '\n' {
			text := string(line)
			if strings.Contains(text, " 001 ") {
				irc.JoinChannel("ebooks")
				return nil
			}
			if strings.HasPrefix(text, "PING ") || strings.HasPrefix(text, "PING :") {
				irc.Pong(strings.TrimSpace(text[5:]))
			}
			line = line[:0]
			continue
		}
		line = append(line, buf[0])
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
