package roq

import (
	"github.com/mengelbart/qlog"
	"github.com/quic-go/quic-go/quicvarint"
)

type RTPSendStream struct {
	stream      SendStream
	flowID      uint64
	flowIDBytes []byte
	sentFlowID  bool
	buffer      []byte
	qlog        *qlog.Logger
}

func newRTPSendStream(stream SendStream, flowID uint64, flowIDBytes []byte, qlog *qlog.Logger) (*RTPSendStream, error) {
	return &RTPSendStream{
		stream:      stream,
		flowID:      flowID,
		flowIDBytes: flowIDBytes,
		sentFlowID:  false,
		buffer:      make([]byte, 0, 65536),
		qlog:        qlog,
	}, nil
}

// WriteRTPBytes sends an RTP or RTCP packet on the stream.
func (s *RTPSendStream) WriteRTPBytes(packet []byte) (int, error) {
	s.buffer = s.buffer[0:0]
	if !s.sentFlowID {
		s.buffer = append(s.buffer, s.flowIDBytes...)
		s.sentFlowID = true
	}
	s.buffer = quicvarint.Append(s.buffer, uint64(len(packet)))
	s.buffer = append(s.buffer, packet...)
	_, err := s.stream.Write(s.buffer)
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
