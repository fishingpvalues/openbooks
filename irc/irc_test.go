package irc

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func selfSignedCert(t *testing.T, org string) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{Organization: []string{org}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	cert := tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
	}
	sum := sha256.Sum256(block.Bytes)
	return cert, sum[:]
}

// startTLSListener runs a throwaway TLS server presenting cert on a
// random localhost port.
func startTLSListener(t *testing.T, cert tls.Certificate) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}})
			go func() {
				_ = tc.Handshake()
				c.Close()
			}()
		}
	}()
	return ln
}

// TestPinnedTLSConfig_KnownServerPins: the known irchighway host must yield a
// config that enforces the pinned fingerprint - the right cert connects,
// a different cert is refused.
func TestPinnedTLSConfig_KnownServerPins(t *testing.T) {
	cfg, err := pinnedTLSConfig("irc.irchighway.net:6697")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %v, want TLS1.2", cfg.MinVersion)
	}
	if cfg.VerifyPeerCertificate == nil {
		t.Fatal("expected VerifyPeerCertificate to be set for a pinned host")
	}

	// A different (but structurally similar) cert must be rejected by the
	// pin. We cannot dial the real server in a unit test, so we replay the
	// callback directly against a stand-in certificate.
	standIn, _ := selfSignedCert(t, "IRCHighway-lookalike")
	block, _ := pem.Decode(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: standIn.Certificate[0]}))
	if err := cfg.VerifyPeerCertificate([][]byte{standIn.Certificate[0]}, nil); err == nil {
		t.Fatal("pin accepted a different certificate - MITM would not be detected")
	} else if !strings.Contains(err.Error(), "not in the pinned set") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(block.Bytes) == 0 {
		t.Fatal("no DER")
	}
}

// TestPinnedTLSConfig_UnknownServerStrict: any host without a pinned
// fingerprint gets the strict default (no callback, no skip-verify).
func TestPinnedTLSConfig_UnknownServerStrict(t *testing.T) {
	cfg, err := pinnedTLSConfig("irc.example.org:6697")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VerifyPeerCertificate != nil {
		t.Fatal("unknown host must not get a pin callback (it should verify normally)")
	}
	if cfg.InsecureSkipVerify {
		t.Fatal("unknown host must not skip verification")
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %v, want TLS1.2", cfg.MinVersion)
	}
}

// TestConnect_PinnedAcceptsMatchingCert is an end-to-end check: a real TLS
// dial through pinnedTLSConfig succeeds only when the peer presents the
// pinned certificate.
func TestConnect_PinnedAcceptsMatchingCert(t *testing.T) {
	// Build a cert whose fingerprint we pin on the fly.
	cert, fingerprint := selfSignedCert(t, "IRCHighway")
	hexPin := hexEncode(fingerprint)
	ln := startTLSListener(t, cert)
	defer ln.Close()

	// Temporarily pin this test cert under the loopback host so the
	// config builder returns the pinning path.
	pinnedIRCCertFingerprints["127.0.0.1"] = []string{hexPin}
	defer delete(pinnedIRCCertFingerprints, "127.0.0.1")

	cfg, err := pinnedTLSConfig(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := tls.Dial("tcp", ln.Addr().String(), cfg)
	if err != nil {
		t.Fatalf("expected the pinned (matching) cert to connect, got: %v", err)
	}
	conn.Close()

	// Now swap in a DIFFERENT certificate on the same host: the pin must
	// refuse it.
	other, _ := selfSignedCert(t, "Attacker")
	pinnedIRCCertFingerprints["127.0.0.1"] = []string{hexPin} // pin stays at the ORIGINAL cert
	otherLn := startTLSListener(t, other)
	defer otherLn.Close()
	_, err = tls.Dial("tcp", otherLn.Addr().String(), cfg)
	if err == nil {
		t.Fatal("pin accepted a different certificate - MITM not detected")
	}
}

func hexEncode(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = digits[c>>4]
		out[i*2+1] = digits[c&0xf]
	}
	return string(out)
}

// TestPinnedSet_AcceptsAnyPinnedCert: the load-balanced irchighway case -
// several backend certs are pinned and the callback must accept any of
// them while refusing a fourth one.
func TestPinnedSet_AcceptsAnyPinnedCert(t *testing.T) {
	a, fpA := selfSignedCert(t, "IRCHighway-A")
	b, fpB := selfSignedCert(t, "IRCHighway-B")
	c, fpC := selfSignedCert(t, "IRCHighway-C")
	attacker, _ := selfSignedCert(t, "Attacker")

	pinnedIRCCertFingerprints["127.0.0.1"] = []string{hexEncode(fpA), hexEncode(fpB), hexEncode(fpC)}
	defer delete(pinnedIRCCertFingerprints, "127.0.0.1")

	cfg, err := pinnedTLSConfig("127.0.0.1:1234")
	if err != nil {
		t.Fatal(err)
	}
	for name, cert := range map[string]tls.Certificate{"A": a, "B": b, "C": c} {
		if err := cfg.VerifyPeerCertificate([][]byte{cert.Certificate[0]}, nil); err != nil {
			t.Fatalf("backend %s (pinned) rejected: %v", name, err)
		}
	}
	if err := cfg.VerifyPeerCertificate([][]byte{attacker.Certificate[0]}, nil); err == nil {
		t.Fatal("unpinned (attacker) cert accepted")
	}
}
