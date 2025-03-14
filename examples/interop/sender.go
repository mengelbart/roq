package main

import (
	"context"
	"errors"
	"io"
	"log"

	"github.com/mengelbart/qlog"
	"github.com/mengelbart/roq"
	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/report"
	"github.com/pion/rtcp"
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
	ic      *interceptor.Chain
}

func newSender(conn roq.Connection, qlog *qlog.Logger) (*sender, error) {
	ri, err := report.NewSenderInterceptor()
	if err != nil {
		return nil, err
	}
	i, err := ri.NewInterceptor("")
	if err != nil {
		return nil, err
	}
	session, err := roq.NewSession(conn, true, qlog)
	if err != nil {
		return nil, err
	}
	return &sender{
		session: session,
		ic:      interceptor.NewChain([]interceptor.Interceptor{i}),
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

	var stream *roq.RTPSendStream
	rtcpFlow, err := s.session.NewSendFlow(flowID + 1)
	if err != nil {
		return err
	}
	_ = s.ic.BindRTCPWriter(interceptor.RTCPWriterFunc(func(pkts []rtcp.Packet, attributes interceptor.Attributes) (int, error) {
		// TODO
		var n int
		for _, pkt := range pkts {
			buf, err := pkt.Marshal()
			if err != nil {
				return 0, err
			}
			log.Println("sending rtcp packet")
			n += len(buf)
			if err = rtcpFlow.WriteRTPBytes(buf); err != nil {
				return n, err
			}
		}
		return n, nil
	}))
	rtpWriter := s.ic.BindLocalStream(&interceptor.StreamInfo{}, interceptor.RTPWriterFunc(func(header *rtp.Header, payload []byte, attributes interceptor.Attributes) (int, error) {
		hdrBuf, err := header.Marshal()
		if err != nil {
			return 0, err
		}
		buf := append(hdrBuf, payload...)
		switch sendMode {
		case datagramMode:
			if err = s.sendDatagram(flow, buf); err != nil {
				return 0, err
			}
		case streamPerFrameMode, singleStreamMode:
			if stream == nil {
				stream, err = flow.NewSendStream(context.Background())
				if err != nil {
					return 0, err
				}
			}
			_, err = stream.WriteRTPBytes(buf)
			if err != nil {
				return 0, err
			}
			flush, ok := attributes.Get("flush").(bool)
			if !ok {
				log.Println("WARNING: flush attribute not found")
			}
			if flush {
				if err := stream.Close(); err != nil {
					return 0, err
				}
				stream = nil
			}

		default:
			return 0, errors.New("invalid sendMode")
		}
		return len(buf), nil
	}))
	for {
		frame, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		packets := packetizer.Packetize(frame, 1)
		for i, packet := range packets {
			attrs := interceptor.Attributes{}
			if i == len(packets)-1 {
				attrs.Set("flush", true)
			} else {
				attrs.Set("flush", false)
			}
			if _, err := rtpWriter.Write(&packet.Header, packet.Payload, nil); err != nil {
				return err
			}
		}
	}
}

func (s *sender) sendDatagram(flow *roq.SendFlow, buf []byte) error {
	return flow.WriteRTPBytes(buf)
}

func (s *sender) sendDatagrams(flow *roq.SendFlow, packets []*rtp.Packet) error {
	for _, pkt := range packets {
		buf, err := pkt.Marshal()
		if err != nil {
			return err
		}
		if err := s.sendDatagram(flow, buf); err != nil {
			return err
		}
	}
	return nil
}

func (s *sender) sendStream(stream *roq.RTPSendStream, buf []byte) error {
	_, err := stream.WriteRTPBytes(buf)
	return err
}

func (s *sender) Close() error {
	return s.session.Close()
}
