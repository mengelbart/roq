package roq

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/mengelbart/qlog"
	"github.com/quic-go/quic-go/quicvarint"
)

const defaultReceiveBufferSize = 1024 // Number of packets to buffer

type SendStream interface {
	io.Writer
	io.Closer
	ID() int64
	CancelWrite(uint64)
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

	mutex     sync.Mutex
	closedErr error
	wg        sync.WaitGroup
	ctx       context.Context
	cancelCtx context.CancelFunc

	qlog *qlog.Logger
}

func NewSession(conn Connection, acceptDatagrams bool, qlogger *qlog.Logger) (*Session, error) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		receiveBufferSize: defaultReceiveBufferSize,
		acceptDatagrams:   acceptDatagrams,
		conn:              conn,
		sendFlows:         newSyncMap[uint64, *SendFlow](),
		receiveFlows:      newSyncMap[uint64, *ReceiveFlow](),
		receiveFlowBuffer: newReceiveFlowBuffer(16),
		mutex:             sync.Mutex{},
		closedErr:         nil,
		wg:                sync.WaitGroup{},
		ctx:               ctx,
		cancelCtx:         cancel,
		qlog:              qlogger,
	}
	s.start()
	return s, nil
}

func (s *Session) start() {
	if s.acceptDatagrams {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			if err := s.receiveDatagrams(); err != nil {
				return
			}
		}()
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.receiveUniStreams(); err != nil {
			return
		}
	}()
}

func (s *Session) NewSendFlow(id uint64) (*SendFlow, error) {
	if err := s.isClosed(); err != nil {
		return nil, err
	}
	if _, ok := s.sendFlows.get(id); ok {
		return nil, errors.New("duplicate flow ID")
	}
	f := newFlow(s.conn, id, func() {
		s.sendFlows.delete(id)
	}, s.qlog)
	if err := s.sendFlows.add(id, f); err != nil {
		return nil, err
	}
	return f, nil
}

func (s *Session) NewReceiveFlow(id uint64) (*ReceiveFlow, error) {
	if err := s.isClosed(); err != nil {
		return nil, err
	}
	if _, ok := s.receiveFlows.get(id); ok {
		return nil, errors.New("duplicate flow ID")
	}
	var f *ReceiveFlow
	f = s.receiveFlowBuffer.pop(id)
	if f == nil {
		f = newReceiveFlow(id, s.receiveBufferSize, s.qlog)
	}
	if err := s.receiveFlows.add(id, f); err != nil {
		return nil, err
	}
	return f, nil
}

func (s *Session) close(code uint64, reason string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.sendFlows.rangeFn(func(_ uint64, v *SendFlow) { v.Close() })
	s.receiveFlows.rangeFn(func(_ uint64, v *ReceiveFlow) { v.Close() })
	_ = s.conn.CloseWithError(code, reason)
	s.closedErr = SessionError{
		code:   code,
		reason: reason,
	}
	s.cancelCtx()
	s.wg.Wait()
}

func (s *Session) isClosed() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.closedErr
}

func (s *Session) closeWithError(code uint64, reason string) {
	s.close(code, reason)
}

func (s *Session) Close() error {
	s.close(ErrRoQNoError, "")
	return nil
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
		s.handleDatagram(dgram)
	}
}

func (s *Session) handleDatagram(datagram []byte) {
	flowID, n, err := quicvarint.Parse(datagram)
	if err != nil {
		s.closeWithError(ErrRoQPacketError, "invalid flow ID")
		return
	}
	if s.qlog != nil {
		s.qlog.RoQDatagramPacketParsed(flowID, len(datagram)-n)
	}
	if f, ok := s.receiveFlows.get(flowID); ok {
		f.push(datagram[quicvarint.Len(flowID):])
		return
	}
	f := s.receiveFlowBuffer.get(flowID)
	if f == nil {
		f = newReceiveFlow(flowID, s.receiveBufferSize, s.qlog)
		s.receiveFlowBuffer.add(f)
	}
	f.push(datagram[n:])
}

func (s *Session) handleUniStream(rs ReceiveStream) {
	reader := quicvarint.NewReader(rs)
	flowID, err := quicvarint.Read(reader)
	if err != nil {
		s.closeWithError(ErrRoQPacketError, "invalid flow ID")
		return
	}
	if s.qlog != nil {
		s.qlog.RoQStreamOpened(flowID, rs.ID())
	}
	if f, ok := s.receiveFlows.get(flowID); ok {
		f.readStream(rs)
		return
	}
	f := s.receiveFlowBuffer.get(flowID)
	if f == nil {
		f = newReceiveFlow(flowID, s.receiveBufferSize, s.qlog)
		s.receiveFlowBuffer.add(f)
	}
	f.readStream(rs)
}
