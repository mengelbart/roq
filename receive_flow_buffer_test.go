package roq

import (
	"sync"
	"testing"
)

func TestReceiveFlowBufferPopRemovesFlow(t *testing.T) {
	b := newReceiveFlowBuffer(16)
	f := b.getOrCreate(1, 1, nil, nil)

	if got := b.pop(1); got != f {
		t.Fatalf("pop returned %v, want %v", got, f)
	}
	if len(b.queue) != 0 {
		t.Errorf("queue has %d entries after pop, want 0", len(b.queue))
	}
	if len(b.buffer) != 0 {
		t.Errorf("buffer has %d entries after pop, want 0", len(b.buffer))
	}
	if got := b.pop(1); got != nil {
		t.Errorf("second pop returned %v, want nil", got)
	}
	if got := b.getOrCreate(1, 1, nil, nil); got == f {
		t.Error("getOrCreate returned the popped flow, want a new one")
	}
}

// A flow that was popped is owned by the application and must never be
// evicted, no matter how many unknown flow IDs arrive afterwards.
func TestReceiveFlowBufferPoppedFlowIsNotEvicted(t *testing.T) {
	const maxLen = 16
	b := newReceiveFlowBuffer(maxLen)
	f := b.getOrCreate(1, 1, nil, nil)

	rs := &stubReceiveStream{id: 1, cancelled: make(chan struct{})}
	f.streams[rs.ID()] = rs

	if got := b.pop(1); got != f {
		t.Fatalf("pop returned %v, want %v", got, f)
	}
	for i := range uint64(maxLen + 1) {
		b.getOrCreate(100+i, 1, nil, nil)
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

// Concurrent packets for the same unknown flow ID must end up on one flow:
// with a lookup and a create that are not atomic, the loser's flow is dropped
// and everything pushed into it is lost.
func TestReceiveFlowBufferGetOrCreateIsAtomic(t *testing.T) {
	const routines = 8
	b := newReceiveFlowBuffer(16)
	flows := make([]*ReceiveFlow, routines)
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for i := range routines {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			flows[i] = b.getOrCreate(1, 1, nil, nil)
		}()
	}
	start.Done()
	done.Wait()

	for i, f := range flows {
		if f != flows[0] {
			t.Fatalf("goroutine %d got flow %p, goroutine 0 got %p, want the same flow", i, f, flows[0])
		}
	}
	if len(b.queue) != 1 {
		t.Fatalf("queue has %d entries, want 1", len(b.queue))
	}
}
