package core

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/evan-buss/openbooks/irc"
)

// TestNickCandidatesShape pins the v5.4.4 change. The deterministic `_`/`__`
// ladder of v5.4.1 collided with this stack's own older sessions just as
// predictably as the base nick - the UI's websocket session, the REST api
// session and the wanted poller all start from one configured nick - so the
// second session lost the same race every time and the third could exhaust the
// ladder outright.
func TestNickCandidatesShape(t *testing.T) {
	got := nickCandidates("potatobooks")
	if len(got) != maxNickAttempts {
		t.Fatalf("nickCandidates len = %d, want %d (%v)", len(got), maxNickAttempts, got)
	}
	if got[0] != "potatobooks" {
		t.Errorf("first candidate = %q, want the configured nick unchanged", got[0])
	}
	seen := map[string]bool{got[0]: true}
	for _, nick := range got[1:] {
		if !strings.HasPrefix(nick, "potatobooks_") {
			t.Errorf("candidate %q does not extend the configured nick", nick)
		}
		if len(nick) != len("potatobooks_")+4 {
			t.Errorf("candidate %q does not carry a 4-character suffix", nick)
		}
		if seen[nick] {
			t.Errorf("duplicate candidate %q", nick)
		}
		seen[nick] = true
	}

	// An empty configured nick must still produce usable candidates.
	empty := nickCandidates("  ")
	if len(empty) != maxNickAttempts || empty[0] != "openbooks" {
		t.Errorf("empty configured nick = %v, want openbooks first", empty)
	}
}

// TestJoinReconnectsWhenTheServerClosesAfter433 is the regression test for the
// measured irchighway behaviour that v5.4.1 missed: when the colliding session
// has the SAME user@host, the server answers `433 * <nick>` and closes the
// socket immediately. Renaming on that dying socket is useless - the next read
// returns EOF and every REST search answered 502 {"error":"api IRC connect:
// EOF"} - so Join has to dial again under the next candidate. The stub below
// refuses and hangs up on the first connection, then accepts the second.
func TestJoinReconnectsWhenTheServerClosesAfter433(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	accepted := make(chan int, 4)
	go func() {
		for i := 0; ; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted <- i
			go func(conn net.Conn, first bool) {
				defer conn.Close()
				reader := bufio.NewReader(conn)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					// The client sends USER and then NICK (see irc.Conn.Connect),
					// so the refusal is answered once the nick is on the wire.
					if !strings.HasPrefix(line, "NICK ") {
						continue
					}
					nick := strings.TrimSpace(strings.TrimPrefix(line, "NICK "))
					if first {
						conn.Write([]byte(":srv 433 * " + nick + " :Nickname is already in use.\r\n"))
						time.Sleep(30 * time.Millisecond)
						return
					}
					conn.Write([]byte(":srv 001 " + nick + " :Welcome to irchighway\r\n"))
					conn.Write([]byte(":srv 376 " + nick + " :End of MOTD\r\n"))
				}
			}(conn, i == 0)
		}
	}()

	client := irc.New("potatobooks", "potatobooks")
	if err := Join(client, listener.Addr().String(), false); err != nil {
		t.Fatalf("Join = %v, want nil (it must reconnect under a fresh nick)", err)
	}
	if client.Username == "potatobooks" || !strings.HasPrefix(client.Username, "potatobooks_") {
		t.Errorf("registered nick = %q, want a random-suffixed candidate", client.Username)
	}
	if n := len(accepted); n < 2 {
		t.Errorf("server accepted %d connections, want at least 2 (a reconnect)", n)
	}
}
