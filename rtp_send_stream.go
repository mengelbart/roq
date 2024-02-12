package roq

import (
	"github.com/quic-go/quic-go/quicvarint"
)

type RTPSendStream struct {
	stream SendStream
	flowID []byte
}

func newRTPSendStream(stream SendStream, flowID []byte) *RTPSendStream {
	return &RTPSendStream{
		stream: stream,
		flowID: flowID,
	}
}

// WriteRTPBytes sends an RTP or RTCP packet on the stream.
func (s *RTPSendStream) WriteRTPBytes(packet []byte) (int, error) {
	length := quicvarint.Len(uint64(len(packet)))
	buf := make([]byte, 0, uint64(length)+uint64(len(packet)))
	buf = append(buf, s.flowID...)
	buf = append(buf, packet...)
	return s.stream.Write(buf)
}

// CancelStream closes the stream with the given error code.
func (s *RTPSendStream) CancelStream(errorCode uint64) {
	s.stream.CancelWrite(errorCode)
}

// Close closes the stream gracefully.
func (s *RTPSendStream) Close() error {
	return s.stream.Close()
}
