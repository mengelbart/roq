package roq

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/mengelbart/roq/qlog"
	"github.com/quic-go/quic-go/qlogwriter"
	"github.com/quic-go/quic-go/quicvarint"
)

// fakeTrace is a qlogwriter.Trace that hands out recorders collecting events in
// memory. schemas are the event schemas it claims to support.
type fakeTrace struct {
	schemas []string

	mutex     sync.Mutex
	recorders []*fakeRecorder
}

func newFakeTrace() *fakeTrace {
	return &fakeTrace{schemas: []string{qlog.EventSchema}}
}

func (t *fakeTrace) SupportsSchemas(schema string) bool {
	for _, s := range t.schemas {
		if s == schema {
			return true
		}
	}
	return false
}

func (t *fakeTrace) AddProducer() qlogwriter.Recorder {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	r := &fakeRecorder{}
	t.recorders = append(t.recorders, r)
	return r
}

// only returns the single recorder the trace handed out.
func (t *fakeTrace) only(tb testing.TB) *fakeRecorder {
	tb.Helper()
	t.mutex.Lock()
	defer t.mutex.Unlock()
	if len(t.recorders) != 1 {
		tb.Fatalf("got %v producers, want 1", len(t.recorders))
	}
	return t.recorders[0]
}

type fakeRecorder struct {
	mutex  sync.Mutex
	events []qlogwriter.Event
	closes int
}

func (r *fakeRecorder) RecordEvent(e qlogwriter.Event) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.events = append(r.events, e)
}

func (r *fakeRecorder) Close() error {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.closes++
	return nil
}

func (r *fakeRecorder) recorded() []qlogwriter.Event {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return append([]qlogwriter.Event{}, r.events...)
}

func (r *fakeRecorder) closed() int {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.closes
}

// awaitEvent waits for an event of the same type as want and returns it.
func (r *fakeRecorder) awaitEvent(t *testing.T, want qlogwriter.Event) qlogwriter.Event {
	t.Helper()
	return r.awaitEventAfter(t, want, 0)
}

// awaitEventAfter waits for the event of the same type as want that follows the
// first skip of them, and returns it.
func (r *fakeRecorder) awaitEventAfter(t *testing.T, want qlogwriter.Event, skip int) qlogwriter.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		seen := 0
		for _, e := range r.recorded() {
			if e.Name() != want.Name() {
				continue
			}
			if seen == skip {
				return e
			}
			seen++
		}
		if time.Now().After(deadline) {
			t.Fatalf("event %v #%v was never recorded, got %v", want.Name(), skip, r.recorded())
		}
		time.Sleep(time.Millisecond)
	}
}

// qlogStubConn is a stubConn that exposes a qlog trace, like QUICGoConnection.
type qlogStubConn struct {
	*stubConn
	trace qlogwriter.Trace
}

func newQlogStubConn(trace qlogwriter.Trace) *qlogStubConn {
	return &qlogStubConn{stubConn: newStubConn(), trace: trace}
}

func (c *qlogStubConn) QlogTrace() qlogwriter.Trace { return c.trace }

func TestQlogTraceFromConnection(t *testing.T) {
	tr := newFakeTrace()
	c := newQlogStubConn(tr)
	s, err := NewSession(c, true)
	if err != nil {
		t.Fatal(err)
	}
	defer closeWithin(t, s, time.Second)

	if s.qlog == nil {
		t.Fatal("session did not take a producer from the connection's trace")
	}
	tr.only(t)
}

// A connection without a trace, or with one that does not carry the RoQ event
// schema, must leave the session without a recorder.
func TestQlogWithoutRoQSchema(t *testing.T) {
	for _, tc := range []struct {
		name string
		conn Connection
	}{
		{"connection without a trace", newQlogStubConn(nil)},
		{"trace without the RoQ schema", newQlogStubConn(&fakeTrace{schemas: []string{"urn:ietf:params:qlog:events:quic-12"}})},
		{"connection that cannot report a trace", newStubConn()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := NewSession(tc.conn, true)
			if err != nil {
				t.Fatal(err)
			}
			defer closeWithin(t, s, time.Second)
			if s.qlog != nil {
				t.Error("session took a producer, want none")
			}
		})
	}
}

// An explicitly configured trace wins over the one the connection reports.
func TestQlogTraceOptionOverridesConnection(t *testing.T) {
	fromConn, fromOption := newFakeTrace(), newFakeTrace()
	c := newQlogStubConn(fromConn)
	s, err := NewSession(c, true, WithQlogTrace(fromOption))
	if err != nil {
		t.Fatal(err)
	}
	defer closeWithin(t, s, time.Second)

	fromOption.only(t)
	fromConn.mutex.Lock()
	defer fromConn.mutex.Unlock()
	if len(fromConn.recorders) != 0 {
		t.Errorf("got %v producers on the connection's trace, want 0", len(fromConn.recorders))
	}
}

// Close releases the producer, which is what closes the underlying trace. It is
// idempotent, so a second Close must not close the producer again.
func TestQlogProducerClosedOnce(t *testing.T) {
	tr := newFakeTrace()
	s, err := NewSession(newQlogStubConn(tr), true)
	if err != nil {
		t.Fatal(err)
	}
	r := tr.only(t)
	closeWithin(t, s, time.Second)
	closeWithin(t, s, time.Second)
	if got := r.closed(); got != 1 {
		t.Errorf("producer closed %v times, want 1", got)
	}
}

func TestQlogDatagramEvents(t *testing.T) {
	tr := newFakeTrace()
	c := newQlogStubConn(tr)
	s, err := NewSession(c, true)
	if err != nil {
		t.Fatal(err)
	}
	defer closeWithin(t, s, time.Second)
	r := tr.only(t)

	f, err := s.NewSendFlow(1)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.WriteRTPBytes(make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	created := r.awaitEvent(t, qlog.DatagramPacketCreated{}).(qlog.DatagramPacketCreated)
	want := qlog.DatagramPacketCreated{
		Packet: qlog.Packet{
			FlowID: 1,
			Length: 101,
			Raw:    qlog.RawInfo{Length: 101, PayloadLength: 100},
		},
	}
	if !reflect.DeepEqual(created, want) {
		t.Errorf("got %+v, want %+v", created, want)
	}

	// The flow ID varint is not part of the RTP packet, so it counts towards
	// the raw length only.
	c.datagrams <- append(quicvarint.Append(nil, 1), make([]byte, 100)...)
	parsed := r.awaitEvent(t, qlog.DatagramPacketParsed{}).(qlog.DatagramPacketParsed)
	if !reflect.DeepEqual(parsed.Packet, want.Packet) {
		t.Errorf("got %+v, want %+v", parsed.Packet, want.Packet)
	}
}

func TestQlogStreamEvents(t *testing.T) {
	tr := newFakeTrace()
	c := newQlogStubConn(tr)
	s, err := NewSession(c, true)
	if err != nil {
		t.Fatal(err)
	}
	defer closeWithin(t, s, time.Second)
	r := tr.only(t)

	f, err := s.NewSendFlow(1)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := f.NewSendStream(t.Context(), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stream.WriteRTPBytes(make([]byte, 100)); err != nil {
		t.Fatal(err)
	}

	opened := r.awaitEvent(t, qlog.StreamOpened{}).(qlog.StreamOpened)
	if want := (qlog.StreamOpened{FlowID: 1, StreamID: 1}); opened != want {
		t.Errorf("got %+v, want %+v", opened, want)
	}
	created := r.awaitEvent(t, qlog.StreamPacketCreated{}).(qlog.StreamPacketCreated)
	// The length prefix (2 bytes for 100) counts towards the raw length only.
	// The flow ID does not: it is sent once per stream, not once per frame.
	want := qlog.StreamPacketCreated{
		StreamID: 1,
		Packet: qlog.Packet{
			FlowID: 1,
			Length: 102,
			Raw:    qlog.RawInfo{Length: 102, PayloadLength: 100},
		},
	}
	if !reflect.DeepEqual(created, want) {
		t.Errorf("got %+v, want %+v", created, want)
	}

	rs := &stubReceiveStream{
		id:        2,
		data:      append(quicvarint.Append(quicvarint.Append(nil, 1), 100), make([]byte, 100)...),
		eof:       true,
		cancelled: make(chan struct{}),
	}
	c.streams <- rs
	parsed := r.awaitEvent(t, qlog.StreamPacketParsed{}).(qlog.StreamPacketParsed)
	wantParsed := qlog.StreamPacketParsed{
		StreamID: 2,
		Packet: qlog.Packet{
			FlowID: 1,
			Length: 102,
			Raw:    qlog.RawInfo{Length: 102, PayloadLength: 100},
		},
	}
	if !reflect.DeepEqual(parsed, wantParsed) {
		t.Errorf("got %+v, want %+v", parsed, wantParsed)
	}
}

// Without WithQlogPacketData a session logs packet lengths but no wire image.
func TestQlogPacketDataOffByDefault(t *testing.T) {
	tr := newFakeTrace()
	c := newQlogStubConn(tr)
	s, err := NewSession(c, true)
	if err != nil {
		t.Fatal(err)
	}
	defer closeWithin(t, s, time.Second)
	r := tr.only(t)

	c.datagrams <- append(quicvarint.Append(nil, 1), make([]byte, 100)...)
	parsed := r.awaitEvent(t, qlog.DatagramPacketParsed{}).(qlog.DatagramPacketParsed)
	if parsed.Packet.Raw.Data != nil {
		t.Errorf("got packet data %v, want none", parsed.Packet.Raw.Data)
	}
}

func TestQlogPacketData(t *testing.T) {
	packet := []byte{0xde, 0xad, 0xbe, 0xef}
	for _, tc := range []struct {
		name     string
		maxBytes int
		// wantDatagram is the datagram wire image: the flow ID varint followed
		// by the packet. wantFrame is the stream wire image: the length varint
		// followed by the packet, the same on both sides of the stream.
		wantDatagram []byte
		wantFrame    []byte
	}{
		{
			name:         "whole packet",
			maxBytes:     0,
			wantDatagram: []byte{0x01, 0xde, 0xad, 0xbe, 0xef},
			wantFrame:    []byte{0x04, 0xde, 0xad, 0xbe, 0xef},
		},
		{
			name:         "truncated",
			maxBytes:     3,
			wantDatagram: []byte{0x01, 0xde, 0xad},
			wantFrame:    []byte{0x04, 0xde, 0xad},
		},
		{
			name:         "limit above the packet size",
			maxBytes:     64,
			wantDatagram: []byte{0x01, 0xde, 0xad, 0xbe, 0xef},
			wantFrame:    []byte{0x04, 0xde, 0xad, 0xbe, 0xef},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newFakeTrace()
			c := newQlogStubConn(tr)
			s, err := NewSession(c, true, WithQlogPacketData(tc.maxBytes))
			if err != nil {
				t.Fatal(err)
			}
			defer closeWithin(t, s, time.Second)
			r := tr.only(t)

			f, err := s.NewSendFlow(1)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.WriteRTPBytes(packet); err != nil {
				t.Fatal(err)
			}
			created := r.awaitEvent(t, qlog.DatagramPacketCreated{}).(qlog.DatagramPacketCreated)
			if !reflect.DeepEqual(created.Packet.Raw.Data, tc.wantDatagram) {
				t.Errorf("sent datagram data = %x, want %x", created.Packet.Raw.Data, tc.wantDatagram)
			}
			// The lengths always describe the complete packet, even when the
			// data logged for it was truncated.
			if created.Packet.Length != 5 || created.Packet.Raw.Length != 5 || created.Packet.Raw.PayloadLength != 4 {
				t.Errorf("got %+v, want length 5 and payload length 4", created.Packet)
			}

			c.datagrams <- append(quicvarint.Append(nil, 1), packet...)
			parsedDgram := r.awaitEvent(t, qlog.DatagramPacketParsed{}).(qlog.DatagramPacketParsed)
			if !reflect.DeepEqual(parsedDgram.Packet.Raw.Data, tc.wantDatagram) {
				t.Errorf("received datagram data = %x, want %x", parsedDgram.Packet.Raw.Data, tc.wantDatagram)
			}

			stream, err := f.NewSendStream(t.Context(), 0, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = stream.WriteRTPBytes(packet); err != nil {
				t.Fatal(err)
			}
			sent := r.awaitEvent(t, qlog.StreamPacketCreated{}).(qlog.StreamPacketCreated)
			if !reflect.DeepEqual(sent.Packet.Raw.Data, tc.wantFrame) {
				t.Errorf("sent stream data = %x, want %x", sent.Packet.Raw.Data, tc.wantFrame)
			}
			// Only the first packet on a stream is preceded by the flow ID, so
			// a later one has to log the same wire image as the first.
			if _, err = stream.WriteRTPBytes(packet); err != nil {
				t.Fatal(err)
			}
			second := r.awaitEventAfter(t, qlog.StreamPacketCreated{}, 1).(qlog.StreamPacketCreated)
			if !reflect.DeepEqual(second.Packet.Raw.Data, tc.wantFrame) {
				t.Errorf("second sent stream data = %x, want %x", second.Packet.Raw.Data, tc.wantFrame)
			}

			rs := &stubReceiveStream{
				id:        2,
				data:      append(quicvarint.Append(quicvarint.Append(nil, 1), uint64(len(packet))), packet...),
				eof:       true,
				cancelled: make(chan struct{}),
			}
			c.streams <- rs
			parsed := r.awaitEvent(t, qlog.StreamPacketParsed{}).(qlog.StreamPacketParsed)
			if !reflect.DeepEqual(parsed.Packet.Raw.Data, tc.wantFrame) {
				t.Errorf("received stream data = %x, want %x", parsed.Packet.Raw.Data, tc.wantFrame)
			}
		})
	}
}

// A QUIC varint may use a longer encoding than the value needs. The receiver
// has to report the frame as it was on the wire, not as it would have written
// it, so its lengths and data follow the encoding the sender chose.
func TestQlogNonMinimalLengthVarint(t *testing.T) {
	tr := newFakeTrace()
	c := newQlogStubConn(tr)
	s, err := NewSession(c, true, WithQlogPacketData(0))
	if err != nil {
		t.Fatal(err)
	}
	defer closeWithin(t, s, time.Second)
	r := tr.only(t)

	packet := []byte{0xde, 0xad, 0xbe, 0xef}
	// The length of 4 fits in one byte, but is encoded in eight here.
	prefix := quicvarint.AppendWithLen(nil, uint64(len(packet)), 8)
	c.streams <- &stubReceiveStream{
		id:        2,
		data:      append(quicvarint.Append(nil, 1), append(prefix, packet...)...),
		eof:       true,
		cancelled: make(chan struct{}),
	}

	parsed := r.awaitEvent(t, qlog.StreamPacketParsed{}).(qlog.StreamPacketParsed)
	want := qlog.StreamPacketParsed{
		StreamID: 2,
		Packet: qlog.Packet{
			FlowID: 1,
			// Eight bytes of length prefix, not the one the value needs.
			Length: 12,
			Raw: qlog.RawInfo{
				Length:        12,
				PayloadLength: 4,
				Data:          append(append([]byte{}, prefix...), packet...),
			},
		},
	}
	if !reflect.DeepEqual(parsed, want) {
		t.Errorf("got %+v, want %+v", parsed, want)
	}
}
