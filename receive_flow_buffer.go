package roq

import (
	"slices"
	"sync"

	"github.com/mengelbart/qlog"
)

type receiveFlowBuffer struct {
	maxLen int
	mutex  sync.Mutex
	buffer map[uint64]*ReceiveFlow
	queue  []uint64
}

func newReceiveFlowBuffer(maxLen uint) *receiveFlowBuffer {
	b := &receiveFlowBuffer{
		maxLen: int(maxLen),
		mutex:  sync.Mutex{},
		buffer: map[uint64]*ReceiveFlow{},
		queue:  []uint64{},
	}
	return b
}

// getOrCreate returns the buffered flow for id, creating and buffering a new
// flow if the buffer holds none. Lookup and creation happen under a single
// lock hold, so concurrent callers for the same unknown flow ID all get the
// same flow.
func (b *receiveFlowBuffer) getOrCreate(id uint64, receiveBufferSize int, qlogger *qlog.Logger) *ReceiveFlow {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	if f, ok := b.buffer[id]; ok {
		return f
	}
	for len(b.queue) >= b.maxLen {
		front := b.queue[0]
		flow, ok := b.buffer[front]
		if ok {
			flow.closeWithError(ErrRoQUnknownFlowID)
		}
		delete(b.buffer, front)
		b.queue = b.queue[1:]
	}
	f := newReceiveFlow(id, receiveBufferSize, qlogger)
	b.queue = append(b.queue, id)
	b.buffer[id] = f
	return f
}

// pop removes the flow with the given ID from the buffer and returns it, or
// nil if the buffer holds no such flow. The returned flow is no longer subject
// to eviction.
func (b *receiveFlowBuffer) pop(id uint64) *ReceiveFlow {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	i := slices.IndexFunc(b.queue, func(f uint64) bool {
		return f == id
	})
	if i >= 0 {
		b.queue = slices.Delete(b.queue, i, i+1)
	}
	f, ok := b.buffer[id]
	if !ok {
		return nil
	}
	delete(b.buffer, id)
	return f
}
