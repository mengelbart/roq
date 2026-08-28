package roq

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// stubSendStream is a SendStream that records writes and closes. writeFn, when
// set, decides what each Write accepts; by default everything is accepted.
type stubSendStream struct {
	id       int64
	closeErr error
	writeFn  func([]byte) (int, error)
	mutex    sync.Mutex
	closed   bool
	written  [][]byte
}

func (s *stubSendStream) Write(p []byte) (int, error) {
	n, err := len(p), error(nil)
	if s.writeFn != nil {
		n, err = s.writeFn(p)
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	// The caller reuses its buffer across calls, so the bytes must be copied.
	s.written = append(s.written, append([]byte{}, p[:max(min(n, len(p)), 0)]...))
	return n, err
}

// wire returns every byte the stream accepted, in the order it accepted them.
func (s *stubSendStream) wire() []byte {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	var out []byte
	for _, w := range s.written {
		out = append(out, w...)
	}
	return out
}

// writes returns the number of Write calls made on the stream.
func (s *stubSendStream) writes() int {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return len(s.written)
}

func (s *stubSendStream) ID() int64          { return s.id }
func (s *stubSendStream) CancelWrite(uint64) {}
func (s *stubSendStream) Close() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.closed = true
	return s.closeErr
}

func (s *stubSendStream) isClosed() bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.closed
}

// stubReceiveStream feeds a fixed payload. Once it is exhausted the stream
// either ends (eof) or stays open until CancelRead.
type stubReceiveStream struct {
	id        int64
	data      []byte
	eof       bool
	cancelled chan struct{}
	once      sync.Once

	// parked is closed once the reader blocks, which happens only after
	// readStream has registered the stream on its flow.
	parked     chan struct{}
	parkedOnce sync.Once
}

func (s *stubReceiveStream) ID() int64 { return s.id }

func (s *stubReceiveStream) CancelRead(uint64) {
	s.once.Do(func() { close(s.cancelled) })
}

func (s *stubReceiveStream) Read(p []byte) (int, error) {
	if len(s.data) > 0 {
		n := copy(p, s.data)
		s.data = s.data[n:]
		return n, nil
	}
	if s.eof {
		return 0, io.EOF
	}
	if s.parked != nil {
		s.parkedOnce.Do(func() { close(s.parked) })
	}
	<-s.cancelled
	return 0, errors.New("stream cancelled")
}

// stubConn is an in-memory Connection driven by the test.
type stubConn struct {
	datagrams chan []byte
	streams   chan ReceiveStream

	// sendCloseErr is returned by every send stream this connection opens.
	sendCloseErr error

	mutex       sync.Mutex
	sendStreams []*stubSendStream
	closeCount  int
	closeCode   uint64
	closeOnce   sync.Once
	closed      chan struct{}
}

func newStubConn() *stubConn {
	return &stubConn{
		datagrams: make(chan []byte, 8),
		streams:   make(chan ReceiveStream, 8),
		closed:    make(chan struct{}),
	}
}

func (c *stubConn) SendDatagram([]byte) error { return nil }

func (c *stubConn) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	select {
	case d := <-c.datagrams:
		return d, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *stubConn) OpenUniStreamSync(context.Context) (SendStream, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	s := &stubSendStream{id: int64(len(c.sendStreams) + 1), closeErr: c.sendCloseErr}
	c.sendStreams = append(c.sendStreams, s)
	return s, nil
}

func (c *stubConn) AcceptUniStream(ctx context.Context) (ReceiveStream, error) {
	select {
	case s := <-c.streams:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *stubConn) CloseWithError(code uint64, _ string) error {
	c.mutex.Lock()
	c.closeCount++
	c.closeCode = code
	c.mutex.Unlock()
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

// awaitClose blocks until the session closes the connection on its own.
func (c *stubConn) awaitClose(t *testing.T) {
	t.Helper()
	select {
	case <-c.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("session never closed the connection")
	}
}

func (c *stubConn) closes() (int, uint64) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.closeCount, c.closeCode
}

// closeWithin fails the test if Close does not return before the timeout.
func closeWithin(t *testing.T, s *Session, timeout time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(timeout):
		t.Fatal("Close did not return: session deadlocked")
	}
}

// QUIC delivers datagrams intact, so a datagram with an unparseable flow ID is
// a protocol violation by the sender and closes the session.
func TestCloseAfterMalformedDatagram(t *testing.T) {
	c := newStubConn()
	s, err := NewSession(c, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.datagrams <- []byte{}
	c.awaitClose(t)

	closeWithin(t, s, 5*time.Second)

	n, code := c.closes()
	if n != 1 {
		t.Errorf("connection closed %d times, want 1", n)
	}
	if code != ErrRoQPacketError {
		t.Errorf("close code = %d, want ErrRoQPacketError (%d)", code, ErrRoQPacketError)
	}
}

// A stream that ends or is reset before delivering a flow ID is dropped: the
// peer cancelling one frame must not tear down the session.
func TestMalformedStreamHeaderIsDropped(t *testing.T) {
	c := newStubConn()
	s, err := NewSession(c, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A stream that ends before delivering a complete flow ID varint.
	rs := &stubReceiveStream{id: 3, eof: true, cancelled: make(chan struct{})}
	c.streams <- rs

	select {
	case <-rs.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("stream was never cancelled")
	}
	if n, _ := c.closes(); n != 0 {
		t.Errorf("connection closed %d times, want 0", n)
	}

	closeWithin(t, s, 5*time.Second)
}

// Close must tear down open send flows without deadlocking on the flow map.
func TestCloseWithOpenSendFlow(t *testing.T) {
	c := newStubConn()
	s, err := NewSession(c, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.NewSendFlow(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.NewSendStream(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}

	closeWithin(t, s, 5*time.Second)

	if err := f.isClosed(); err == nil {
		t.Error("send flow is still open after session close")
	}
}

// Close must tear down open receive flows and their streams.
func TestCloseWithOpenReceiveFlow(t *testing.T) {
	c := newStubConn()
	s, err := NewSession(c, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.NewReceiveFlow(1); err != nil {
		t.Fatal(err)
	}
	rs := &stubReceiveStream{
		id:        5,
		data:      []byte{0x01}, // flow ID 1, then the stream stays open
		cancelled: make(chan struct{}),
		parked:    make(chan struct{}),
	}
	c.streams <- rs

	// Wait until the stream is registered on the flow, otherwise Close may win
	// the race with the accept loop and the stream is never picked up at all.
	select {
	case <-rs.parked:
	case <-time.After(5 * time.Second):
		t.Fatal("stream was never read by the session")
	}

	closeWithin(t, s, 5*time.Second)

	select {
	case <-rs.cancelled:
	case <-time.After(time.Second):
		t.Error("receive stream was not cancelled by session close")
	}
}

// close is idempotent: the first code wins and the connection is closed once.
func TestCloseIsIdempotent(t *testing.T) {
	c := newStubConn()
	s, err := NewSession(c, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	closeWithin(t, s, 5*time.Second)
	closeWithin(t, s, 5*time.Second)

	n, _ := c.closes()
	if n != 1 {
		t.Errorf("connection closed %d times, want 1", n)
	}
	var sessErr SessionError
	if !errors.As(s.isClosed(), &sessErr) {
		t.Fatalf("isClosed() = %v, want SessionError", s.isClosed())
	}
	if sessErr.code != ErrRoQNoError {
		t.Errorf("close code = %d, want ErrRoQNoError", sessErr.code)
	}
}

// Concurrent Close calls must all return.
func TestConcurrentClose(t *testing.T) {
	c := newStubConn()
	s, err := NewSession(c, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = s.Close() }()
	}
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent Close deadlocked")
	}
	if n, _ := c.closes(); n != 1 {
		t.Errorf("connection closed %d times, want 1", n)
	}
}

// Operations after close must fail rather than block.
func TestNewFlowAfterClose(t *testing.T) {
	c := newStubConn()
	s, err := NewSession(c, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	closeWithin(t, s, 5*time.Second)

	if _, err := s.NewSendFlow(1); err == nil {
		t.Error("NewSendFlow succeeded on a closed session")
	}
	if _, err := s.NewReceiveFlow(1); err == nil {
		t.Error("NewReceiveFlow succeeded on a closed session")
	}
}
