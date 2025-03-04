package roq

import (
	"context"
	"sync"

	"github.com/mengelbart/qlog"
	roqqlog "github.com/mengelbart/qlog/roq"
	"github.com/quic-go/quic-go/quicvarint"
)

type SendFlow struct {
	lock      sync.Mutex
	id        uint64
	conn      Connection
	flowID    []byte
	streams   []*RTPSendStream
	onClose   func()
	closedErr error
	qlog      *qlog.Logger
}

func newFlow(conn Connection, id uint64, onClose func(), qlog *qlog.Logger) *SendFlow {
	flowID := make([]byte, 0, quicvarint.Len(id))
	flowID = quicvarint.Append(flowID, id)
	return &SendFlow{
		lock:      sync.Mutex{},
		id:        id,
		conn:      conn,
		flowID:    flowID,
		streams:   []*RTPSendStream{},
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
		raw := make([]byte, len(buf))
		m := copy(raw, buf)
		f.qlog.Log(roqqlog.DatagramPacketEvent{
			Type: roqqlog.DatagramPacketEventTypeCreated,
			Packet: roqqlog.Packet{
				FlowID: f.id,
				Length: uint64(len(buf)),
				Raw: &qlog.RawInfo{
					Length:        uint64(m),
					PayloadLength: uint64(m),
					Data:          raw,
				},
			},
		})
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
	stream, err := newRTPSendStream(s, f.id, f.flowID, f.qlog)
	if err != nil {
		return nil, err
	}
	if f.qlog != nil {
		f.qlog.Log(roqqlog.StreamOpenedEvent{
			FlowID:   f.id,
			StreamID: uint64(s.ID()),
		})
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
