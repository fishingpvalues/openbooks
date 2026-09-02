package mock

import (
	"io"
	"log"
	"net"
	"os"
	"time"
)

type DccServer struct {
	Port   string
	Reader io.ReadSeeker
	log    *log.Logger

	// ActualPort is the port the listener bound to, set by Start. It
	// differs from Port only when Port requests an ephemeral port (":0"),
	// which is what the tests use so the mock never collides with a
	// service that happens to hold a fixed port (6969 is whisparr on the
	// stack, and the old test hard-coded it).
	ActualPort int
}

func (dcc *DccServer) Start(ready chan<- struct{}) {
	dcc.log = log.New(os.Stdout, "MOCK DCC: ", 0)

	server, err := net.Listen("tcp", dcc.Port)
	if err != nil {
		panic(err)
	}
	dcc.ActualPort = server.Addr().(*net.TCPAddr).Port
	dcc.log.Println("Listening on " + dcc.Port)
	ready <- struct{}{}

	for {
		conn, err := server.Accept()
		if err != nil {
			panic(err)
		}
		go dcc.handler(conn)
	}
}

func (dcc *DccServer) handler(conn net.Conn) {
	defer func() {
		dcc.Reader.Seek(0, io.SeekStart)
		dcc.log.Println("closing connection")
		conn.Close()
	}()

	dcc.log.Println("Received a connection...")

	var err error
	var n int
	// n, err := io.Copy(conn, dcc.Reader)

	// Use below to slow download speed for testing
	bytes := make([]byte, 4096)
	for {
		n, _ = dcc.Reader.Read(bytes)
		_, err = conn.Write(bytes[:n])

		time.Sleep(time.Millisecond * 250)

		if n == 0 {
			break
		}
	}

	if err != nil {
		dcc.log.Println(err)
	} else {
		dcc.log.Printf("Done copying %d bytes\n", n)
	}
}

type WriteCloser struct {
	Data []byte
}

func (m *WriteCloser) Write(p []byte) (n int, err error) {
	m.Data = append(m.Data, p...)
	return len(p), nil
}

func (m WriteCloser) Close() error {
	return nil
}
