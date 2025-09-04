package roq

import (
	"context"

	"github.com/quic-go/quic-go"
)

type QuicGoReceiveStream struct {
	stream *quic.ReceiveStream
}

func NewQuicGoReceiveStream(stream *quic.ReceiveStream) *QuicGoReceiveStream {
	return &QuicGoReceiveStream{
		stream: stream,
	}
}

func (s *QuicGoReceiveStream) ID() int64 {
	return int64(s.stream.StreamID())
}

func (s *QuicGoReceiveStream) CancelRead(c uint64) {
	s.stream.CancelRead(quic.StreamErrorCode(c))
}

func (c *QuicGoReceiveStream) Read(p []byte) (n int, err error) {
	return c.stream.Read(p)
}

type QuicGoSendStream struct {
	stream *quic.SendStream
}

func NewQuicstream(stream *quic.SendStream) *QuicGoSendStream {
	return &QuicGoSendStream{
		stream: stream,
	}
}

func (s *QuicGoSendStream) ID() int64 {
	return int64(s.stream.StreamID())
}

func (s *QuicGoSendStream) Write(b []byte) (int, error) {
	return s.stream.Write(b)
}

func (s *QuicGoSendStream) Close() error {
	return s.stream.Close()
}

func (s *QuicGoSendStream) CancelWrite(c uint64) {
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
	return &QuicGoSendStream{
		stream: s,
	}, nil
}

func (c *QUICGoConnection) AcceptUniStream(ctx context.Context) (ReceiveStream, error) {
	s, err := c.conn.AcceptUniStream(ctx)
	if err != nil {
		return nil, err
	}
	return &QuicGoReceiveStream{
		stream: s,
	}, nil
}

func (c *QUICGoConnection) CloseWithError(code uint64, reason string) error {
	return c.conn.CloseWithError(quic.ApplicationErrorCode(code), reason)
}
