package roq

import (
	"context"
	"sync"

	"github.com/mengelbart/qlog"
	"github.com/quic-go/quic-go/quicvarint"
)

type prioritySetter interface {
	SetPriority(uint32)
	SetIncremental(bool)
}

type SendFlow struct {
	lock      sync.Mutex
	id        uint64
	conn      Connection
	flowID    []byte
	streams   []*RTPSendStream
	priority  uint32
	onClose   func()
	closedErr error
	qlog      *qlog.Logger
}

func newFlow(conn Connection, id uint64, priority uint32, onClose func(), qlog *qlog.Logger) *SendFlow {
	flowID := make([]byte, 0, quicvarint.Len(id))
	flowID = quicvarint.Append(flowID, id)
	return &SendFlow{
		lock:      sync.Mutex{},
		id:        id,
		conn:      conn,
		flowID:    flowID,
		streams:   []*RTPSendStream{},
		priority:  priority,
		onClose:   onClose,
		closedErr: nil,
		qlog:      qlog,
	}
}

// WriteRTP sends an RTP or RTCP packet as a QUIC Datagram.
func (f *SendFlow) WriteRTPBytes(packet []byte) error {
	if err := f.isClosed(); err != nil {
		return err
	}
	buf := make([]byte, 0, len(f.flowID)+len(packet))
	buf = append(buf, f.flowID...)
	buf = append(buf, packet...)
	if f.qlog != nil {
		f.qlog.RoQDatagramPacketCreated(f.id, len(packet))
	}
	return f.conn.SendDatagram(buf)
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
	if ps, ok := s.(prioritySetter); ok {
		ps.SetPriority(f.priority)
		ps.SetIncremental(true)
	}
	stream, err := newRTPSendStream(s, f.id, f.flowID, f.qlog)
	if err != nil {
		return nil, err
	}
	if f.qlog != nil {
		f.qlog.RoQStreamOpened(f.id, int64(s.StreamID()))
	}
	f.lock.Lock()
	defer f.lock.Unlock()
	f.streams = append(f.streams, stream)
	return stream, nil
}

func (f *SendFlow) isClosed() error {
	f.lock.Lock()
	defer f.lock.Unlock()
	return f.closedErr
}

// Close closes the flow and all associated streams.
func (f *SendFlow) Close() error {
	f.lock.Lock()
	defer f.lock.Unlock()
	for _, s := range f.streams {
		if err := s.Close(); err != nil {
			return err
		}
	}
	f.onClose()
	f.closedErr = errClosed
	return nil
}
