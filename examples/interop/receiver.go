package main

import (
	"io"

	"github.com/mengelbart/roq"
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

func (r *receiver) receive(flowID uint64, writer io.WriteCloser) error {
	flow, err := r.session.NewReceiveFlow(flowID)
	if err != nil {
		return err
	}
	defer flow.Close()   //nolint
	defer writer.Close() //nolint
	_, err = io.Copy(writer, flow)
	return err
}

func (r *receiver) Close() error {
	return r.session.Close()
}
