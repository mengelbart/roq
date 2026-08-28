package integrationtests_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mengelbart/roq"
	roqqlog "github.com/mengelbart/roq/qlog"
	"github.com/quic-go/quic-go"
	quicqlog "github.com/quic-go/quic-go/qlog"
	"github.com/quic-go/quic-go/qlogwriter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestQlog runs a session over a real QUIC connection whose tracer carries the
// RoQ event schema, and checks that the RoQ events end up in the same sqlog file
// as the QUIC transport events.
func TestQlog(t *testing.T) {
	qlogDir := t.TempDir()
	t.Setenv("QLOGDIR", qlogDir)
	config := &quic.Config{EnableDatagrams: true, Tracer: roqqlog.DefaultConnectionTracer}

	listener, err := quic.ListenAddr("localhost:0", generateTLSConfig(), config)
	require.NoError(t, err)
	defer listener.Close() //nolint:errcheck

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type receiveEnd struct {
		session *roq.Session
		flow    *roq.ReceiveFlow
	}
	accepted := make(chan receiveEnd, 1)
	go func() {
		conn, err := listener.Accept(ctx)
		assert.NoError(t, err)
		s, err := roq.NewSession(roq.NewQUICGoConnection(conn), true)
		assert.NoError(t, err)
		f, err := s.NewReceiveFlow(0)
		assert.NoError(t, err)
		accepted <- receiveEnd{session: s, flow: f}
	}()

	conn, err := quic.DialAddr(ctx, listener.Addr().String(), generateTLSConfig(), config)
	require.NoError(t, err)
	sender, err := roq.NewSession(roq.NewQUICGoConnection(conn), true)
	require.NoError(t, err)

	// Only send once the peer is ready to receive, so that a datagram cannot be
	// dropped before its connection has finished the handshake.
	receiver := <-accepted

	f, err := sender.NewSendFlow(0)
	require.NoError(t, err)
	stream, err := f.NewSendStream(ctx, 0, false)
	require.NoError(t, err)
	_, err = stream.WriteRTPBytes(make([]byte, 100))
	require.NoError(t, err)
	require.NoError(t, f.WriteRTPBytes(make([]byte, 100)))

	buf := make([]byte, 2000)
	for i := 0; i < 2; i++ {
		_, err = receiver.flow.Read(buf)
		require.NoError(t, err)
	}

	// Closing the sessions releases their qlog producers, which is what closes
	// and flushes the sqlog files.
	require.NoError(t, sender.Close())
	require.NoError(t, receiver.session.Close())

	files, err := filepath.Glob(filepath.Join(qlogDir, "*.sqlog"))
	require.NoError(t, err)
	require.Len(t, files, 2)

	var sawRoQ, sawTransport bool
	for _, file := range files {
		header, names := readQlog(t, file)
		assert.Contains(t, header.Trace.EventSchemas, quicqlog.EventSchema)
		assert.Contains(t, header.Trace.EventSchemas, roqqlog.EventSchema)
		for _, name := range names {
			switch name {
			case "roq:stream_packet_created", "roq:datagram_packet_parsed":
				sawRoQ = true
			case "transport:packet_sent":
				sawTransport = true
			}
		}
	}
	assert.True(t, sawRoQ, "no RoQ events in the qlog files")
	assert.True(t, sawTransport, "no QUIC transport events in the qlog files")
}

type qlogHeader struct {
	Trace struct {
		EventSchemas []string `json:"event_schemas"`
	} `json:"trace"`
}

// readQlog parses a JSON-SEQ qlog file into its header record and the names of
// the events that follow.
func readQlog(t *testing.T, path string) (qlogHeader, []string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	require.NoError(t, err)

	var header qlogHeader
	var names []string
	scanner := bufio.NewScanner(bytes.NewReader(contents))
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	scanner.Split(splitRecords)
	for i := 0; scanner.Scan(); i++ {
		if i == 0 {
			require.NoError(t, json.Unmarshal(scanner.Bytes(), &header))
			continue
		}
		var event struct {
			Name string `json:"name"`
		}
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &event))
		names = append(names, event.Name)
	}
	require.NoError(t, scanner.Err())
	return header, names
}

// splitRecords splits a JSON-SEQ stream on its record separators.
func splitRecords(data []byte, atEOF bool) (int, []byte, error) {
	start := bytes.IndexByte(data, qlogwriter.RecordSeparator)
	if start < 0 {
		if atEOF {
			return len(data), nil, nil
		}
		return 0, nil, nil
	}
	if end := bytes.IndexByte(data[start+1:], qlogwriter.RecordSeparator); end >= 0 {
		return start + 1 + end, data[start+1 : start+1+end], nil
	}
	if atEOF {
		return len(data), data[start+1:], nil
	}
	return 0, nil, nil
}
