package main

import (
	"context"
	"crypto/tls"
	"flag"
	"log"

	"github.com/mengelbart/roq"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3/pkg/media/ivfwriter"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/qlog"
)

func main() {
	cert := flag.String("cert", "cert.pem", "Cert file")
	key := flag.String("key", "key.pem", "Key file")
	addr := flag.String("addr", "localhost:443", "address to connect and send video to")
	flag.Parse()

	tlsConfig, err := generateTLSConfigWithCertAndKey(*cert, *key)
	if err != nil {
		panic(err)
	}
	listener, err := quic.ListenAddr(*addr, tlsConfig, &quic.Config{
		EnableDatagrams: true,
		Tracer:          qlog.DefaultTracer,
	})
	if err != nil {
		panic(err)
	}
	conn, err := listener.Accept(context.Background())
	if err != nil {
		panic(err)
	}
	session, err := roq.NewSession(roq.NewQUICGoConnection(conn))
	if err != nil {
		panic(err)
	}
	flow, err := session.NewReceiveFlow(0)
	if err != nil {
		panic(err)
	}
	fileWriter, err := ivfwriter.New("output.ivf")
	if err != nil {
		panic(err)
	}
	//fileWriter, err := h264writer.New("output.h264")
	//if err != nil {
	//	panic(err)
	//}
	defer fileWriter.Close()
	session.Start()
	buf := make([]byte, 4096)
	for {
		n, err := flow.Read(buf)
		if err != nil {
			log.Println(err)
			continue
		}
		log.Printf("received buffer of length %v", n)
		var pkt rtp.Packet
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			panic(err)
		}
		log.Printf("received RTP packet: %v", pkt.Header)
		if err := fileWriter.WriteRTP(&pkt); err != nil {
			panic(err)
		}
	}
}

func generateTLSConfigWithCertAndKey(certFile, keyFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"roq-09"},
	}, nil
}
