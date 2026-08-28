package roq

import (
	"bytes"
	"context"
	"io"
	"sync"

	"github.com/mengelbart/qlog"
	roqqlog "github.com/mengelbart/qlog/roq"
	"github.com/quic-go/quic-go/quicvarint"
)

type SendStream interface {
	io.Writer
	io.Closer
	ID() int64
	CancelWrite(uint64)
}

type PrioritySendStream interface {
	SetPriority(p uint32)
	SetIncremental(b bool)
}

type ReceiveStream interface {
	io.Reader
	ID() int64
	CancelRead(uint64)
}

type Connection interface {
	SendDatagram(payload []byte) error
	ReceiveDatagram(context.Context) ([]byte, error)
	OpenUniStreamSync(context.Context) (SendStream, error)
	AcceptUniStream(context.Context) (ReceiveStream, error)
	CloseWithError(uint64, string) error
}

type Session struct {
	receiveBufferSize int
	acceptDatagrams   bool

	conn              Connection
	sendFlows         *syncMap[uint64, *SendFlow]
	receiveFlows      *syncMap[uint64, *ReceiveFlow]
	receiveFlowBuffer *receiveFlowBuffer
	receiveFlowsMutex sync.Mutex

	mutex sync.Mutex
	// closedErr is the SessionError reported by operations on the closed
	// session; connCloseErr is what closing the QUIC connection returned.
	closedErr    error
	connCloseErr error
	wg           sync.WaitGroup
	ctx          context.Context
	cancelCtx    context.CancelFunc

	qlog *qlog.Logger
}

// NewSession creates a new roq session. QUIC connection is handled by roq.
// It returns an error if conn is nil or if an option is invalid.
func NewSession(conn Connection, acceptDatagrams bool, qlogger *qlog.Logger, opts ...Option) (*Session, error) {
	if conn == nil {
		return nil, errNilConnection
	}
	config, err := newSessionConfig(opts)
	if err != nil {
		return nil, err
	}
	s := newSession(conn, acceptDatagrams, qlogger, config)
	s.start()

	return s, nil
}

// NewSessionWithAppHandledConn creates a new roq session. QUIC connection is
// handled by the application. HandleDatagram and HandleUniStreamWithFlowID have
// to be called for each datagram / new stream. It returns an error if conn is
// nil or if an option is invalid.
func NewSessionWithAppHandledConn(conn Connection, acceptDatagrams bool, qlogger *qlog.Logger, opts ...Option) (*Session, error) {
	if conn == nil {
		return nil, errNilConnection
	}
	config, err := newSessionConfig(opts)
	if err != nil {
		return nil, err
	}
	s := newSession(conn, acceptDatagrams, qlogger, config)

	return s, nil
}

func newSession(conn Connection, acceptDatagrams bool, qlogger *qlog.Logger, config *sessionConfig) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	return &Session{
		receiveBufferSize: config.receiveBufferSize,
		acceptDatagrams:   acceptDatagrams,
		conn:              conn,
		sendFlows:         newSyncMap[uint64, *SendFlow](),
		receiveFlows:      newSyncMap[uint64, *ReceiveFlow](),
		receiveFlowBuffer: newReceiveFlowBuffer(config.unknownFlowBufferSize),
		receiveFlowsMutex: sync.Mutex{},
		mutex:             sync.Mutex{},
		closedErr:         nil,
		wg:                sync.WaitGroup{},
		ctx:               ctx,
		cancelCtx:         cancel,
		qlog:              qlogger,
	}
}

func (s *Session) start() {
	// The receive loops run until the session context is cancelled or the
	// connection fails, and only ever return a non-nil error describing why.
	// There is nothing left to act on here: close has already run by then, or
	// is what stopped them in the first place.
	if s.acceptDatagrams {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			_ = s.receiveDatagrams()
		}()
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		_ = s.receiveUniStreams()
	}()
}

func (s *Session) NewSendFlow(id uint64) (*SendFlow, error) {
	if err := s.isClosed(); err != nil {
		return nil, err
	}
	f := newFlow(s.conn, id, func() {
		s.sendFlows.delete(id)
	}, s.qlog)
	if _, ok := s.sendFlows.getOrInsert(id, f); !ok {
		return nil, errDuplicateFlowID
	}
	return f, nil
}

func (s *Session) NewReceiveFlow(id uint64) (*ReceiveFlow, error) {
	if err := s.isClosed(); err != nil {
		return nil, err
	}
	s.receiveFlowsMutex.Lock()
	defer s.receiveFlowsMutex.Unlock()
	if _, ok := s.receiveFlows.get(id); ok {
		return nil, errDuplicateFlowID
	}
	f := s.receiveFlowBuffer.pop(id)
	if f == nil {
		f = newReceiveFlow(id, s.receiveBufferSize, s.qlog)
	}
	s.receiveFlows.set(id, f)
	return f, nil
}

// close initiates the session shutdown: it tears down all flows, closes the
// QUIC connection and cancels the session context, which makes the receive
// loops return. It is idempotent, and the first caller's error code wins.
//
// close does not wait for the receive loops to return, so it is safe to call
// from the receive loops themselves. Use Close to also wait for them.
func (s *Session) close(code uint64, reason string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.closedErr != nil {
		return
	}
	s.closedErr = SessionError{
		code:   code,
		reason: reason,
	}
	s.sendFlows.rangeFn(func(_ uint64, v *SendFlow) { _ = v.Close() })
	s.receiveFlows.rangeFn(func(_ uint64, v *ReceiveFlow) { _ = v.Close() })
	s.connCloseErr = s.conn.CloseWithError(code, reason)
	s.cancelCtx()
}

func (s *Session) isClosed() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.closedErr
}

func (s *Session) closeWithError(code uint64, reason string) {
	s.close(code, reason)
}

// Close closes the session and waits for its receive loops to return. It must
// not be called from a callback running on one of those loops.
//
// Close returns the error from closing the underlying QUIC connection. It is
// idempotent: later calls close nothing and report the same error as the call
// that closed the session.
func (s *Session) Close() error {
	s.close(ErrRoQNoError, "")
	s.wg.Wait()
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.connCloseErr
}

func (s *Session) receiveUniStreams() error {
	for {
		rs, err := s.conn.AcceptUniStream(s.ctx)
		if err != nil {
			return err
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleUniStream(rs)
		}()
	}
}

func (s *Session) receiveDatagrams() error {
	for {
		dgram, err := s.conn.ReceiveDatagram(s.ctx)
		if err != nil {
			return err
		}
		s.HandleDatagram(dgram)
	}
}

func (s *Session) receiveFlow(flowID uint64) *ReceiveFlow {
	s.receiveFlowsMutex.Lock()
	defer s.receiveFlowsMutex.Unlock()
	if f, ok := s.receiveFlows.get(flowID); ok {
		return f
	}
	return s.receiveFlowBuffer.getOrCreate(flowID, s.receiveBufferSize, s.qlog)
}

// HandleDatagram handles a datagram. If QUIC connection is handled by the
// application, this function has to be called by the application for each
// datagram that belongs to the roq connection.
func (s *Session) HandleDatagram(datagram []byte) {
	flowID, n, err := quicvarint.Parse(datagram)
	if err != nil {
		s.closeWithError(ErrRoQPacketError, "invalid flow ID")
		return
	}
	if s.qlog != nil {
		raw := make([]byte, len(datagram))
		m := copy(raw, datagram)
		s.qlog.Log(roqqlog.DatagramPacketEvent{
			Type: roqqlog.DatagramPacketEventTypeParsed,
			Packet: roqqlog.Packet{
				FlowID: flowID,
				Length: uint64(n),
				Raw: &qlog.RawInfo{
					Length:        uint64(m),
					PayloadLength: uint64(m),
					Data:          raw,
				},
			},
		})
	}
	f := s.receiveFlow(flowID)
	b := f.bufferPool.Get().(*bytes.Buffer)
	b.Reset()
	b.Write(datagram[quicvarint.Len(flowID):])
	f.push(b)
}

// HandleUniStreamWithFlowID handles a new stream with the flow ID already
// parsed. If QUIC connection is handled by the application, this function has to
// be called by the application for each new QUIC stream containing a roq flow
// ID.
func (s *Session) HandleUniStreamWithFlowID(flowID uint64, rs ReceiveStream) {
	if s.qlog != nil {
		s.qlog.Log(roqqlog.StreamOpenedEvent{
			FlowID:   flowID,
			StreamID: uint64(rs.ID()),
		})
	}
	s.receiveFlow(flowID).readStream(rs)
}

func (s *Session) handleUniStream(rs ReceiveStream) {
	reader := quicvarint.NewReader(rs)
	flowID, err := quicvarint.Read(reader)
	if err != nil {
		rs.CancelRead(ErrRoQPacketError)
		return
	}

	s.HandleUniStreamWithFlowID(flowID, rs)
}
