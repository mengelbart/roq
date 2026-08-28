package qlog

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go/qlogwriter"
	"github.com/quic-go/quic-go/qlogwriter/jsontext"
)

func encode(t *testing.T, e qlogwriter.Event) string {
	t.Helper()
	buf := &bytes.Buffer{}
	if err := e.Encode(jsontext.NewEncoder(buf), time.Now()); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// The encoder terminates each top-level value with a newline.
	return strings.TrimSuffix(buf.String(), "\n")
}

func TestEventEncoding(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event qlogwriter.Event
		want  string
	}{
		{
			name:  "stream opened",
			event: StreamOpened{FlowID: 1, StreamID: 2},
			want:  `{"flow_id":1,"stream_id":2}`,
		},
		{
			name: "stream packet created",
			event: StreamPacketCreated{
				StreamID: 4,
				Packet:   Packet{FlowID: 1, Length: 1203, Raw: RawInfo{Length: 1203, PayloadLength: 1200}},
			},
			want: `{"stream_id":4,"packet":{"flow_id":1,"length":1203,"raw":{"length":1203,"payload_length":1200}}}`,
		},
		{
			name: "stream packet parsed",
			event: StreamPacketParsed{
				StreamID: 4,
				Packet:   Packet{FlowID: 1, Length: 1203, Raw: RawInfo{Length: 1203, PayloadLength: 1200}},
			},
			want: `{"stream_id":4,"packet":{"flow_id":1,"length":1203,"raw":{"length":1203,"payload_length":1200}}}`,
		},
		{
			name:  "datagram packet created",
			event: DatagramPacketCreated{Packet: Packet{FlowID: 7, Length: 101, Raw: RawInfo{Length: 101, PayloadLength: 100}}},
			want:  `{"packet":{"flow_id":7,"length":101,"raw":{"length":101,"payload_length":100}}}`,
		},
		{
			name:  "datagram packet parsed",
			event: DatagramPacketParsed{Packet: Packet{FlowID: 7, Length: 101, Raw: RawInfo{Length: 101, PayloadLength: 100}}},
			want:  `{"packet":{"flow_id":7,"length":101,"raw":{"length":101,"payload_length":100}}}`,
		},
		{
			name: "packet data",
			event: DatagramPacketParsed{Packet: Packet{
				FlowID: 1,
				Length: 5,
				Raw:    RawInfo{Length: 5, PayloadLength: 4, Data: []byte{0x01, 0xde, 0xad, 0xbe, 0xef}},
			}},
			want: `{"packet":{"flow_id":1,"length":5,"raw":{"length":5,"payload_length":4,"data":"01deadbeef"}}}`,
		},
		{
			name: "truncated packet data",
			event: DatagramPacketParsed{Packet: Packet{
				FlowID: 1,
				Length: 101,
				Raw:    RawInfo{Length: 101, PayloadLength: 100, Data: []byte{0x01, 0xde}},
			}},
			want: `{"packet":{"flow_id":1,"length":101,"raw":{"length":101,"payload_length":100,"data":"01de"}}}`,
		},
		{
			name:  "packet without raw info",
			event: DatagramPacketParsed{Packet: Packet{FlowID: 0, Length: 0}},
			want:  `{"packet":{"flow_id":0,"length":0}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := encode(t, tc.event); got != tc.want {
				t.Errorf("Encode() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEventNames(t *testing.T) {
	for _, tc := range []struct {
		event qlogwriter.Event
		want  string
	}{
		{StreamOpened{}, "roq:stream_opened"},
		{StreamPacketCreated{}, "roq:stream_packet_created"},
		{StreamPacketParsed{}, "roq:stream_packet_parsed"},
		{DatagramPacketCreated{}, "roq:datagram_packet_created"},
		{DatagramPacketParsed{}, "roq:datagram_packet_parsed"},
	} {
		if got := tc.event.Name(); got != tc.want {
			t.Errorf("Name() = %v, want %v", got, tc.want)
		}
	}
}
