package main

import (
	"io"

	"github.com/mengelbart/roq"
	"github.com/pion/rtp"
)

type FrameReader interface {
	Read() ([]byte, error)
}

type sender struct {
	session *roq.Session
}

func newSender(conn roq.Connection, qlog io.Writer) (*sender, error) {
	session, err := roq.NewSession(conn, true, qlog)
	if err != nil {
		return nil, err
	}
	return &sender{
		session: session,
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
		//stream, err := flow.NewSendStream(ctx)
		if err != nil {
			return err
		}
		for _, pkt := range packets {
			buf, err := pkt.Marshal()
			if err != nil {
				return err
			}
			err = flow.WriteRTPBytes(buf)
			//_, err = stream.WriteRTPBytes(buf)
			if err != nil {
				return err
			}
		}
		//stream.Close()
	}
}

func (s *sender) Close() error {
	return s.session.Close()
}
