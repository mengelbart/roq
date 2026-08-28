package roq

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/quic-go/quic-go/quicvarint"
)

var errWriteFailed = errors.New("write failed")

// acceptor builds a writeFn that accepts the given number of bytes on each
// successive call and returns err whenever it accepts fewer than it was given.
// Calls past the end of the script accept everything.
func acceptor(err error, accept ...int) func([]byte) (int, error) {
	var mutex sync.Mutex
	call := 0
	return func(p []byte) (int, error) {
		mutex.Lock()
		defer mutex.Unlock()
		n := len(p)
		if call < len(accept) {
			n = min(accept[call], len(p))
		}
		call++
		if n < len(p) {
			return n, err
		}
		return n, nil
	}
}

func newTestSendStream(t *testing.T, flowID uint64, writeFn func([]byte) (int, error)) (*RTPSendStream, *stubSendStream) {
	t.Helper()
	st := &stubSendStream{id: 1, writeFn: writeFn}
	s, err := newRTPSendStream(st, flowID, quicvarint.Append(nil, flowID), nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, st
}

// parseFrames reads back everything the peer would have seen: one flow ID
// varint, then a length-prefixed payload per packet. It fails the test if the
// bytes do not decode, which is what a corrupted or resumed-wrongly stream
// looks like from the other end.
func parseFrames(t *testing.T, st *stubSendStream, wantFlowID uint64) [][]byte {
	t.Helper()
	wire := st.wire()
	if len(wire) == 0 {
		return nil
	}
	r := bytes.NewReader(wire)
	id, err := quicvarint.Read(r)
	if err != nil {
		t.Fatalf("reading flow ID: %v", err)
	}
	if id != wantFlowID {
		t.Fatalf("flow ID = %v, want %v", id, wantFlowID)
	}
	frames := [][]byte{}
	for r.Len() > 0 {
		n, err := quicvarint.Read(r)
		if err != nil {
			t.Fatalf("reading length of frame %v: %v", len(frames), err)
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			t.Fatalf("reading payload of frame %v: %v", len(frames), err)
		}
		frames = append(frames, payload)
	}
	return frames
}

// A short write must report the bytes of the packet that actually reached the
// stream, not the full packet length.
func TestWriteRTPBytesShortWrite(t *testing.T) {
	// flow ID 0 and a length varint of 1 byte each, so the payload starts at
	// offset 2 and accepting 5 bytes delivers 3 bytes of the packet.
	s, _ := newTestSendStream(t, 0, acceptor(errWriteFailed, 5))

	n, err := s.WriteRTPBytes(make([]byte, 10))
	if !errors.Is(err, errWriteFailed) {
		t.Errorf("WriteRTPBytes() error = %v, want %v", err, errWriteFailed)
	}
	if n != 3 {
		t.Errorf("WriteRTPBytes() = %v, want 3", n)
	}
}

// A write that accepts a short count without reporting an error must still be
// reported as a short write.
func TestWriteRTPBytesShortWriteWithoutError(t *testing.T) {
	s, _ := newTestSendStream(t, 0, func(p []byte) (int, error) {
		return len(p) - 1, nil
	})

	if _, err := s.WriteRTPBytes(make([]byte, 10)); !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("WriteRTPBytes() error = %v, want %v", err, io.ErrShortWrite)
	}
}

// The tail a partial write left behind must go out ahead of the next packet, so
// that the peer sees both frames intact.
func TestWriteRTPBytesResumesAfterPartialWrite(t *testing.T) {
	s, st := newTestSendStream(t, 0, acceptor(errWriteFailed, 4))
	first := bytes.Repeat([]byte{0xaa}, 8)
	second := bytes.Repeat([]byte{0xbb}, 8)

	if n, err := s.WriteRTPBytes(first); n != 2 || !errors.Is(err, errWriteFailed) {
		t.Fatalf("WriteRTPBytes() = (%v, %v), want (2, %v)", n, err, errWriteFailed)
	}
	if n, err := s.WriteRTPBytes(second); n != len(second) || err != nil {
		t.Fatalf("WriteRTPBytes() = (%v, %v), want (%v, nil)", n, err, len(second))
	}

	frames := parseFrames(t, st, 0)
	if len(frames) != 2 {
		t.Fatalf("peer received %v frames, want 2", len(frames))
	}
	if !bytes.Equal(frames[0], first) {
		t.Errorf("frame 0 = %x, want %x", frames[0], first)
	}
	if !bytes.Equal(frames[1], second) {
		t.Errorf("frame 1 = %x, want %x", frames[1], second)
	}
}

// A write that stops inside the flow ID varint must have the rest of the flow
// ID resumed, not a second copy of it prepended.
func TestWriteRTPBytesResumesPartialFlowID(t *testing.T) {
	const flowID = 1000 // two bytes as a varint
	s, st := newTestSendStream(t, flowID, acceptor(errWriteFailed, 1))
	first := bytes.Repeat([]byte{0xaa}, 8)
	second := bytes.Repeat([]byte{0xbb}, 8)

	// The write stopped before any of the packet was delivered.
	if n, err := s.WriteRTPBytes(first); n != 0 || !errors.Is(err, errWriteFailed) {
		t.Fatalf("WriteRTPBytes() = (%v, %v), want (0, %v)", n, err, errWriteFailed)
	}
	if n, err := s.WriteRTPBytes(second); n != len(second) || err != nil {
		t.Fatalf("WriteRTPBytes() = (%v, %v), want (%v, nil)", n, err, len(second))
	}

	frames := parseFrames(t, st, flowID)
	if len(frames) != 2 {
		t.Fatalf("peer received %v frames, want 2", len(frames))
	}
	if !bytes.Equal(frames[0], first) || !bytes.Equal(frames[1], second) {
		t.Errorf("frames = [%x %x], want [%x %x]", frames[0], frames[1], first, second)
	}
}

// The flow ID prefixes the first frame only.
func TestWriteRTPBytesFlowIDOnlyOnce(t *testing.T) {
	const flowID = 1000
	s, st := newTestSendStream(t, flowID, nil)
	packet := bytes.Repeat([]byte{0xaa}, 8)

	for range 2 {
		if _, err := s.WriteRTPBytes(packet); err != nil {
			t.Fatal(err)
		}
	}

	flowIDBytes := quicvarint.Append(nil, flowID)
	want := len(flowIDBytes) + 2*(1+len(packet))
	if got := len(st.wire()); got != want {
		t.Errorf("peer received %v bytes, want %v (flow ID sent more than once?)", got, want)
	}
	if frames := parseFrames(t, st, flowID); len(frames) != 2 {
		t.Errorf("peer received %v frames, want 2", len(frames))
	}
}

// A stream whose writes never succeed keeps failing, and never puts a packet
// on the wire behind the frame it could not finish.
func TestWriteRTPBytesPermanentFailure(t *testing.T) {
	s, st := newTestSendStream(t, 0, func([]byte) (int, error) {
		return 0, errWriteFailed
	})

	for i := range 3 {
		n, err := s.WriteRTPBytes(bytes.Repeat([]byte{byte(i)}, 8))
		if n != 0 || !errors.Is(err, errWriteFailed) {
			t.Fatalf("WriteRTPBytes() = (%v, %v), want (0, %v)", n, err, errWriteFailed)
		}
	}
	if got := len(st.wire()); got != 0 {
		t.Errorf("peer received %v bytes, want 0", got)
	}
	if got := st.writes(); got != 3 {
		t.Errorf("made %v Write calls, want 3 (one attempt per packet)", got)
	}
}

// Concurrent writers must not interleave their frames: every packet arrives
// whole and exactly once.
func TestConcurrentWriteRTPBytes(t *testing.T) {
	const (
		writers        = 8
		packetsPerCall = 50
		payloadLen     = 8
	)
	s, st := newTestSendStream(t, 0, nil)

	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range packetsPerCall {
				packet := make([]byte, payloadLen)
				binary.BigEndian.PutUint16(packet, uint16(w*packetsPerCall+i))
				if n, err := s.WriteRTPBytes(packet); n != payloadLen || err != nil {
					t.Errorf("WriteRTPBytes() = (%v, %v), want (%v, nil)", n, err, payloadLen)
					return
				}
			}
		}()
	}
	wg.Wait()

	frames := parseFrames(t, st, 0)
	if len(frames) != writers*packetsPerCall {
		t.Fatalf("peer received %v frames, want %v", len(frames), writers*packetsPerCall)
	}
	seen := make(map[uint16]int, len(frames))
	for i, f := range frames {
		if len(f) != payloadLen {
			t.Fatalf("frame %v has length %v, want %v", i, len(f), payloadLen)
		}
		seen[binary.BigEndian.Uint16(f)]++
	}
	for i := range uint16(writers * packetsPerCall) {
		if seen[i] != 1 {
			t.Errorf("packet %v arrived %v times, want once", i, seen[i])
		}
	}
}
