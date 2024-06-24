package roq

import (
	"github.com/mengelbart/qlog"
	"github.com/quic-go/quic-go/quicvarint"
)

type RTPSendStream struct {
	stream SendStream
	flowID uint64
	qlog   *qlog.Logger
}

func newRTPSendStream(stream SendStream, flowID uint64, qlog *qlog.Logger) (*RTPSendStream, error) {
	return &RTPSendStream{
		stream: stream,
		flowID: flowID,
		qlog:   qlog,
	}, nil
}

// WriteRTPBytes sends an RTP or RTCP packet on the stream.
func (s *RTPSendStream) WriteRTPBytes(packet []byte) (int, error) {
	length := quicvarint.Len(uint64(len(packet)))
	buf := make([]byte, 0, uint64(length)+uint64(len(packet)))
	buf = quicvarint.Append(buf, uint64(len(packet)))
	buf = append(buf, packet...)
	_, err := s.stream.Write(buf)
	if s.qlog != nil {
		s.qlog.RoQStreamPacketCreated(s.flowID, s.stream.ID(), len(packet))
	}
	return len(packet), err
}

// CancelStream closes the stream with the given error code.
func (s *RTPSendStream) CancelStream(errorCode uint64) {
	s.stream.CancelWrite(errorCode)
}

// Close closes the stream gracefully.
func (s *RTPSendStream) Close() error {
	return s.stream.Close()
}
