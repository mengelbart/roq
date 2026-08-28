package roq

import (
	"io"
	"sync"

	"github.com/mengelbart/roq/qlog"
	"github.com/quic-go/quic-go/quicvarint"
)

type RTPSendStream struct {
	stream      SendStream
	flowID      uint64
	flowIDBytes []byte
	qlog        *qlogger

	// lock guards the fields below, which are mutated by every write.
	lock       sync.Mutex
	sentFlowID bool
	buffer     []byte
	// pending is the tail of the last frame that stream.Write did not accept.
	// It has to reach the peer before any following packet does.
	pending []byte
}

func newRTPSendStream(stream SendStream, flowID uint64, flowIDBytes []byte, qlog *qlogger) (*RTPSendStream, error) {
	return &RTPSendStream{
		stream:      stream,
		flowID:      flowID,
		flowIDBytes: flowIDBytes,
		sentFlowID:  false,
		buffer:      make([]byte, 0, 65536),
		qlog:        qlog,
	}, nil
}

// WriteRTPBytes sends an RTP or RTCP packet on the stream. It usually doesn't
// make sense to call this method from multiple goroutines concurrently, but it
// is safe for concurrent use.
//
// The returned count is the number of bytes of packet that reached the stream,
// which is len(packet) on success. A frame is length-prefixed, so a partial
// write is recoverable: the bytes the peer has not seen are written first on
// the next call, before the next packet. Callers must therefore not re-send a
// packet that was reported short, or the peer receives it twice.
func (s *RTPSendStream) WriteRTPBytes(packet []byte) (int, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	// Nothing may go on the wire ahead of the tail of an earlier frame.
	if len(s.pending) > 0 {
		if err := s.flush(); err != nil {
			return 0, err
		}
	}

	s.buffer = s.buffer[0:0]
	// frameStart is where the packet's own frame begins: the flow ID ahead of
	// it goes on the stream once, not once per packet.
	frameStart := 0
	if !s.sentFlowID {
		s.buffer = append(s.buffer, s.flowIDBytes...)
		s.sentFlowID = true
		frameStart = len(s.buffer)
	}
	s.buffer = quicvarint.Append(s.buffer, uint64(len(packet)))
	headerLen := len(s.buffer)
	s.buffer = append(s.buffer, packet...)

	n, err := s.stream.Write(s.buffer)
	if n < len(s.buffer) {
		// s.buffer is reused by the next call, so the tail has to be copied.
		s.pending = append(s.pending[0:0], s.buffer[n:]...)
		if err == nil {
			err = io.ErrShortWrite
		}
	}
	if s.qlog != nil {
		length := uint64(len(packet))
		framed := uint64(len(s.buffer) - frameStart)
		s.qlog.record(qlog.StreamPacketCreated{
			StreamID: uint64(s.stream.ID()),
			Packet: qlog.Packet{
				FlowID: s.flowID,
				Length: framed,
				Raw: qlog.RawInfo{
					Length:        framed,
					PayloadLength: length,
					Data:          s.qlog.rawData(s.buffer[frameStart:]),
				},
			},
		})
	}
	return min(max(n-headerLen, 0), len(packet)), err
}

// flush writes the tail of a frame that an earlier partial write left behind.
// The caller must hold s.lock.
func (s *RTPSendStream) flush() error {
	n, err := s.stream.Write(s.pending)
	s.pending = s.pending[:copy(s.pending, s.pending[n:])]
	if err == nil && len(s.pending) > 0 {
		err = io.ErrShortWrite
	}
	return err
}

// CancelStream closes the stream with the given error code. It may be called
// concurrently with WriteRTPBytes, and is what unblocks a write that is blocked
// on flow control.
func (s *RTPSendStream) CancelStream(errorCode uint64) {
	s.stream.CancelWrite(errorCode)
}

// Close closes the stream gracefully. It may be called concurrently with
// WriteRTPBytes. Close does not flush the tail of a partially written frame, so
// closing after a short write leaves the peer with a truncated final frame.
func (s *RTPSendStream) Close() error {
	return s.stream.Close()
}
