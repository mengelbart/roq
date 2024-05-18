package roq

import (
	"slices"
	"sync"
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

func (b *receiveFlowBuffer) add(f *ReceiveFlow) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	for len(b.queue) >= b.maxLen {
		front := b.queue[0]
		flow, ok := b.buffer[front]
		if ok {
			flow.closeWithError(ErrRoQUnknownFlowID)
		}
		delete(b.buffer, front)
		b.queue = b.queue[1:]
	}
	b.queue = append(b.queue, f.ID())
	b.buffer[f.ID()] = f
}

func (b *receiveFlowBuffer) get(id uint64) *ReceiveFlow {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	f, ok := b.buffer[id]
	if !ok {
		return nil
	}
	return f
}

func (b *receiveFlowBuffer) pop(id uint64) *ReceiveFlow {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	i := slices.IndexFunc[[]uint64, uint64](b.queue, func(f uint64) bool {
		return f == id
	})
	if i >= 0 && i < len(b.queue) {
		slices.Delete[[]uint64, uint64](b.queue, i, i)
	}
	if f, ok := b.buffer[id]; ok {
		return f
	}
	return nil
}
