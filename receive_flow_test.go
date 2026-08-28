package roq

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
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

// Pooled buffers must be allocated with capacity, not content: a buffer holding
// 65535 bytes only works because every Get is followed by a Reset.
func TestBufferPoolAllocatesEmptyBuffers(t *testing.T) {
	f := newReceiveFlow(1, 10, nil)

	b := f.bufferPool.Get().(*bytes.Buffer)
	if b.Len() != 0 {
		t.Errorf("pooled buffer holds %d bytes of content, want 0", b.Len())
	}
	if b.Cap() < maxPacketBufferSize {
		t.Errorf("pooled buffer has capacity %d, want at least %d", b.Cap(), maxPacketBufferSize)
	}
}

// A packet dropped because the queue is full must go back to the pool,
// otherwise overload defeats the pool entirely.
func TestPushReturnsDroppedBufferToPool(t *testing.T) {
	f := newReceiveFlow(1, 1, nil)
	allocated := countPoolAllocations(f)

	f.push(f.bufferPool.Get().(*bytes.Buffer))
	for range overload {
		f.push(f.bufferPool.Get().(*bytes.Buffer))
	}

	if len(f.buffer) != 1 {
		t.Fatalf("flow buffered %d packets, want 1", len(f.buffer))
	}
	if *allocated > maxPoolAllocations {
		t.Errorf("pool allocated %d buffers for %d dropped packets, want them recycled", *allocated, overload)
	}
}

// overload is the number of packets a pool test pushes into a flow that cannot
// queue them, and maxPoolAllocations the number of buffers the pool may
// allocate while serving them. Recycling lets a single buffer serve every push,
// so a count anywhere near overload means dropped buffers leak to the garbage
// collector instead. The bound is only half of overload because sync.Pool
// deliberately drops a quarter of all Puts under the race detector, to catch
// code that assumes a pooled value comes back.
const (
	overload           = 1000
	maxPoolAllocations = overload / 2
)

// countPoolAllocations instruments the flow's buffer pool and returns a counter
// of the buffers it had to allocate because none were pooled. It must be called
// before the flow is used.
func countPoolAllocations(f *ReceiveFlow) *int {
	var allocated int
	f.bufferPool.New = func() any {
		allocated++
		return bytes.NewBuffer(make([]byte, 0, maxPacketBufferSize))
	}
	return &allocated
}

// Read must hand out the packets that were already buffered when the flow was
// closed, instead of racing them against the cancelled context.
func TestReadDrainsBufferAfterClose(t *testing.T) {
	const packets = 20
	f := newReceiveFlow(1, packets, nil)
	for i := range packets {
		f.push(bytes.NewBuffer([]byte{byte(i)}))
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	buf := make([]byte, 1)
	for i := range packets {
		n, err := f.Read(buf)
		if err != nil {
			t.Fatalf("Read %d after close: %v, want the buffered packet", i, err)
		}
		if n != 1 || buf[0] != byte(i) {
			t.Fatalf("Read %d returned %d bytes %v, want 1 byte %v", i, n, buf[:n], i)
		}
	}
	if _, err := f.Read(buf); !errors.Is(err, context.Canceled) {
		t.Errorf("Read on drained closed flow: %v, want %v", err, context.Canceled)
	}
}

// Once Close returned, the buffer contents are final: a push that lost the
// race must be dropped rather than queued behind the close where no Read would
// ever return it.
func TestPushAfterCloseIsDropped(t *testing.T) {
	f := newReceiveFlow(1, 10, nil)
	f.push(bytes.NewBuffer([]byte{0x01}))
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A closed flow still recycles what it drops, so that a producer which
	// keeps pushing until it notices the closure does not leak every buffer.
	allocated := countPoolAllocations(f)
	for range overload {
		f.push(f.bufferPool.Get().(*bytes.Buffer))
	}
	if *allocated > maxPoolAllocations {
		t.Errorf("pool allocated %d buffers for %d packets pushed after close, want them recycled", *allocated, overload)
	}

	buf := make([]byte, 1)
	n, err := f.Read(buf)
	if err != nil || n != 1 || buf[0] != 0x01 {
		t.Fatalf("Read = %d, %v, buf %v, want the packet buffered before Close", n, err, buf[:n])
	}
	if _, err := f.Read(buf); !errors.Is(err, context.Canceled) {
		t.Fatalf("Read after close: %v, want %v", err, context.Canceled)
	}
}

// Read keeps reporting the error once a closed flow ran dry, instead of
// blocking or panicking on the closed buffer.
func TestReadAfterCloseIsRepeatable(t *testing.T) {
	f := newReceiveFlow(1, 10, nil)
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	buf := make([]byte, 1)
	for i := range 3 {
		if _, err := f.Read(buf); !errors.Is(err, context.Canceled) {
			t.Fatalf("Read %d: %v, want %v", i, err, context.Canceled)
		}
	}
}

// Sessions close their flows on shutdown, so a flow the application also closes
// itself is closed twice. That must not panic on the buffer.
func TestCloseFlowTwice(t *testing.T) {
	f := newReceiveFlow(1, 10, nil)
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// Closing a flow while its producers are still pushing must not lose a packet
// that was queued, and must not send on the closed buffer.
func TestConcurrentPushAndClose(t *testing.T) {
	f := newReceiveFlow(1, 10, nil)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		f.push(bytes.NewBuffer([]byte{0x01}))
	}()
	go func() {
		defer wg.Done()
		_ = f.Close()
	}()
	wg.Wait()

	// The push either won the race, in which case Read must hand the
	// packet out, or it lost it and Read reports the closure. Both are
	// correct; a queued packet that Read never returns is not.
	buf := make([]byte, 1)
	n, err := f.Read(buf)
	if err == nil {
		if n != 1 || buf[0] != 0x01 {
			t.Fatalf("Read = %d bytes %v, want the pushed packet", n, buf[:n])
		}
		if _, err := f.Read(buf); !errors.Is(err, context.Canceled) {
			t.Fatalf("Read on drained flow: %v, want %v", err, context.Canceled)
		}
		return
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Read: %v, want %v", err, context.Canceled)
	}
	if len(f.buffer) != 0 {
		t.Fatalf("closed flow still holds %d packets no Read can return", len(f.buffer))
	}
}

// readStream must recognise wrapped errors: a wrapped io.EOF ends the stream
// quietly, and a wrapped stream error is reported once, not twice.
func TestReadStreamWrappedErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{{
		name: "wrapped EOF",
		err:  fmt.Errorf("read failed: %w", io.EOF),
		want: "",
	}, {
		name: "wrapped stream error",
		err:  fmt.Errorf("read failed: %w", &quic.StreamError{StreamID: 1, ErrorCode: 42}),
		want: "got stream error while reading length",
	}, {
		name: "other error",
		err:  errors.New("boom"),
		want: "got unexpected error while reading length",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			log.SetOutput(&logs)
			defer log.SetOutput(os.Stderr)

			f := newReceiveFlow(1, 10, nil)
			payload := []byte{0x42}
			data := quicvarint.Append(nil, uint64(len(payload)))
			data = append(data, payload...)
			f.readStream(&stubReceiveStream{
				id:        1,
				data:      data,
				readErr:   tc.err,
				cancelled: make(chan struct{}),
			})

			if len(f.buffer) != 1 {
				t.Errorf("flow buffered %d packets, want 1", len(f.buffer))
			}
			lines := 0
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				if line != "" {
					lines++
				}
			}
			if tc.want == "" {
				if lines != 0 {
					t.Errorf("logged %q, want nothing", logs.String())
				}
				return
			}
			if lines != 1 {
				t.Errorf("logged %d lines, want 1:\n%s", lines, logs.String())
			}
			if !strings.Contains(logs.String(), tc.want) {
				t.Errorf("logged %q, want it to contain %q", logs.String(), tc.want)
			}
		})
	}
}

// A deadline that passes while Read is blocked must unblock it.
func TestReadDeadlineExpires(t *testing.T) {
	f := newReceiveFlow(1, 10, nil)
	if err := f.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.Read(make([]byte, 100))
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("Read error = %v, want os.ErrDeadlineExceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Read did not return after the deadline passed")
	}
	// The deadline stays expired until it is extended or cleared.
	if _, err := f.Read(make([]byte, 100)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("second Read error = %v, want os.ErrDeadlineExceeded", err)
	}
}

// A deadline in the past times out immediately, even with a packet buffered.
func TestReadDeadlineInThePast(t *testing.T) {
	f := newReceiveFlow(1, 10, nil)
	f.push(bytes.NewBuffer([]byte{0x42}))
	if err := f.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	if _, err := f.Read(make([]byte, 100)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("Read error = %v, want os.ErrDeadlineExceeded", err)
	}
	// Clearing the deadline makes the buffered packet readable again.
	if err := f.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	n, err := f.Read(make([]byte, 100))
	if err != nil || n != 1 {
		t.Errorf("Read = %v, %v, want 1, nil", n, err)
	}
}

// Setting a deadline must also apply to a Read that is already blocked.
func TestSetReadDeadlineWhileReadBlocked(t *testing.T) {
	f := newReceiveFlow(1, 10, nil)
	done := make(chan error, 1)
	go func() {
		_, err := f.Read(make([]byte, 100))
		done <- err
	}()
	// Give the reader a chance to block on the empty buffer first.
	time.Sleep(10 * time.Millisecond)
	if err := f.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("Read error = %v, want os.ErrDeadlineExceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked Read did not pick up the new deadline")
	}
}

// Extending the deadline of a blocked reader must keep it blocked, and the
// replaced deadline must not fire on the new one.
func TestExtendReadDeadlineWhileReadBlocked(t *testing.T) {
	f := newReceiveFlow(1, 10, nil)
	if err := f.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.Read(make([]byte, 100))
		done <- err
	}()
	if err := f.SetReadDeadline(time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("Read returned %v, want it to keep waiting for the extended deadline", err)
	case <-time.After(100 * time.Millisecond):
	}
	f.push(bytes.NewBuffer([]byte{0x42}))
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Read error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Read did not return the pushed packet")
	}
}

// A deadline must not keep Read from reporting the closed flow.
func TestReadDeadlineAndClose(t *testing.T) {
	f := newReceiveFlow(1, 10, nil)
	if err := f.SetReadDeadline(time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.Read(make([]byte, 100))
		done <- err
	}()
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Read error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Read did not return after the flow was closed")
	}
}

// Racing SetReadDeadline against readers and expiring timers must not panic on
// a double close of the deadline channel.
func TestConcurrentSetReadDeadline(t *testing.T) {
	f := newReceiveFlow(1, 10, nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				if err := f.SetReadDeadline(time.Now().Add(time.Millisecond)); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 100 {
			_, _ = f.Read(make([]byte, 100))
		}
	}()
	wg.Wait()
}
