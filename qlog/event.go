package qlog

import (
	"encoding/hex"
	"time"

	"github.com/quic-go/quic-go/qlogwriter/jsontext"
)

// encoderHelper writes tokens to an encoder, remembering the first error so that
// a sequence of writes only has to be checked once.
type encoderHelper struct {
	enc *jsontext.Encoder
	err error
}

func (h *encoderHelper) WriteToken(t jsontext.Token) {
	if h.err != nil {
		return
	}
	h.err = h.enc.WriteToken(t)
}

// RawInfo describes the wire image of a packet.
type RawInfo struct {
	// Length is the length of the packet as it appears on the wire, including
	// the RoQ framing around it: the length prefix on a stream, or the flow ID
	// on a datagram. The flow ID of a stream is sent once, ahead of its first
	// packet, and is not counted.
	Length uint64
	// PayloadLength is the length of the RTP or RTCP packet, without any RoQ
	// framing.
	PayloadLength uint64
	// Data is the packet as it appears on the wire, of Length bytes. It is
	// logged as a hex string and omitted when nil, which is what a Session
	// does unless it was created with roq.WithQlogPacketData. It may be
	// shorter than Length, because the session can be configured to keep only
	// the first few bytes of each packet.
	Data []byte
}

func (i RawInfo) HasValues() bool {
	return i.Length != 0 || i.PayloadLength != 0 || len(i.Data) > 0
}

func (i RawInfo) encode(enc *jsontext.Encoder) error {
	h := encoderHelper{enc: enc}
	h.WriteToken(jsontext.BeginObject)
	if i.Length != 0 {
		h.WriteToken(jsontext.String("length"))
		h.WriteToken(jsontext.Uint(i.Length))
	}
	if i.PayloadLength != 0 {
		h.WriteToken(jsontext.String("payload_length"))
		h.WriteToken(jsontext.Uint(i.PayloadLength))
	}
	if len(i.Data) > 0 {
		h.WriteToken(jsontext.String("data"))
		h.WriteToken(jsontext.String(hex.EncodeToString(i.Data)))
	}
	h.WriteToken(jsontext.EndObject)
	return h.err
}

// Packet is the RoQ packet an event refers to.
type Packet struct {
	FlowID uint64
	// Length is the length of the packet as it appears on the wire, including
	// the RoQ framing around it: the length prefix on a stream, or the flow ID
	// on a datagram.
	Length uint64
	Raw    RawInfo
}

func (p Packet) encode(enc *jsontext.Encoder) error {
	h := encoderHelper{enc: enc}
	h.WriteToken(jsontext.BeginObject)
	h.WriteToken(jsontext.String("flow_id"))
	h.WriteToken(jsontext.Uint(p.FlowID))
	h.WriteToken(jsontext.String("length"))
	h.WriteToken(jsontext.Uint(p.Length))
	if p.Raw.HasValues() {
		h.WriteToken(jsontext.String("raw"))
		if h.err == nil {
			h.err = p.Raw.encode(enc)
		}
	}
	h.WriteToken(jsontext.EndObject)
	return h.err
}

// StreamOpened is logged when a stream is opened for a flow.
type StreamOpened struct {
	FlowID   uint64
	StreamID uint64
}

func (e StreamOpened) Name() string { return "roq:stream_opened" }

func (e StreamOpened) Encode(enc *jsontext.Encoder, _ time.Time) error {
	h := encoderHelper{enc: enc}
	h.WriteToken(jsontext.BeginObject)
	h.WriteToken(jsontext.String("flow_id"))
	h.WriteToken(jsontext.Uint(e.FlowID))
	h.WriteToken(jsontext.String("stream_id"))
	h.WriteToken(jsontext.Uint(e.StreamID))
	h.WriteToken(jsontext.EndObject)
	return h.err
}

// StreamPacketCreated is logged when a packet is written to a stream.
type StreamPacketCreated struct {
	StreamID uint64
	Packet   Packet
}

func (e StreamPacketCreated) Name() string { return "roq:stream_packet_created" }

func (e StreamPacketCreated) Encode(enc *jsontext.Encoder, _ time.Time) error {
	return encodeStreamPacket(enc, e.StreamID, e.Packet)
}

// StreamPacketParsed is logged when a packet is read from a stream.
type StreamPacketParsed struct {
	StreamID uint64
	Packet   Packet
}

func (e StreamPacketParsed) Name() string { return "roq:stream_packet_parsed" }

func (e StreamPacketParsed) Encode(enc *jsontext.Encoder, _ time.Time) error {
	return encodeStreamPacket(enc, e.StreamID, e.Packet)
}

func encodeStreamPacket(enc *jsontext.Encoder, streamID uint64, packet Packet) error {
	h := encoderHelper{enc: enc}
	h.WriteToken(jsontext.BeginObject)
	h.WriteToken(jsontext.String("stream_id"))
	h.WriteToken(jsontext.Uint(streamID))
	h.WriteToken(jsontext.String("packet"))
	if h.err == nil {
		h.err = packet.encode(enc)
	}
	h.WriteToken(jsontext.EndObject)
	return h.err
}

// DatagramPacketCreated is logged when a packet is sent as a datagram.
type DatagramPacketCreated struct {
	Packet Packet
}

func (e DatagramPacketCreated) Name() string { return "roq:datagram_packet_created" }

func (e DatagramPacketCreated) Encode(enc *jsontext.Encoder, _ time.Time) error {
	return encodeDatagramPacket(enc, e.Packet)
}

// DatagramPacketParsed is logged when a datagram is received.
type DatagramPacketParsed struct {
	Packet Packet
}

func (e DatagramPacketParsed) Name() string { return "roq:datagram_packet_parsed" }

func (e DatagramPacketParsed) Encode(enc *jsontext.Encoder, _ time.Time) error {
	return encodeDatagramPacket(enc, e.Packet)
}

func encodeDatagramPacket(enc *jsontext.Encoder, packet Packet) error {
	h := encoderHelper{enc: enc}
	h.WriteToken(jsontext.BeginObject)
	h.WriteToken(jsontext.String("packet"))
	if h.err == nil {
		h.err = packet.encode(enc)
	}
	h.WriteToken(jsontext.EndObject)
	return h.err
}
