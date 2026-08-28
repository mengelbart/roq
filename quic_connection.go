package roq

import (
	"context"

	"github.com/quic-go/quic-go"
)

type QUICGoReceiveStream struct {
	stream *quic.ReceiveStream
}

func NewQUICGoReceiveStream(stream *quic.ReceiveStream) *QUICGoReceiveStream {
	return &QUICGoReceiveStream{
		stream: stream,
	}
}

func (s *QUICGoReceiveStream) ID() int64 {
	return int64(s.stream.StreamID())
}

func (s *QUICGoReceiveStream) CancelRead(c uint64) {
	s.stream.CancelRead(quic.StreamErrorCode(c))
}

func (s *QUICGoReceiveStream) Read(p []byte) (n int, err error) {
	return s.stream.Read(p)
}

type QUICGoSendStream struct {
	stream *quic.SendStream
}

func NewQUICGoSendStream(stream *quic.SendStream) *QUICGoSendStream {
	return &QUICGoSendStream{
		stream: stream,
	}
}

func (s *QUICGoSendStream) ID() int64 {
	return int64(s.stream.StreamID())
}

func (s *QUICGoSendStream) Write(b []byte) (int, error) {
	return s.stream.Write(b)
}

func (s *QUICGoSendStream) Close() error {
	return s.stream.Close()
}

func (s *QUICGoSendStream) CancelWrite(c uint64) {
	s.stream.CancelWrite(quic.StreamErrorCode(c))
}

type QUICGoConnection struct {
	conn *quic.Conn
}

func NewQUICGoConnection(conn *quic.Conn) *QUICGoConnection {
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

func (c *QUICGoConnection) OpenUniStreamSync(ctx context.Context) (SendStream, error) {
	s, err := c.conn.OpenUniStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return &QUICGoSendStream{
		stream: s,
	}, nil
}

func (c *QUICGoConnection) AcceptUniStream(ctx context.Context) (ReceiveStream, error) {
	s, err := c.conn.AcceptUniStream(ctx)
	if err != nil {
		return nil, err
	}
	return &QUICGoReceiveStream{
		stream: s,
	}, nil
}

func (c *QUICGoConnection) CloseWithError(code uint64, reason string) error {
	return c.conn.CloseWithError(quic.ApplicationErrorCode(code), reason)
}
