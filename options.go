package roq

import "fmt"

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
}

// newSessionConfig applies opts on top of the defaults.
func newSessionConfig(opts []Option) (*sessionConfig, error) {
	c := &sessionConfig{
		receiveBufferSize:     defaultReceiveBufferSize,
		unknownFlowBufferSize: defaultUnknownFlowBufferSize,
	}
	for _, opt := range opts {
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
