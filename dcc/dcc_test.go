package dcc

import (
	"bytes"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/evan-buss/openbooks/mock"
)

// TestStringParsing makes sure that data is properly extracted from the DCC
// response string. (filename, IP conversion, port, and size)
func TestStringParsing(t *testing.T) {
	tables := []struct {
		search   string
		download *Download
	}{
		{
			":SearchOok!ook@only.ook PRIVMSG evan_28 :DCC SEND SearchOok_results_for__hp_lovecraft.txt.zip 1543751478 2043 784",
			&Download{Filename: "SearchOok_results_for__hp_lovecraft.txt.zip", IP: "92.3.199.54", Port: "2043", Size: 784},
		},
		{
			":Search!Search@ihw-4q5hcb.dyn.suddenlink.net PRIVMSG evan_bot :DCC SEND SearchBot_results_for__stephen_king_the_stand.txt.zip 2907707975 4342 1116",
			&Download{Filename: "SearchBot_results_for__stephen_king_the_stand.txt.zip", IP: "173.80.26.71", Port: "4342", Size: 1116},
		},
		{
			`:DV8!HandyAndy@ihw-39fkft.ip-164-132-173.eu PRIVMSG negative-bishop-1 :DCC SEND "Douglas Adams - [HITCHHIKER'S GUIDE TO THE GALAXY & THE 01] - Hitchhiker's Guide to the Galaxy & The (v5.0) (EPUB).rar" 2760158537 2050 2321788`,
			&Download{Filename: "Douglas Adams - [HITCHHIKER'S GUIDE TO THE GALAXY & THE 01] - Hitchhiker's Guide to the Galaxy & The (v5.0) (EPUB).rar", IP: "164.132.173.73", Port: "2050", Size: 2321788},
		},
	}

	for _, table := range tables {
		download, err := ParseString(table.search)
		require.NoError(t, err)
		assert.Equal(t, table.download, download)
	}
}

func TestDownload(t *testing.T) {
	text := "Test dcc download content."

	// Bind the mock to an ephemeral port (":0") so the test never
	// collides with a service holding a fixed port (6969 is whisparr on
	// the stack). Dial the real port the OS assigned.
	server := mock.DccServer{
		Port:   ":0",
		Reader: bytes.NewReader([]byte(text)),
	}

	ready := make(chan struct{}, 1)
	go server.Start(ready)
	<-ready

	t.Logf("After server start (bound to :%d)", server.ActualPort)

	textDownload := Download{
		Filename: "test.txt",
		IP:       "localhost",
		Port:     strconv.Itoa(server.ActualPort),
		Size:     int64(len(text)),
	}

	received := new(mock.WriteCloser)
	err := textDownload.Download(received)
	require.NoError(t, err)
	assert.Equal(t, text, string(received.Data))
}
