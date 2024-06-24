package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"time"

	"github.com/mengelbart/roq"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3/pkg/media/ivfreader"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/qlog"
)

func main() {
	videoFileName := flag.String("file", "output.ivf", "IVF video file to read")
	addr := flag.String("addr", "localhost:4443", "address to connect and send video to")
	flag.Parse()

	file, openErr := os.Open(*videoFileName)
	if openErr != nil {
		panic(openErr)
	}

	_, header, openErr := ivfreader.NewWith(file)
	if openErr != nil {
		panic(openErr)
	}

	// Determine video codec
	var payloader rtp.Payloader
	switch header.FourCC {
	case "AV01":
		payloader = &codecs.AV1Payloader{}
	case "VP90":
		payloader = &codecs.VP9Payloader{}
	case "VP80":
		payloader = &codecs.VP8Payloader{}
	default:
		panic(fmt.Sprintf("Unable to handle FourCC %s", header.FourCC))
	}
	log.Printf("got payloader: %T", payloader)

	packetizer := rtp.NewPacketizer(1200, 96, 1, payloader, rtp.NewRandomSequencer(), 90_000)

	file, ivfErr := os.Open(*videoFileName)
	if ivfErr != nil {
		panic(ivfErr)
	}

	ivf, header, ivfErr := ivfreader.NewWith(file)
	if ivfErr != nil {
		panic(ivfErr)
	}

	conn, err := quic.DialAddr(context.Background(), *addr, &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"roq-09"},
	}, &quic.Config{
		InitialStreamReceiveWindow:     math.MaxInt32,
		MaxStreamReceiveWindow:         math.MaxInt32,
		InitialConnectionReceiveWindow: math.MaxInt32,
		MaxConnectionReceiveWindow:     math.MaxInt32,
		MaxIncomingStreams:             math.MaxInt32,
		MaxIncomingUniStreams:          math.MaxInt32,
		EnableDatagrams:                true,
		Tracer:                         qlog.DefaultTracer,
	})
	if err != nil {
		panic(err)
	}
	session, err := roq.NewSession(roq.NewQUICGoConnection(conn), true, nil)
	if err != nil {
		panic(err)
	}

	flow, err := session.NewSendFlow(0)
	if err != nil {
		panic(err)
	}

	ticker := time.NewTicker(time.Millisecond * time.Duration((float32(header.TimebaseNumerator)/float32(header.TimebaseDenominator))*1000))
	for ; true; <-ticker.C {
		frame, _, ivfErr := ivf.ParseNextFrame()
		if errors.Is(ivfErr, io.EOF) {
			fmt.Printf("All video frames parsed and sent")
			os.Exit(0)
		}

		if ivfErr != nil {
			panic(ivfErr)
		}

		stream, err := flow.NewSendStream(context.Background())
		if err != nil {
			panic(err)
		}
		packets := packetizer.Packetize(frame, 1)
		for _, pkt := range packets {
			buf, err := pkt.Marshal()
			if err != nil {
				panic(err)
			}
			_, err = stream.WriteRTPBytes(buf)
			if err != nil {
				panic(err)
			}
		}
		stream.Close()
	}
}
