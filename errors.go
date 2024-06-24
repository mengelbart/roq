package roq

import (
	"errors"
	"fmt"
)

const (
	ErrRoQNoError = iota
	ErrRoQGeneralError
	ErrRoQInternalError
	ErrRoQPacketError
	ErrRoQStreamCreationError
	ErrRoQFrameCancelled
	ErrRoQUnknownFlowID
	ErrRoQExpectationUnmet
)

var (
	errClosed = errors.New("session closed")
)

type SessionError struct {
	code   uint64
	reason string
}

func (e SessionError) Error() string {
	return fmt.Sprintf("roq session error %v: %v", e.code, e.reason)
}
