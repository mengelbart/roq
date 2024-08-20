package roq

import (
	"context"
	"errors"
	"io"
	"log"
	"sync"
	"time"

	"github.com/mengelbart/qlog"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/quicvarint"
)

type ReceiveFlow struct {
	id        uint64
	buffer    chan []byte
	ctx       context.Context
	cancelCtx context.CancelFunc
	lock      sync.Mutex
	streams   map[int64]ReceiveStream
	qlog      *qlog.Logger
}

func newReceiveFlow(id uint64, receiveBufferSize int, qlog *qlog.Logger) *ReceiveFlow {
	ctx, cancel := context.WithCancel(context.Background())
	return &ReceiveFlow{
		id:        id,
		buffer:    make(chan []byte, receiveBufferSize),
		ctx:       ctx,
		cancelCtx: cancel,
		lock:      sync.Mutex{},
		streams:   map[int64]ReceiveStream{},
		qlog:      qlog,
	}
}

func (f *ReceiveFlow) push(packet []byte) {
	select {
	case f.buffer <- packet:
	case <-f.ctx.Done():
	default:
	}
}

func (f *ReceiveFlow) readStream(rs ReceiveStream) {
	select {
	case <-f.ctx.Done():
		rs.CancelRead(ErrRoQNoError)
		return
	default:
	}
	f.lock.Lock()
	f.streams[rs.ID()] = rs
	f.lock.Unlock()

	reader := quicvarint.NewReader(rs)
	for {
		length, err := quicvarint.Read(reader)
		if err != nil {
			if err == io.EOF {
				return
			}
			streamErr, ok := err.(*quic.StreamError)
			if ok {
				log.Printf("got stream error: %v", streamErr)
			}
			log.Printf("got unexpected error while reading length: %v", err)
			return
		}
		buf := make([]byte, length)
		n, err := io.ReadFull(reader, buf)
		if err != nil {
			streamErr, ok := err.(*quic.StreamError)
			if ok {
				log.Printf("got stream error: %v", streamErr)
			}
			log.Printf("got unexpected error after reading %v bytes of payload: %v", n, err)
			return
		}
		if f.qlog != nil {
			f.qlog.RoQStreamPacketParsed(f.id, rs.ID(), int(length))
		}
		f.push(buf[:n])
	}
}

func (f *ReceiveFlow) Read(buf []byte) (int, error) {
	select {
	case packet := <-f.buffer:
		n := copy(buf, packet)
		return n, nil
	case <-f.ctx.Done():
		return 0, f.ctx.Err()
	case <-time.After(time.Second):
		// TODO: Implement real deadline
		return 0, errors.New("deadline exceeded")
	}
}

func (f *ReceiveFlow) SetReadDeadline(t time.Time) error {
	// TODO
	return nil
}

func (f *ReceiveFlow) ID() uint64 {
	return f.id
}

func (f *ReceiveFlow) closeWithError(code uint64) {
	for _, s := range f.streams {
		s.CancelRead(code)
	}
}

func (f *ReceiveFlow) Close() error {
	f.cancelCtx()
	f.closeWithError(ErrRoQNoError)
	return nil
}
