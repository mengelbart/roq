package roq

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"sync"

	"github.com/mengelbart/roq/qlog"
	"github.com/quic-go/quic-go/qlogwriter"
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

// QlogConnection is implemented by Connections that can provide the qlog trace
// of the underlying QUIC connection. A Session whose Connection implements it
// logs its RoQ events into that trace, provided the trace was created with the
// RoQ event schema. QUICGoConnection implements it.
type QlogConnection interface {
	QlogTrace() qlogwriter.Trace
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

	qlog          *qlogger
	closeQlogOnce sync.Once
	logger        *slog.Logger
}

// NewSession creates a new roq session. QUIC connection is handled by roq.
// It returns an error if conn is nil or if an option is invalid.
func NewSession(conn Connection, acceptDatagrams bool, opts ...Option) (*Session, error) {
	if conn == nil {
		return nil, errNilConnection
	}
	config, err := newSessionConfig(opts)
	if err != nil {
		return nil, err
	}
	s := newSession(conn, acceptDatagrams, config)
	s.start()

	return s, nil
}

// NewSessionWithAppHandledConn creates a new roq session. QUIC connection is
// handled by the application. HandleDatagram and HandleUniStreamWithFlowID have
// to be called for each datagram / new stream. It returns an error if conn is
// nil or if an option is invalid.
func NewSessionWithAppHandledConn(conn Connection, acceptDatagrams bool, opts ...Option) (*Session, error) {
	if conn == nil {
		return nil, errNilConnection
	}
	config, err := newSessionConfig(opts)
	if err != nil {
		return nil, err
	}
	s := newSession(conn, acceptDatagrams, config)

	return s, nil
}

func newSession(conn Connection, acceptDatagrams bool, config *sessionConfig) *Session {
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
		qlog:              newQlogger(conn, config),
		logger:            config.logger,
	}
}

// newQlogger returns the qlogger the session logs its RoQ events to, or nil if
// RoQ events are not being logged. A trace set on the config takes precedence
// over the trace of the connection, if any.
func newQlogger(conn Connection, config *sessionConfig) *qlogger {
	trace := config.qlogTrace
	if trace == nil {
		qc, ok := conn.(QlogConnection)
		if !ok {
			return nil
		}
		trace = qc.QlogTrace()
	}
	if trace == nil || !trace.SupportsSchemas(qlog.EventSchema) {
		return nil
	}
	return &qlogger{
		recorder:  trace.AddProducer(),
		logData:   config.qlogPacketData,
		dataLimit: config.qlogPacketDataLimit,
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
	}, s.qlog, s.logger)
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
		f = newReceiveFlow(id, s.receiveBufferSize, s.qlog, s.logger)
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
//
// Close also releases the session's qlog producer, which is what finishes the
// qlog trace of the QUIC connection, so it has to be called for the trace to be
// written completely.
func (s *Session) Close() error {
	s.close(ErrRoQNoError, "")
	s.wg.Wait()
	// Only now that nothing can record another event is it safe to close the
	// producer: Recorder.Close must not run concurrently with RecordEvent.
	if s.qlog != nil {
		s.closeQlogOnce.Do(func() { _ = s.qlog.close() })
	}
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
	return s.receiveFlowBuffer.getOrCreate(flowID, s.receiveBufferSize, s.qlog, s.logger)
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
		s.qlog.record(qlog.DatagramPacketParsed{
			Packet: qlog.Packet{
				FlowID: flowID,
				Length: uint64(len(datagram)),
				Raw: qlog.RawInfo{
					Length:        uint64(len(datagram)),
					PayloadLength: uint64(len(datagram) - n),
					Data:          s.qlog.rawData(datagram),
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
		s.qlog.record(qlog.StreamOpened{
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
