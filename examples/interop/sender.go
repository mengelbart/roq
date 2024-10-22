package main

import (
	"context"
	"io"

	"github.com/mengelbart/qlog"
	"github.com/mengelbart/roq"
	"github.com/pion/rtp"
)

type mode int

const (
	datagramMode mode = iota
	streamPerFrameMode
	singleStreamMode
)

type FrameReader interface {
	Read() ([]byte, error)
}

type sender struct {
	session *roq.Session
}

func newSender(conn roq.Connection, qlog *qlog.Logger) (*sender, error) {
	session, err := roq.NewSession(conn, true, qlog)
	if err != nil {
		return nil, err
	}
	session.Start()
	return &sender{
		session: session,
	}, err
}

func (s *sender) send(flowID uint64, reader FrameReader, packetizer rtp.Packetizer, sendMode mode) error {
	flow, err := s.session.NewSendFlow(flowID)
	if err != nil {
		return err
	}
	var singleStream *roq.RTPSendStream
	if sendMode == singleStreamMode {
		singleStream, err = flow.NewSendStream(context.Background())
		if err != nil {
			return err
		}
		defer singleStream.Close()
	}
	defer flow.Close()
	for {
		frame, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		packets := packetizer.Packetize(frame, 1)
		switch sendMode {
		case datagramMode:
			if err := s.sendDatagrams(flow, packets); err != nil {
				return err
			}
		case streamPerFrameMode:
			stream, err := flow.NewSendStream(context.Background())
			if err != nil {
				return err
			}
			if err := s.sendStream(stream, packets); err != nil {
				return err
			}
			if err := stream.Close(); err != nil {
				return err
			}
		case singleStreamMode:
			if err := s.sendStream(singleStream, packets); err != nil {
				return err
			}
		}
	}
}

func (s *sender) sendDatagrams(flow *roq.SendFlow, packets []*rtp.Packet) error {
	for _, pkt := range packets {
		buf, err := pkt.Marshal()
		if err != nil {
			return err
		}
		err = flow.WriteRTPBytes(buf)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *sender) sendStream(stream *roq.RTPSendStream, packets []*rtp.Packet) error {
	for _, pkt := range packets {
		buf, err := pkt.Marshal()
		if err != nil {
			return err
		}
		_, err = stream.WriteRTPBytes(buf)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *sender) Close() error {
	return s.session.Close()
}
