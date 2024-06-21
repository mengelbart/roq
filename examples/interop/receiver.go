package main

import (
	"io"

	"github.com/mengelbart/roq"
)

const (
	bufferSize = 1 << 15
)

type receiver struct {
	session *roq.Session
}

func newReceiver(conn roq.Connection) (*receiver, error) {
	session, err := roq.NewSession(conn, true)
	if err != nil {
		return nil, err
	}
	return &receiver{
		session: session,
	}, err
}

func (r *receiver) receive(flowID uint64, writer io.Writer) error {
	flow, err := r.session.NewReceiveFlow(flowID)
	if err != nil {
		return err
	}
	defer flow.Close()
	buf := make([]byte, bufferSize)
	for {
		n, err := flow.Read(buf)
		if err != nil {
			return err
		}
		if _, err := writer.Write(buf[:n]); err != nil {
			return err
		}
	}
}

func (r *receiver) Close() error {
	return r.session.Close()
}
