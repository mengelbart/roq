package main

import (
	"io"
	"log"

	"github.com/mengelbart/qlog"
	"github.com/mengelbart/roq"
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
)

const (
	bufferSize = 1 << 15
)

type receiver struct {
	session *roq.Session
	ic      *interceptor.Chain
}

func newReceiver(conn roq.Connection, qlog *qlog.Logger) (*receiver, error) {
	session, err := roq.NewSession(conn, true, qlog)
	if err != nil {
		return nil, err
	}
	return &receiver{
		session: session,
		ic:      interceptor.NewChain([]interceptor.Interceptor{}),
	}, err
}

func (r *receiver) receive(flowID uint64, writer io.WriteCloser) error {
	flow, err := r.session.NewReceiveFlow(flowID)
	if err != nil {
		return err
	}
	defer flow.Close()
	reader := r.ic.BindRemoteStream(&interceptor.StreamInfo{}, interceptor.RTCPReaderFunc(func(b []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) {
		n, err := flow.Read(b)
		return n, a, err
	}))

	defer writer.Close()
	readBuf := make([]byte, 64_000)
	for {
		n, _, err := reader.Read(readBuf, interceptor.Attributes{})
		if err != nil {
			return err
		}
		if _, err := writer.Write(readBuf[:n]); err != nil {
			return err
		}
	}
}

func (r *receiver) receiveRTCP(flowID uint64) error {
	flow, err := r.session.NewReceiveFlow(flowID)
	if err != nil {
		return err
	}
	reader := r.ic.BindRTCPReader(interceptor.RTCPReaderFunc(func(b []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) {
		n, err := flow.Read(b)
		return n, a, err
	}))
	go func() {
		for {
			buf := make([]byte, 1500)
			n, _, err := reader.Read(buf, interceptor.Attributes{})
			if err != nil {
				panic(err)
			}
			pkts, err := rtcp.Unmarshal(buf[:n])
			if err != nil {
				panic(err)
			}
			log.Printf("got rtcp reports: %v", pkts)
		}
	}()
	return nil
}

func (r *receiver) Close() error {
	return r.session.Close()
}
