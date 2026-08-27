package roq

import (
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
