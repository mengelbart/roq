package roq

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"

	"github.com/quic-go/quic-go/quicvarint"
)

const defaultReceiveBufferSize = 1024 // Number of packets to buffer

type SendStream interface {
	io.Writer
	io.Closer
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
	ctx               context.Context
	cancelCtx         context.CancelCauseFunc
	wg                sync.WaitGroup
	sendFlows         *syncMap[uint64, *SendFlow]
	receiveFlows      *syncMap[uint64, *ReceiveFlow]
	receiveFlowBuffer *receiveFlowBuffer
}

func NewSession(conn Connection, acceptDatagrams bool) (*Session, error) {
	ctx, cancel := context.WithCancelCause(context.Background())
	s := &Session{
		receiveBufferSize: defaultReceiveBufferSize,
		acceptDatagrams:   acceptDatagrams,
		conn:              conn,
		ctx:               ctx,
		cancelCtx:         cancel,
		wg:                sync.WaitGroup{},
		sendFlows:         newSyncMap[uint64, *SendFlow](),
		receiveFlows:      newSyncMap[uint64, *ReceiveFlow](),
		receiveFlowBuffer: newReceiveFlowBuffer(16),
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
				s.Close()
			}
		}()
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.receiveUniStreams(); err != nil {
			s.Close()
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
	})
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
		f = newReceiveFlow(id, s.receiveBufferSize)
	}
	if err := s.receiveFlows.add(id, f); err != nil {
		return nil, err
	}
	return f, nil
}

func (s *Session) isClosed() error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	default:
		return nil
	}
}

func (s *Session) close(code uint64, reason string) {
	s.sendFlows.rangeFn(func(_ uint64, v *SendFlow) { v.Close() })
	s.receiveFlows.rangeFn(func(_ uint64, v *ReceiveFlow) { v.Close() })
	_ = s.conn.CloseWithError(code, reason)
	s.wg.Wait()
}

func (s *Session) closeWithError(code uint64, reason string) {
	s.cancelCtx(errors.New(reason))
	s.close(code, reason)
}

func (s *Session) Close() error {
	s.cancelCtx(nil)
	s.close(ErrRoQNoError, "")
	return nil
}

func (s *Session) receiveUniStreams() error {
	for {
		rs, err := s.conn.AcceptUniStream(context.TODO())
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
		dgram, err := s.conn.ReceiveDatagram(context.TODO())
		if err != nil {
			return err
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleDatagram(dgram)
		}()
	}
}

func (s *Session) handleDatagram(packet []byte) {
	reader := quicvarint.NewReader(bytes.NewReader(packet))
	flowID, err := quicvarint.Read(reader)
	if err != nil {
		s.closeWithError(ErrRoQPacketError, "invalid flow ID")
		return
	}
	if f, ok := s.receiveFlows.get(flowID); ok {
		f.push(packet[quicvarint.Len(flowID):])
		return
	}
	f := s.receiveFlowBuffer.get(flowID)
	if f == nil {
		f = newReceiveFlow(flowID, s.receiveBufferSize)
		s.receiveFlowBuffer.add(f)
	}
	f.push(packet[quicvarint.Len(flowID):])
}

func (s *Session) handleUniStream(rs ReceiveStream) {
	reader := quicvarint.NewReader(rs)
	flowID, err := quicvarint.Read(reader)
	if err != nil {
		s.closeWithError(ErrRoQPacketError, "invalid flow ID")
		return
	}
	if f, ok := s.receiveFlows.get(flowID); ok {
		f.readStream(rs)
		return
	}
	f := s.receiveFlowBuffer.get(flowID)
	if f == nil {
		f = newReceiveFlow(flowID, s.receiveBufferSize)
		s.receiveFlowBuffer.add(f)
	}
	f.readStream(rs)
}
