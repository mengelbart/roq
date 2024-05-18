package roq

import (
	"context"
	"errors"
	"sync"

	"github.com/quic-go/quic-go/quicvarint"
)

type SendFlow struct {
	lock    sync.Mutex
	id      uint64
	conn    Connection
	flowID  []byte
	scratch []byte
	streams []*RTPSendStream
	ctx     context.Context
	cancel  context.CancelFunc
	onClose func()
}

func newFlow(conn Connection, id uint64, onClose func()) *SendFlow {
	flowID := make([]byte, 0, quicvarint.Len(id))
	flowID = quicvarint.Append(flowID, id)
	scratch := make([]byte, 1500)
	scratch = append(scratch, flowID...)
	ctx, cancel := context.WithCancel(context.Background())
	return &SendFlow{
		lock:    sync.Mutex{},
		id:      id,
		conn:    conn,
		flowID:  flowID,
		scratch: scratch,
		streams: []*RTPSendStream{},
		ctx:     ctx,
		cancel:  cancel,
		onClose: onClose,
	}
}

// WriteRTP sends an RTP or RTCP packet as a QUIC Datagram.
func (f *SendFlow) WriteRTPBytes(packet []byte) error {
	if err := f.isClosed(); err != nil {
		return err
	}
	n := copy(f.scratch[len(f.flowID):], packet)
	if n < len(packet) {
		return errors.New("copied unexpected number of bytes")
	}
	return f.conn.SendDatagram(f.scratch[:n+len(f.flowID)])
}

// NewSendStream creates a new Stream for sending outgoing RTP and RTCP packets
// over a QUIC stream.
func (f *SendFlow) NewSendStream(ctx context.Context) (*RTPSendStream, error) {
	if err := f.isClosed(); err != nil {
		return nil, err
	}
	s, err := f.conn.OpenUniStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	stream, err := newRTPSendStream(s, f.flowID)
	if err != nil {
		return nil, err
	}
	f.lock.Lock()
	defer f.lock.Unlock()
	f.streams = append(f.streams, stream)
	return stream, nil
}

func (f *SendFlow) isClosed() error {
	select {
	case <-f.ctx.Done():
		return f.ctx.Err()
	default:
		return nil
	}
}

// Close closes the flow and all associated streams.
func (f *SendFlow) Close() error {
	f.cancel()
	f.lock.Lock()
	defer f.lock.Unlock()
	for _, s := range f.streams {
		if err := s.Close(); err != nil {
			return err
		}
	}
	f.onClose()
	return nil
}
