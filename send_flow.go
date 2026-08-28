package roq

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/mengelbart/roq/qlog"
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
	qlog      *qlogger
	logger    *slog.Logger
}

func newFlow(conn Connection, id uint64, onClose func(), qlog *qlogger, logger *slog.Logger) *SendFlow {
	if logger == nil {
		logger = discardLogger()
	}
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
		logger:    logger,
	}
}

// WriteRTPBytes sends an RTP or RTCP packet as a QUIC Datagram.
func (f *SendFlow) WriteRTPBytes(packet []byte) error {
	if err := f.isClosed(); err != nil {
		return err
	}
	buf := make([]byte, 0, len(f.flowID)+len(packet))
	buf = append(buf, f.flowID...)
	buf = append(buf, packet...)
	if f.qlog != nil {
		f.qlog.record(qlog.DatagramPacketCreated{
			Packet: qlog.Packet{
				FlowID: f.id,
				Length: uint64(len(buf)),
				Raw: qlog.RawInfo{
					Length:        uint64(len(buf)),
					PayloadLength: uint64(len(packet)),
					Data:          f.qlog.rawData(buf),
				},
			},
		})
	}
	return f.conn.SendDatagram(buf)
}

// NewSendStream creates a new Stream for sending outgoing RTP and RTCP packets
// over a QUIC stream.
func (f *SendFlow) NewSendStream(ctx context.Context, priority uint32, incremantal bool) (*RTPSendStream, error) {
	if err := f.isClosed(); err != nil {
		return nil, err
	}
	s, err := f.conn.OpenUniStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	priorityStream, ok := s.(PrioritySendStream)
	if ok {
		f.logger.Debug("setting stream priority",
			"flowID", f.id, "streamID", s.ID(), "priority", priority, "incremental", incremantal)
		priorityStream.SetPriority(priority)
		priorityStream.SetIncremental(incremantal)
	}

	stream, err := newRTPSendStream(s, f.id, f.flowID, f.qlog)
	if err != nil {
		return nil, err
	}
	if f.qlog != nil {
		f.qlog.record(qlog.StreamOpened{
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

// Close closes the flow and all associated streams. Every stream is closed
// even if an earlier one failed to close; the errors are joined and returned.
// The flow is marked closed and removed from the session in either case.
func (f *SendFlow) Close() error {
	f.lock.Lock()
	defer f.lock.Unlock()
	errs := make([]error, 0, len(f.streams))
	for _, s := range f.streams {
		errs = append(errs, s.Close())
	}
	f.onClose()
	f.closedErr = errClosed
	return errors.Join(errs...)
}

func (f *SendFlow) ID() uint64 {
	return f.id
}
