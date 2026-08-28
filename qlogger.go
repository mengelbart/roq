package roq

import (
	"github.com/quic-go/quic-go/qlogwriter"
)

// qlogger records the RoQ events of a session.
type qlogger struct {
	recorder qlogwriter.Recorder
	// logData reports whether the bytes of a packet are logged along with its
	// lengths. dataLimit caps how many of them are kept; zero keeps the whole
	// packet.
	logData   bool
	dataLimit int
}

func (q *qlogger) record(e qlogwriter.Event) {
	q.recorder.RecordEvent(e)
}

func (q *qlogger) close() error {
	return q.recorder.Close()
}

// rawData returns the bytes to log as the wire image of a packet, or nil if
// the session does not log packet data. The event is encoded on another
// goroutine and callers reuse their buffers, so the bytes have to be copied.
func (q *qlogger) rawData(b []byte) []byte {
	if !q.logData {
		return nil
	}
	if q.dataLimit > 0 && len(b) > q.dataLimit {
		b = b[:q.dataLimit]
	}
	return append([]byte(nil), b...)
}
