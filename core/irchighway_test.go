package core

import (
	"bufio"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/evan-buss/openbooks/irc"
)

// TestNickCandidates moved to irchighway_reconnect_test.go in v5.4.4: the
// candidate suffixes are random now, so that file asserts the shape instead of
// the exact `_`/`__` ladder.

func TestRegistrationLineClassification(t *testing.T) {
	cases := []struct {
		line     string
		complete bool
		rejected bool
		ping     bool
	}{
		{":fang.fr.eu.irchighway.net 001 potatobooks_ :Welcome", true, false, false},
		{":fang.fr.eu.irchighway.net 433 * potatobooks :Nickname is already in use.", false, true, false},
		{":srv 432 * potatobooks :Erroneous nickname", false, true, false},
		{":srv 436 potatobooks potatobooks_ :Nickname collision KILL", false, true, false},
		{"PING :fang.fr.eu.irchighway.net", false, false, true},
		{":srv 353 potatobooks_ = #ebooks :@SearchBot", false, false, false},
	}
	for _, c := range cases {
		if got := registrationComplete(c.line); got != c.complete {
			t.Errorf("registrationComplete(%q) = %v, want %v", c.line, got, c.complete)
		}
		if got := registrationNickRejected(c.line); got != c.rejected {
			t.Errorf("registrationNickRejected(%q) = %v, want %v", c.line, got, c.rejected)
		}
		if got := isPingRequest(c.line); got != c.ping {
			t.Errorf("isPingRequest(%q) = %v, want %v", c.line, got, c.ping)
		}
	}
}

// TestJoinRetriesOnNickCollision drives the real Join against a loopback stub
// that answers the first NICK with 433 (what irchighway sends when the nickname
// is already held by another session) and the second NICK with 001. Before the
// v5.4.1 fallback this path ended in the server's registration timeout, i.e. a
// bare EOF and no registration.
func TestJoinRetriesOnNickCollision(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	type seenLines struct {
		firstNick  string
		secondNick string
		join       string
	}
	seen := make(chan seenLines, 1)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		readLine := func() string {
			line, err := reader.ReadString('\n')
			if err != nil && line == "" {
				return ""
			}
			return strings.TrimSpace(line)
		}

		// Connect() writes USER first, then NICK.
		readLine()          // USER
		first := readLine() // NICK potatobooks
		io.WriteString(conn, ":srv 433 * potatobooks :Nickname is already in use.\r\n")
		second := readLine() // NICK potatobooks_<random>
		// Echo back whatever nick the client chose - v5.4.4 made the fallback
		// suffix random, so the stub cannot hard-code it.
		io.WriteString(conn, ":srv 001 "+strings.TrimSpace(strings.TrimPrefix(second, "NICK "))+" :Welcome to the network\r\n")
		io.WriteString(conn, "PING :srv\r\n")
		seen <- seenLines{firstNick: first, secondNick: second, join: readLine()}
	}()

	client := irc.New("potatobooks", "OpenBooks test")
	if err := Join(client, listener.Addr().String(), false); err != nil {
		t.Fatalf("Join returned %v, want nil after the nick fallback", err)
	}
	if !strings.HasPrefix(client.Username, "potatobooks_") || len(client.Username) != len("potatobooks_")+4 {
		t.Errorf("client.Username = %q, want the random fallback nick potatobooks_<4 chars>", client.Username)
	}

	select {
	case lines := <-seen:
		if lines.firstNick != "NICK potatobooks" {
			t.Errorf("first NICK = %q, want \"NICK potatobooks\"", lines.firstNick)
		}
		if !strings.HasPrefix(lines.secondNick, "NICK potatobooks_") {
			t.Errorf("second NICK = %q, want a NICK for a potatobooks_* candidate", lines.secondNick)
		}
		if lines.join != "JOIN #ebooks" {
			t.Errorf("after 001 the client sent %q, want \"JOIN #ebooks\"", lines.join)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stub never observed the JOIN; the client did not register")
	}
}
