package roq

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/quic-go/quic-go/quicvarint"
)

// readStream must unregister the stream when it returns, otherwise flows that
// receive one stream per frame accumulate finished streams forever.
func TestReadStreamUnregistersOnEOF(t *testing.T) {
	f := newReceiveFlow(1, 10, nil)

	for i := range int64(4) {
		payload := []byte{0x42}
		data := quicvarint.Append(nil, uint64(len(payload)))
		data = append(data, payload...)
		f.readStream(&stubReceiveStream{
			id:        i,
			data:      data,
			eof:       true,
			cancelled: make(chan struct{}),
		})
	}

	if len(f.buffer) != 4 {
		t.Errorf("flow buffered %d packets, want 4", len(f.buffer))
	}
	f.lock.Lock()
	defer f.lock.Unlock()
	if len(f.streams) != 0 {
		t.Errorf("flow still tracks %d streams after readStream returned, want 0", len(f.streams))
	}
}

func TestReadStreamUnregistersOnClose(t *testing.T) {
	f := newReceiveFlow(1, 10, nil)
	rs := &stubReceiveStream{
		id:        1,
		cancelled: make(chan struct{}),
		parked:    make(chan struct{}),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.readStream(rs)
	}()

	select {
	case <-rs.parked:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for readStream to register the stream")
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for readStream to return")
	}

	f.lock.Lock()
	defer f.lock.Unlock()
	if len(f.streams) != 0 {
		t.Errorf("flow still tracks %d streams after close, want 0", len(f.streams))
	}
}

// Read must not silently truncate: a caller whose buffer is too small for the
// next packet gets io.ErrShortBuffer instead of a mangled RTP packet.
func TestReadShortBuffer(t *testing.T) {
	f := newReceiveFlow(1, 10, nil)
	packet := []byte{0x80, 0x60, 0x00, 0x01, 0xde, 0xad, 0xbe, 0xef}
	f.push(bytes.NewBuffer(packet))

	buf := make([]byte, len(packet)-1)
	n, err := f.Read(buf)
	if !errors.Is(err, io.ErrShortBuffer) {
		t.Fatalf("Read with undersized buffer returned error %v, want io.ErrShortBuffer", err)
	}
	if n != 0 {
		t.Errorf("Read with undersized buffer returned n = %d, want 0", n)
	}
}

// A short read must not stall the flow: the truncated packet is dropped and the
// next packet is still readable.
func TestReadAfterShortBuffer(t *testing.T) {
	f := newReceiveFlow(1, 10, nil)
	first := []byte{0x80, 0x60, 0x00, 0x01}
	second := []byte{0x42}
	f.push(bytes.NewBuffer(first))
	f.push(bytes.NewBuffer(second))

	if _, err := f.Read(make([]byte, len(first)-1)); !errors.Is(err, io.ErrShortBuffer) {
		t.Fatalf("Read with undersized buffer returned error %v, want io.ErrShortBuffer", err)
	}

	buf := make([]byte, 65535)
	n, err := f.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(buf[:n], second) {
		t.Errorf("Read returned %v, want %v", buf[:n], second)
	}
}

// A buffer of exactly the packet size is not short.
func TestReadExactBuffer(t *testing.T) {
	f := newReceiveFlow(1, 10, nil)
	packet := []byte{0x80, 0x60, 0x00, 0x01, 0xde, 0xad, 0xbe, 0xef}
	f.push(bytes.NewBuffer(packet))

	buf := make([]byte, len(packet))
	n, err := f.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(buf[:n], packet) {
		t.Errorf("Read returned %v, want %v", buf[:n], packet)
	}
}
