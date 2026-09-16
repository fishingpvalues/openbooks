package irc

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
)

// pinnedIRCCertFingerprints maps an IRC server hostname to the set of
// SHA-256 fingerprints of the leaf TLS certificates it may present.
//
// Why this exists (measured 2026-09-01):
//   - irc.irchighway.net:6697 (the default search server) is behind a
//     proxy pool (e.g. fang.fr.eu.irchighway.net) and each backend serves
//     its OWN self-signed certificate that has NO subject CN and NO SAN
//     (subject/issuer: C=FR, O=IRCHighway). Standard TLS verification
//     therefore CANNOT succeed against any of them - Go fails with
//     "certificate is not valid for any names". That is why upstream
//     shipped InsecureSkipVerify:true.
//   - A 12-sample probe (2026-09-01) saw three distinct fingerprints:
//
//	f50231e6e4594259d5522a92cc79cee4173a5bb56b32352fe9ee064fc5013d25 (8/12)
//	7715fe7f757ce3e035ac127c32bd7256ac352a246a987b284ece3a69c7717cb4 (2/12)
//	527c93465e26c075ce37070ba558ae52ea7fc82105d277af0655e4c467d6367f (2/12)
//
//   - InsecureSkipVerify:true is a real vulnerability (a MITM on the
//     IRC/DCC path - where book content is fetched over - could read or
//     replace every download).
//
// The fix is NOT to keep skipping verify, and NOT to force strict verify
// (impossible here). It is to pin the certificate fingerprints: the
// handshake still performs the full TLS record/crypto, but the peer's leaf
// certificate must hash to one of the pinned values. A MITM presenting a
// certificate outside the set is rejected. Adding a value here requires
// verifying it from two independent egress paths (host + VPN netns), so a
// single compromised dial cannot expand the allow-set on its own.
//
// To capture a fingerprint (e.g. a new backend appears in the logs):
//
//	openssl s_client -connect <host>:6697 -servername <host> 2>/dev/null |
//	    openssl x509 -noout -fingerprint -sha256
var pinnedIRCCertFingerprints = map[string][]string{
	"irc.irchighway.net": {
		"f50231e6e4594259d5522a92cc79cee4173a5bb56b32352fe9ee064fc5013d25",
		"7715fe7f757ce3e035ac127c32bd7256ac352a246a987b284ece3a69c7717cb4",
		"527c93465e26c075ce37070ba558ae52ea7fc82105d277af0655e4c467d6367f",
	},
}

// pinnedTLSConfig returns a tls.Config for connecting to the given IRC
// address. It either pins the known self-signed server's certificate
// fingerprint, or falls back to strict verification for everything else.
func pinnedTLSConfig(address string) (*tls.Config, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		// No port in the address; treat the whole string as the host so
		// the pin lookup (and strict fallback) still behave sanely.
		host = address
	}

	pins, ok := pinnedIRCCertFingerprints[host]
	if !ok || len(pins) == 0 {
		// Not a known self-signed server: verify normally. This is the
		// secure default and is what applies to any --server the operator
		// configures that uses a proper CA-issued certificate.
		return &tls.Config{MinVersion: tls.VersionTLS12}, nil
	}

	want := make([][32]byte, 0, len(pins))
	for _, pinHex := range pins {
		b, err := hex.DecodeString(pinHex)
		if err != nil || len(b) != 32 {
			return nil, errors.New("irc: malformed pinned certificate fingerprint for " + host)
		}
		var f [32]byte
		copy(f[:], b)
		want = append(want, f)
	}

	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Skip the root-CA and hostname checks (the self-signed certs have
		// no chain and no hostname to match), but keep
		// VerifyPeerCertificate so the leaf fingerprint is still enforced
		// against the pinned set. Verified empirically that this callback
		// runs even with InsecureSkipVerify set and that a non-nil error
		// aborts the handshake.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("irc: no certificate presented")
			}
			got := sha256.Sum256(rawCerts[0])
			matched := 0
			for i := range want {
				// OR-fold the constant-time comparisons so the timing does
				// not reveal which slot (if any) matched.
				matched |= subtle.ConstantTimeCompare(got[:], want[i][:])
			}
			if matched == 0 {
				return errors.New("irc: certificate fingerprint is not in the pinned set (possible MITM or new backend); see irc.go to capture and add the fingerprint")
			}
			return nil
		},
	}, nil
}

// Conn represents an IRC connection to a server
type Conn struct {
	net.Conn
	channel  string
	Username string
	realname string
}

// New creates a new IRC connection to the server using the supplied username and realname
func New(username, realname string) *Conn {
	irc := &Conn{
		channel:  "",
		Username: username,
		realname: realname,
	}

	return irc
}

// Connect connects to the given server at port 6667
func (i *Conn) Connect(address string, enableTLS bool) error {
	var conn net.Conn
	var err error
	if enableTLS {
		cfg, cfgErr := pinnedTLSConfig(address)
		if cfgErr != nil {
			return cfgErr
		}
		conn, err = tls.Dial("tcp", address, cfg)
	} else {
		conn, err = net.Dial("tcp", address)
	}

	if err != nil {
		return err
	}

	i.Conn = conn

	user := "USER " + i.Username + " 0 * :" + i.Username + "\r\n"
	nick := "NICK " + i.Username + "\r\n"

	i.Write([]byte(user))
	i.Write([]byte(nick))
	return nil
}

// Disconnect closes connection to the server
func (i *Conn) Disconnect() {
	if !i.IsConnected() {
		return
	}
	i.Write([]byte("QUIT :Goodbye\r\n"))
	i.Conn.Close()
}

// SendMessage sends the given message string to the connected IRC server
func (i *Conn) SendMessage(message string) {
	if !i.IsConnected() {
		return
	}
	i.Write([]byte("PRIVMSG #" + i.channel + " :" + message + "\r\n"))
}

// SendNotice sends the notice string to the specified user
func (i *Conn) SendNotice(user, message string) {
	if !i.IsConnected() {
		return
	}
	i.Write([]byte("NOTICE " + user + " :" + message + "\r\n"))
}

// JoinChannel joins the channel given by channel string
func (i *Conn) JoinChannel(channel string) {
	if !i.IsConnected() {
		return
	}
	i.channel = channel
	i.Write([]byte("JOIN #" + channel + "\r\n"))
}

// ChangeNick sends a NICK command and records the new name on the connection.
//
// PotatoStack v5.4.1: used by core.Join when the server answers 432/433/436
// during registration (the configured nickname is already held by another
// session). The field is written while Join still owns the connection
// exclusively - before any reader goroutine or hub registration exists - so the
// Username readers (UI connection detail, IRC log file name, GET /stats) never
// race with it.
func (i *Conn) ChangeNick(nick string) {
	if !i.IsConnected() || nick == "" {
		return
	}
	i.Write([]byte("NICK " + nick + "\r\n"))
	i.Username = nick
}

// GetUsers sends a NAMES request to the channel
func (i *Conn) GetUsers(channel string) {
	if !i.IsConnected() {
		return
	}
	i.Write([]byte("NAMES #" + channel + "\r\n"))
}

// Pong sends a Pong to the server, often used after a PING request
func (i *Conn) Pong(server string) {
	if !i.IsConnected() {
		return
	}
	i.Write([]byte("PONG " + server + "\r\n"))
}

// IsConnected returns true if the connection is not null
func (i *Conn) IsConnected() bool {
	return i.Conn != nil
}
