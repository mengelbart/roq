package roq

import (
	"testing"
)

func TestReceiveFlowBufferPopRemovesFlow(t *testing.T) {
	b := newReceiveFlowBuffer(16)
	f := newReceiveFlow(1, 1, nil)
	b.add(f)

	if got := b.pop(1); got != f {
		t.Fatalf("pop returned %v, want %v", got, f)
	}
	if len(b.queue) != 0 {
		t.Errorf("queue has %d entries after pop, want 0", len(b.queue))
	}
	if len(b.buffer) != 0 {
		t.Errorf("buffer has %d entries after pop, want 0", len(b.buffer))
	}
	if got := b.get(1); got != nil {
		t.Errorf("get returned %v after pop, want nil", got)
	}
	if got := b.pop(1); got != nil {
		t.Errorf("second pop returned %v, want nil", got)
	}
}

// A flow that was popped is owned by the application and must never be
// evicted, no matter how many unknown flow IDs arrive afterwards.
func TestReceiveFlowBufferPoppedFlowIsNotEvicted(t *testing.T) {
	const maxLen = 16
	b := newReceiveFlowBuffer(maxLen)
	f := newReceiveFlow(1, 1, nil)
	b.add(f)

	rs := &stubReceiveStream{id: 1, cancelled: make(chan struct{})}
	f.streams[rs.ID()] = rs

	if got := b.pop(1); got != f {
		t.Fatalf("pop returned %v, want %v", got, f)
	}
	for i := range uint64(maxLen + 1) {
		b.add(newReceiveFlow(100+i, 1, nil))
	}

	select {
	case <-rs.cancelled:
		t.Error("stream of popped flow was cancelled by eviction")
	default:
	}
	if len(b.queue) > maxLen {
		t.Errorf("queue has %d entries, want at most %d", len(b.queue), maxLen)
	}
	if len(b.buffer) != len(b.queue) {
		t.Errorf("buffer has %d entries, queue has %d, want equal", len(b.buffer), len(b.queue))
	}
}
