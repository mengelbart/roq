package roq

import (
	"context"

	"github.com/quic-go/quic-go"
)

type quicGoReceiveStream struct {
	stream quic.ReceiveStream
}

func (s *quicGoReceiveStream) ID() int64 {
	return int64(s.stream.StreamID())
}

func (s *quicGoReceiveStream) CancelRead(c uint64) {
	s.stream.CancelRead(quic.StreamErrorCode(c))
}

func (c *quicGoReceiveStream) Read(p []byte) (n int, err error) {
	return c.stream.Read(p)
}

type quicGoSendStream struct {
	stream quic.SendStream
}

func (s *quicGoSendStream) ID() int64 {
	return int64(s.stream.StreamID())
}

func (s *quicGoSendStream) Write(b []byte) (int, error) {
	return s.stream.Write(b)
}

func (s *quicGoSendStream) Close() error {
	return s.stream.Close()
}

func (s *quicGoSendStream) CancelWrite(c uint64) {
	s.stream.CancelWrite(quic.StreamErrorCode(c))
}

type QUICGoConnection struct {
	conn quic.Connection
}

func NewQUICGoConnection(conn quic.Connection) *QUICGoConnection {
	return &QUICGoConnection{
		conn: conn,
	}
}

func (c *QUICGoConnection) SendDatagram(payload []byte) error {
	return c.conn.SendDatagram(payload)
}

func (c *QUICGoConnection) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	return c.conn.ReceiveDatagram(ctx)
}

func (c *QUICGoConnection) OpenUniStreamSync(ctx context.Context) (quic.SendStream, error) {
	s, err := c.conn.OpenUniStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (c *QUICGoConnection) AcceptUniStream(ctx context.Context) (quic.ReceiveStream, error) {
	s, err := c.conn.AcceptUniStream(ctx)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (c *QUICGoConnection) CloseWithError(code quic.ApplicationErrorCode, reason string) error {
	return c.conn.CloseWithError(quic.ApplicationErrorCode(code), reason)
}
