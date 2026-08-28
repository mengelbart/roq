package roq

import (
	"bytes"
	"errors"
	"log"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/quic-go/quic-go/quicvarint"
)

// Without options a session uses the documented defaults.
func TestSessionDefaultKnobs(t *testing.T) {
	c := newStubConn()
	s, err := NewSession(c, true)
	if err != nil {
		t.Fatal(err)
	}
	defer closeWithin(t, s, 5*time.Second)

	f, err := s.NewReceiveFlow(1)
	if err != nil {
		t.Fatal(err)
	}
	if got := cap(f.buffer); got != defaultReceiveBufferSize {
		t.Errorf("receive flow buffers %d packets, want %d", got, defaultReceiveBufferSize)
	}
	if got := s.receiveFlowBuffer.maxLen; got != defaultUnknownFlowBufferSize {
		t.Errorf("unknown flow buffer holds %d flows, want %d", got, defaultUnknownFlowBufferSize)
	}
}

// WithReceiveBufferSize must reach the flows the application creates and the
// flows the session creates for unknown flow IDs.
func TestWithReceiveBufferSize(t *testing.T) {
	c := newStubConn()
	s, err := NewSession(c, true, WithReceiveBufferSize(4))
	if err != nil {
		t.Fatal(err)
	}
	defer closeWithin(t, s, 5*time.Second)

	f, err := s.NewReceiveFlow(1)
	if err != nil {
		t.Fatal(err)
	}
	if got := cap(f.buffer); got != 4 {
		t.Errorf("receive flow buffers %d packets, want 4", got)
	}

	// A datagram for a flow ID the application has not claimed yet creates a
	// buffered flow, which must be sized the same way.
	c.datagrams <- append(quicvarint.Append(nil, 2), 0x42)
	unknown := awaitBufferedFlow(t, s, 2)
	if got := cap(unknown.buffer); got != 4 {
		t.Errorf("buffered flow holds %d packets, want 4", got)
	}
}

// WithUnknownFlowBufferSize must bound how many unclaimed flows are buffered.
func TestWithUnknownFlowBufferSize(t *testing.T) {
	c := newStubConn()
	s, err := NewSessionWithAppHandledConn(c, true, WithUnknownFlowBufferSize(2))
	if err != nil {
		t.Fatal(err)
	}
	defer closeWithin(t, s, 5*time.Second)

	for id := range uint64(4) {
		s.HandleDatagram(append(quicvarint.Append(nil, id), 0x42))
	}

	s.receiveFlowBuffer.mutex.Lock()
	defer s.receiveFlowBuffer.mutex.Unlock()
	if got := len(s.receiveFlowBuffer.buffer); got != 2 {
		t.Errorf("session buffered %d unknown flows, want 2", got)
	}
	// The two oldest flow IDs must have been evicted, not the newest.
	for _, id := range []uint64{2, 3} {
		if _, ok := s.receiveFlowBuffer.buffer[id]; !ok {
			t.Errorf("flow %d was evicted, want the oldest flows evicted first", id)
		}
	}
}

// Invalid option values are rejected by both constructors rather than silently
// producing a session with a useless buffer.
func TestInvalidOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		opt  Option
	}{
		{"zero receive buffer", WithReceiveBufferSize(0)},
		{"negative receive buffer", WithReceiveBufferSize(-1)},
		{"zero unknown flow buffer", WithUnknownFlowBufferSize(0)},
		{"negative unknown flow buffer", WithUnknownFlowBufferSize(-1)},
		{"negative qlog packet data limit", WithQlogPacketData(-1)},
		{"nil logger", WithLogger(nil)},
		{"nil option", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewSession(newStubConn(), true, tc.opt); !errors.Is(err, errInvalidOption) {
				t.Errorf("NewSession error = %v, want errInvalidOption", err)
			}
			if _, err := NewSessionWithAppHandledConn(newStubConn(), true, tc.opt); !errors.Is(err, errInvalidOption) {
				t.Errorf("NewSessionWithAppHandledConn error = %v, want errInvalidOption", err)
			}
		})
	}
}

func TestWithLogger(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	c := newStubConn()
	s, err := NewSession(c, true, WithLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	defer closeWithin(t, s, 5*time.Second)

	rf, err := s.NewReceiveFlow(1)
	if err != nil {
		t.Fatal(err)
	}
	if rf.logger != logger {
		t.Error("receive flow does not use the logger passed with WithLogger")
	}
	sf, err := s.NewSendFlow(1)
	if err != nil {
		t.Fatal(err)
	}
	if sf.logger != logger {
		t.Error("send flow does not use the logger passed with WithLogger")
	}

	c.datagrams <- quicvarint.Append(nil, 2)
	if bf := awaitBufferedFlow(t, s, 2); bf.logger != logger {
		t.Error("buffered flow does not use the logger passed with WithLogger")
	}
}

func TestSessionDoesNotUseGlobalLoggerByDefault(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	defaultLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	defer slog.SetDefault(defaultLogger)

	s, err := NewSession(newStubConn(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer closeWithin(t, s, 5*time.Second)

	f, err := s.NewReceiveFlow(1)
	if err != nil {
		t.Fatal(err)
	}
	f.readStream(&stubReceiveStream{
		id:        1,
		readErr:   errors.New("boom"),
		cancelled: make(chan struct{}),
	})

	if logs.Len() != 0 {
		t.Errorf("session wrote %q to the global logger, want nothing", logs.String())
	}
}

// awaitBufferedFlow waits for the session to buffer a flow for an unclaimed
// flow ID and returns it.
func awaitBufferedFlow(t *testing.T, s *Session, id uint64) *ReceiveFlow {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		s.receiveFlowBuffer.mutex.Lock()
		f, ok := s.receiveFlowBuffer.buffer[id]
		s.receiveFlowBuffer.mutex.Unlock()
		if ok {
			return f
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("session never buffered a flow for ID %d", id)
	return nil
}
