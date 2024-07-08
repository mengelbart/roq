package main

import (
	"context"
	"io"

	"github.com/mengelbart/roq"
	"github.com/pion/rtp"
)

type FrameReader interface {
	Read() ([]byte, error)
}

type sender struct {
	session   *roq.Session
	datagrams bool
}

func newSender(conn roq.Connection, qlog io.Writer, datagrams bool) (*sender, error) {
	session, err := roq.NewSession(conn, true, qlog)
	if err != nil {
		return nil, err
	}
	return &sender{
		session:   session,
		datagrams: datagrams,
	}, err
}

func (s *sender) send(flowID uint64, reader FrameReader, packetizer rtp.Packetizer) error {
	flow, err := s.session.NewSendFlow(flowID)
	if err != nil {
		return err
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
		if s.datagrams {
			if err := s.sendDatagrams(flow, packets); err != nil {
				return err
			}
		} else {
			if err := s.sendStream(flow, packets); err != nil {
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

func (s *sender) sendStream(flow *roq.SendFlow, packets []*rtp.Packet) error {
	stream, err := flow.NewSendStream(context.Background())
	if err != nil {
		return err
	}
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
	return stream.Close()
}

func (s *sender) Close() error {
	return s.session.Close()
}
