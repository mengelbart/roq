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
	errClosed        = errors.New("session closed")
	errNilConnection = errors.New("nil connection")
	errInvalidOption = errors.New("invalid option")
)

// SessionError is the error returned by operations on a closed session. It
// carries the RoQ error code and reason the session was closed with, which
// callers can inspect with errors.As.
type SessionError struct {
	code   uint64
	reason string
}

// Code returns the RoQ error code the session was closed with, e.g.
// ErrRoQPacketError.
func (e SessionError) Code() uint64 {
	return e.code
}

// Reason returns the reason phrase the session was closed with. It may be
// empty.
func (e SessionError) Reason() string {
	return e.reason
}

func (e SessionError) Error() string {
	return fmt.Sprintf("roq session error %v: %v", e.code, e.reason)
}
