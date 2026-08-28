package roq

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// A stream that fails to close must not abort the rest of Close: the remaining
// streams still get closed, the flow is marked closed and removed from the
// session, and every error is reported.
func TestSendFlowCloseContinuesAfterStreamError(t *testing.T) {
	closeErr := errors.New("stream close failed")
	c := newStubConn()
	c.sendCloseErr = closeErr
	s, err := NewSession(c, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	f, err := s.NewSendFlow(1)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := f.NewSendStream(context.Background(), 0, false); err != nil {
			t.Fatal(err)
		}
	}

	err = f.Close()
	if !errors.Is(err, closeErr) {
		t.Errorf("Close() = %v, want an error wrapping %v", err, closeErr)
	}

	c.mutex.Lock()
	streams := c.sendStreams
	c.mutex.Unlock()
	if len(streams) != 3 {
		t.Fatalf("connection opened %d streams, want 3", len(streams))
	}
	for i, st := range streams {
		if !st.isClosed() {
			t.Errorf("stream %d was not closed", i)
		}
	}

	if err := f.isClosed(); err == nil {
		t.Error("flow is still open after Close")
	}
	if _, ok := s.sendFlows.get(1); ok {
		t.Error("flow was not removed from the session after Close")
	}
}

// prioritySendStream is a SendStream that supports stream priorities, like the
// streams of a Connection implementation built on a QUIC stack that has them.
type prioritySendStream struct {
	*stubSendStream
	mutex       sync.Mutex
	priority    uint32
	incremental bool
}

func (s *prioritySendStream) SetPriority(p uint32) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.priority = p
}

func (s *prioritySendStream) SetIncremental(b bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.incremental = b
}

func (s *prioritySendStream) get() (uint32, bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.priority, s.incremental
}

// priorityConn opens send streams that implement PrioritySendStream.
type priorityConn struct {
	*stubConn
	stream *prioritySendStream
}

func (c *priorityConn) OpenUniStreamSync(context.Context) (SendStream, error) {
	return c.stream, nil
}

// The priority and incremental arguments of NewSendStream are passed on if the
// connection opens streams that implement PrioritySendStream, and ignored if
// it does not.
func TestNewSendStreamPriority(t *testing.T) {
	ps := &prioritySendStream{stubSendStream: &stubSendStream{id: 1}}
	c := &priorityConn{stubConn: newStubConn(), stream: ps}
	f := newFlow(c, 1, func() {}, nil, nil)
	if _, err := f.NewSendStream(context.Background(), 7, true); err != nil {
		t.Fatal(err)
	}
	if priority, incremental := ps.get(); priority != 7 || !incremental {
		t.Errorf("priority = %v, incremental = %v, want 7, true", priority, incremental)
	}

	// A stream without priority support must not keep NewSendStream from
	// returning a usable stream.
	plain := newStubConn()
	f = newFlow(plain, 1, func() {}, nil, nil)
	if _, err := f.NewSendStream(context.Background(), 7, true); err != nil {
		t.Fatal(err)
	}
}
