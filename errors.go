package roq

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
