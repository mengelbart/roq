package roq

import (
	"bytes"
	"context"
	"io"
	"log"
	"sync"
	"time"

	"github.com/mengelbart/qlog"
	roqqlog "github.com/mengelbart/qlog/roq"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/quicvarint"
)

type ReceiveFlow struct {
	id         uint64
	buffer     chan *bytes.Buffer
	bufferPool sync.Pool
	ctx        context.Context
	cancelCtx  context.CancelFunc
	lock       sync.Mutex
	streams    map[int64]ReceiveStream
	qlog       *qlog.Logger
}

func newReceiveFlow(id uint64, receiveBufferSize int, qlog *qlog.Logger) *ReceiveFlow {
	ctx, cancel := context.WithCancel(context.Background())
	return &ReceiveFlow{
		id:     id,
		buffer: make(chan *bytes.Buffer, receiveBufferSize),
		bufferPool: sync.Pool{
			New: func() any {
				return bytes.NewBuffer(make([]byte, 65535))
			},
		},
		ctx:       ctx,
		cancelCtx: cancel,
		lock:      sync.Mutex{},
		streams:   map[int64]ReceiveStream{},
		qlog:      qlog,
	}
}

func (f *ReceiveFlow) push(packet *bytes.Buffer) {
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
		r := io.LimitReader(reader, int64(length))
		b := f.bufferPool.Get().(*bytes.Buffer)
		b.Reset()
		n, err := b.ReadFrom(r)
		if err != nil {
			streamErr, ok := err.(*quic.StreamError)
			if ok {
				log.Printf("got stream error: %v", streamErr)
			}
			log.Printf("got unexpected error after reading %v bytes of payload: %v", n, err)
			return
		}
		if f.qlog != nil {
			raw := make([]byte, b.Len())
			n := copy(raw, b.Bytes())
			f.qlog.Log(roqqlog.StreamPacketEvent{
				EventName: roqqlog.StreamPacketEventTypeParsed,
				StreamID:  rs.ID(),
				Packet: roqqlog.Packet{
					FlowID: f.id,
					Length: length,
					Raw: &qlog.RawInfo{
						Length:        length,
						PayloadLength: length,
						Data:          raw[:n],
					},
				},
			})
		}
		f.push(b)
	}
}

func (f *ReceiveFlow) Read(buf []byte) (int, error) {
	select {
	case packet := <-f.buffer:
		n := copy(buf, packet.Bytes())
		f.bufferPool.Put(packet)
		return n, nil
	case <-f.ctx.Done():
		return 0, f.ctx.Err()
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
