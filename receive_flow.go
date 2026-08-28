package roq

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/mengelbart/roq/qlog"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/quicvarint"
)

// maxPacketBufferSize is the capacity pooled packet buffers are allocated
// with. It is the largest possible UDP payload, so a packet never needs the
// buffer to grow.
const maxPacketBufferSize = 65535

type ReceiveFlow struct {
	id         uint64
	buffer     chan *bytes.Buffer
	bufferPool sync.Pool
	ctx        context.Context
	cancelCtx  context.CancelFunc

	lock    sync.Mutex
	streams map[int64]ReceiveStream
	closed  bool

	// deadlineCh is closed when the current read deadline expires or is
	// replaced, which wakes blocked readers so that they pick up the new
	// deadline. deadlineExpired records whether it was closed by expiry.
	deadlineCh      chan struct{}
	deadlineExpired bool
	deadlineTimer   *time.Timer

	qlog   *qlogger
	logger *slog.Logger
}

func newReceiveFlow(id uint64, receiveBufferSize int, qlog *qlogger, logger *slog.Logger) *ReceiveFlow {
	if logger == nil {
		logger = discardLogger()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &ReceiveFlow{
		id:     id,
		buffer: make(chan *bytes.Buffer, receiveBufferSize),
		bufferPool: sync.Pool{
			New: func() any {
				return bytes.NewBuffer(make([]byte, 0, maxPacketBufferSize))
			},
		},
		ctx:        ctx,
		cancelCtx:  cancel,
		lock:       sync.Mutex{},
		streams:    map[int64]ReceiveStream{},
		closed:     false,
		deadlineCh: make(chan struct{}),
		qlog:       qlog,
		logger:     logger,
	}
}

// push queues packet for reading. If the queue is full or the flow is closed,
// the packet is dropped and its buffer handed back to the pool.
func (f *ReceiveFlow) push(packet *bytes.Buffer) {
	f.lock.Lock()
	defer f.lock.Unlock()
	if f.closed {
		f.bufferPool.Put(packet)
		return
	}
	select {
	case f.buffer <- packet:
	default:
		f.bufferPool.Put(packet)
	}
}

func (f *ReceiveFlow) readStream(rs ReceiveStream) {
	// Checking for closure and registering the stream must be atomic with
	// respect to closeWithError: a stream registered after it has run would
	// never be cancelled, and readStream below would block forever.
	f.lock.Lock()
	select {
	case <-f.ctx.Done():
		f.lock.Unlock()
		rs.CancelRead(ErrRoQNoError)
		return
	default:
	}
	f.streams[rs.ID()] = rs
	f.lock.Unlock()

	// Unregister on exit, so that flows reading a stream per frame do not
	// accumulate finished streams for the lifetime of the session.
	defer func() {
		f.lock.Lock()
		defer f.lock.Unlock()
		delete(f.streams, rs.ID())
	}()

	reader := quicvarint.NewReader(rs)
	var lengthBuf [8]byte
	for {
		length, encodedLength, err := readVarint(reader, lengthBuf[:])
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			var streamErr *quic.StreamError
			if errors.As(err, &streamErr) {
				f.logger.Debug("got stream error while reading length",
					"flowID", f.id, "streamID", rs.ID(), "error", streamErr)
				return
			}
			f.logger.Warn("got unexpected error while reading length",
				"flowID", f.id, "streamID", rs.ID(), "error", err)
			return
		}
		r := io.LimitReader(reader, int64(length))
		b := f.bufferPool.Get().(*bytes.Buffer)
		b.Reset()
		// The buffer holds the frame as it was on the wire, prefix included,
		// until the packet is handed to the flow below.
		b.Write(encodedLength)
		n, err := b.ReadFrom(r)
		if err != nil {
			var streamErr *quic.StreamError
			if errors.As(err, &streamErr) {
				f.logger.Debug("got stream error while reading payload",
					"flowID", f.id, "streamID", rs.ID(), "read", n, "error", streamErr)
				return
			}
			f.logger.Warn("got unexpected error while reading payload",
				"flowID", f.id, "streamID", rs.ID(), "read", n, "error", err)
			return
		}
		if f.qlog != nil {
			f.qlog.record(qlog.StreamPacketParsed{
				StreamID: uint64(rs.ID()),
				Packet: qlog.Packet{
					FlowID: f.id,
					Length: uint64(b.Len()),
					Raw: qlog.RawInfo{
						Length:        uint64(b.Len()),
						PayloadLength: length,
						Data:          f.qlog.rawData(b.Bytes()),
					},
				},
			})
		}
		// Skip the prefix again: the flow hands out RTP and RTCP packets.
		b.Next(len(encodedLength))
		f.push(b)
	}
}

// readVarint reads a QUIC varint from r and returns its value together with the
// bytes it was encoded in, which alias buf. buf must have room for 8 bytes.
//
// A QUIC varint is not required to use the shortest encoding of its value (RFC
// 9000, Section 16), so the encoding cannot be reconstructed from the value
// alone. Keeping it lets the qlog events report the frame as it was on the
// wire rather than as this implementation would have written it.
func readVarint(r quicvarint.Reader, buf []byte) (uint64, []byte, error) {
	firstByte, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	// The first two bits of the first byte encode the length.
	buf = buf[:1<<(firstByte>>6)]
	buf[0] = firstByte
	if len(buf) > 1 {
		if _, err := io.ReadFull(r, buf[1:]); err != nil {
			return 0, nil, err
		}
	}
	v, _, err := quicvarint.Parse(buf)
	return v, buf, err
}

// Read reads the next RTP packet of the flow into buf. Packets are not split
// across calls: if buf is too small to hold the complete packet, the packet is
// dropped and Read returns 0 and io.ErrShortBuffer. Callers should provide a
// buffer large enough for the largest packet they expect to receive.
//
// Read blocks until a packet arrives, the flow is closed, or the deadline set
// with SetReadDeadline passes.
func (f *ReceiveFlow) Read(buf []byte) (int, error) {
	for {
		f.lock.Lock()
		expired, deadlineCh := f.deadlineExpired, f.deadlineCh
		f.lock.Unlock()
		if expired {
			return 0, os.ErrDeadlineExceeded
		}
		select {
		case packet, ok := <-f.buffer:
			if !ok {
				return 0, f.ctx.Err()
			}
			return f.copyPacket(buf, packet)
		case <-deadlineCh:
			// The deadline expired or was replaced: re-read it and wait again.
		}
	}
}

func (f *ReceiveFlow) copyPacket(buf []byte, packet *bytes.Buffer) (int, error) {
	if len(buf) < packet.Len() {
		f.bufferPool.Put(packet)
		return 0, io.ErrShortBuffer
	}
	n := copy(buf, packet.Bytes())
	f.bufferPool.Put(packet)
	return n, nil
}

// SetReadDeadline sets the deadline for future Read calls and for any Read
// that is currently blocked. Once the deadline has passed, Read returns
// os.ErrDeadlineExceeded without waiting for a packet, even if packets are
// still buffered, until the deadline is extended or cleared. A zero value for
// t clears the deadline.
func (f *ReceiveFlow) SetReadDeadline(t time.Time) error {
	f.lock.Lock()
	defer f.lock.Unlock()
	if f.deadlineTimer != nil {
		f.deadlineTimer.Stop()
		f.deadlineTimer = nil
	}
	// Wake the readers waiting on the previous deadline and give them a fresh
	// channel to wait on, so that they observe the new one.
	if !f.deadlineExpired {
		close(f.deadlineCh)
	}
	f.deadlineCh = make(chan struct{})
	f.deadlineExpired = false
	if t.IsZero() {
		return nil
	}
	d := time.Until(t)
	if d <= 0 {
		f.expireDeadline(f.deadlineCh)
		return nil
	}
	ch := f.deadlineCh
	f.deadlineTimer = time.AfterFunc(d, func() {
		f.lock.Lock()
		defer f.lock.Unlock()
		f.expireDeadline(ch)
	})
	return nil
}

// expireDeadline reports the deadline ch belongs to as expired. It does
// nothing if that deadline has meanwhile been replaced. Must be called with
// f.lock held.
func (f *ReceiveFlow) expireDeadline(ch chan struct{}) {
	if f.deadlineCh != ch || f.deadlineExpired {
		return
	}
	f.deadlineExpired = true
	close(ch)
}

func (f *ReceiveFlow) ID() uint64 {
	return f.id
}

func (f *ReceiveFlow) closeWithError(code uint64) {
	f.lock.Lock()
	defer f.lock.Unlock()
	for _, s := range f.streams {
		s.CancelRead(code)
	}
}

// Close closes the flow.
func (f *ReceiveFlow) Close() error {
	// Cancel before closing the buffer, so that a Read woken by the closed
	// buffer always finds a non-nil error to report.
	f.cancelCtx()
	f.closeBuffer()
	f.closeWithError(ErrRoQNoError)
	return nil
}

// closeBuffer makes the flow reject further pushes and closes the packet
// buffer to unblock Read once the queued packets are drained.
func (f *ReceiveFlow) closeBuffer() {
	f.lock.Lock()
	defer f.lock.Unlock()
	if f.closed {
		return
	}
	f.closed = true
	if f.deadlineTimer != nil {
		f.deadlineTimer.Stop()
		f.deadlineTimer = nil
	}
	close(f.buffer)
}
