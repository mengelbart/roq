package roq

import (
	"context"
	"errors"
	"testing"
)

// A stream that fails to close must not abort the rest of Close: the remaining
// streams still get closed, the flow is marked closed and removed from the
// session, and every error is reported.
func TestSendFlowCloseContinuesAfterStreamError(t *testing.T) {
	closeErr := errors.New("stream close failed")
	c := newStubConn()
	c.sendCloseErr = closeErr
	s, err := NewSession(c, true, nil)
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
