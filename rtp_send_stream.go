package roq

import (
	"github.com/mengelbart/qlog"
	roqqlog "github.com/mengelbart/qlog/roq"
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
	n, err := s.stream.Write(s.buffer)
	if s.qlog != nil {
		raw := make([]byte, len(s.buffer))
		m := copy(raw, s.buffer)
		s.qlog.Log(roqqlog.StreamPacketEvent{
			EventName: roqqlog.StreamPacketEventTypeCreated,
			StreamID:  s.stream.ID(),
			Packet: roqqlog.Packet{
				FlowID: s.flowID,
				Length: uint64(n),
				Raw: &qlog.RawInfo{
					Length:        uint64(m),
					PayloadLength: uint64(m),
					Data:          raw,
				},
			},
		})
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
