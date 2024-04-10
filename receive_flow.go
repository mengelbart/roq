package roq

import (
	"errors"
	"io"
	"log"
	"time"

	"github.com/quic-go/quic-go/quicvarint"
)

type ReceiveFlow struct {
	id     uint64
	buffer chan []byte
}

func newReceiveFlow(id uint64, receiveBufferSize int) *ReceiveFlow {
	return &ReceiveFlow{
		id:     id,
		buffer: make(chan []byte, receiveBufferSize),
	}
}

func (f *ReceiveFlow) push(packet []byte) {
	select {
	case f.buffer <- packet:
	default:
	}
}

func (f *ReceiveFlow) readStream(rs ReceiveStream) {
	reader := quicvarint.NewReader(rs)

	for {
		length, err := quicvarint.Read(reader)
		if err != nil {
			if err == io.EOF {
				return
			}
			log.Printf("got unexpected error while reading length: %v", err)
			return
		}
		buf := make([]byte, length)
		n, err := io.ReadFull(reader, buf)
		if err != nil {
			panic(err)
		}
		f.push(buf[:n])
	}
}

func (f *ReceiveFlow) Read(buf []byte) (int, error) {
	select {
	case packet := <-f.buffer:
		n := copy(buf, packet)
		return n, nil
	case <-time.After(time.Second):
		// TODO: Implement real deadline
		return 0, errors.New("deadline exceeded")
	}
}

func (f *ReceiveFlow) SetReadDeadline(t time.Time) error {
	// TODO
	return nil
}

func (f *ReceiveFlow) ID() uint64 {
	return f.id
}
