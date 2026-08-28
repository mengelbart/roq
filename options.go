package roq

import (
	"fmt"

	"github.com/quic-go/quic-go/qlogwriter"
)

const (
	// defaultReceiveBufferSize is the number of packets buffered per receive
	// flow.
	defaultReceiveBufferSize = 1024
	// defaultUnknownFlowBufferSize is the number of receive flows buffered for
	// flow IDs the application has not created a ReceiveFlow for yet.
	defaultUnknownFlowBufferSize = 16
)

// Option configures a Session.
type Option func(*sessionConfig) error

type sessionConfig struct {
	receiveBufferSize     int
	unknownFlowBufferSize int
	qlogTrace             qlogwriter.Trace
	qlogPacketData        bool
	qlogPacketDataLimit   int
}

// newSessionConfig applies opts on top of the defaults.
func newSessionConfig(opts []Option) (*sessionConfig, error) {
	c := &sessionConfig{
		receiveBufferSize:     defaultReceiveBufferSize,
		unknownFlowBufferSize: defaultUnknownFlowBufferSize,
	}
	for _, opt := range opts {
		if opt == nil {
			return nil, fmt.Errorf("%w: option must not be nil", errInvalidOption)
		}
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// WithReceiveBufferSize sets the number of packets each ReceiveFlow of the
// session buffers for the application to read. Packets that arrive for a flow
// whose buffer is full are dropped. It defaults to 1024 packets and must be
// greater than zero.
func WithReceiveBufferSize(packets int) Option {
	return func(c *sessionConfig) error {
		if packets <= 0 {
			return fmt.Errorf("%w: receive buffer size must be greater than zero, got %v", errInvalidOption, packets)
		}
		c.receiveBufferSize = packets
		return nil
	}
}

// WithUnknownFlowBufferSize sets the number of receive flows the session
// buffers for flow IDs the application has not called NewReceiveFlow for yet.
// Once the buffer is full, the oldest of those flows is evicted and its
// streams are cancelled with ErrRoQUnknownFlowID. It defaults to 16 flows and
// must be greater than zero.
func WithUnknownFlowBufferSize(flows int) Option {
	return func(c *sessionConfig) error {
		if flows <= 0 {
			return fmt.Errorf("%w: unknown flow buffer size must be greater than zero, got %v", errInvalidOption, flows)
		}
		c.unknownFlowBufferSize = flows
		return nil
	}
}

// WithQlogTrace sets the qlog trace the session logs its RoQ events to. RoQ
// events are only logged if the trace was created with the RoQ event schema, see
// [github.com/mengelbart/roq/qlog.DefaultConnectionTracer].
//
// It is only needed for connections that do not implement QlogConnection: a
// session on a QUICGoConnection picks up the trace of the QUIC connection by
// itself. Setting it takes precedence over that.
func WithQlogTrace(t qlogwriter.Trace) Option {
	return func(c *sessionConfig) error {
		c.qlogTrace = t
		return nil
	}
}

// WithQlogPacketData makes the session log the wire image of every RTP and RTCP
// packet it sends and receives as the data field of the raw info of its qlog
// events. maxBytes caps how many bytes of each packet are logged; zero logs the
// whole packet. The logged length is always that of the complete packet, even
// when the data is truncated.
//
// Packet data makes qlog files considerably larger, and every logged packet is
// copied on the send and receive paths, so it is off unless this option is set.
func WithQlogPacketData(maxBytes int) Option {
	return func(c *sessionConfig) error {
		if maxBytes < 0 {
			return fmt.Errorf("%w: qlog packet data limit must not be negative, got %v", errInvalidOption, maxBytes)
		}
		c.qlogPacketData = true
		c.qlogPacketDataLimit = maxBytes
		return nil
	}
}
