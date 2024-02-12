package roq

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"

	"github.com/quic-go/quic-go/quicvarint"
)

const DEFAULT_RECEIVE_BUFFER_SIZE = 1024 // Number of packets to buffer

type SendStream interface {
	io.Writer
	io.Closer
	CancelWrite(uint64)
}

type ReceiveStream interface {
	io.Reader
	CancelRead(uint64)
}

type Connection interface {
	SendDatagram(payload []byte) error
	ReceiveDatagram(context.Context) ([]byte, error)
	OpenUniStreamSync(context.Context) (SendStream, error)
	AcceptUniStream(context.Context) (ReceiveStream, error)
}

type Session struct {
	receiveBufferSize int

	lock         sync.Mutex
	conn         Connection
	sendFlows    map[uint64]*SendFlow
	receiveFlows map[uint64]*ReceiveFlow
}

func NewSession(conn Connection) (*Session, error) {
	s := &Session{
		receiveBufferSize: DEFAULT_RECEIVE_BUFFER_SIZE,
		lock:              sync.Mutex{},
		conn:              conn,
		sendFlows:         map[uint64]*SendFlow{},
		receiveFlows:      map[uint64]*ReceiveFlow{},
	}
	go func() {
		if err := s.receiveDatagrams(); err != nil {
			// TODO
			panic(err)
		}
	}()
	go func() {
		if err := s.receiveUniStreams(); err != nil {
			// TODO
			panic(err)
		}
	}()
	return s, nil
}

func (s *Session) NewSendFlow(id uint64) (*SendFlow, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if _, ok := s.sendFlows[id]; ok {
		return nil, errors.New("duplicate flow ID")
	}
	f := newFlow(s.conn, id, func() {
		s.lock.Lock()
		defer s.lock.Unlock()
		delete(s.sendFlows, id)
	})
	s.sendFlows[id] = f
	return f, nil
}

func (s *Session) NewReceiveFlow(id uint64) (*ReceiveFlow, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if _, ok := s.receiveFlows[id]; ok {
		return nil, errors.New("duplicate flow ID")
	}
	f := newReceiveFlow(id, s.receiveBufferSize)
	s.receiveFlows[id] = f
	return f, nil
}

func (s *Session) receiveUniStreams() error {
	for {
		rs, err := s.conn.AcceptUniStream(context.TODO())
		if err != nil {
			return err
		}
		go s.handleUniStream(rs)
	}
}

func (s *Session) receiveDatagrams() error {
	for {
		dgram, err := s.conn.ReceiveDatagram(context.TODO())
		if err != nil {
			return err
		}
		go s.handleDatagram(dgram)
	}
}

func (s *Session) handleDatagram(packet []byte) {
	reader := quicvarint.NewReader(bytes.NewReader(packet))
	flowID, err := quicvarint.Read(reader)
	if err != nil {
		panic(err)
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	if f, ok := s.receiveFlows[flowID]; ok {
		f.push(packet[quicvarint.Len(flowID):])
		return
	}
	panic("TODO: Handle unknown flow IDs")
}

func (s *Session) handleUniStream(rs ReceiveStream) {
	reader := quicvarint.NewReader(rs)
	flowID, err := quicvarint.Read(reader)
	if err != nil {
		panic(err)
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	if f, ok := s.receiveFlows[flowID]; ok {
		f.readStream(rs)
		return
	}
	panic("TODO: Handle unknown flow IDs")
}
