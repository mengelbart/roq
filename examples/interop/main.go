package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"io"
	"log"
	"math/big"
	"os"
	"os/exec"

	"github.com/mengelbart/roq"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/qlog"
)

type flags struct {
	server      bool
	send        bool
	cert        string
	key         string
	addr        string
	destination string
	ffmpeg      bool
}

func main() {
	setupAndRun(context.Background())
}

func readConfig() flags {
	testcase := os.Getenv("TESTCASE")
	if len(testcase) > 0 {
		return parseFlagsFromEnv(testcase)
	}
	return parseFlags()
}

func parseFlagsFromEnv(testcase string) flags {
	switch testcase {
	case "datagrams":

	}
	return flags{
		server:      os.Getenv("ENDPOINT") == "server",
		send:        os.Getenv("ROLE") == "sender",
		cert:        "",
		key:         "",
		addr:        os.Getenv("ADDR"),
		destination: "",
		ffmpeg:      true,
	}
}

func parseFlags() flags {
	server := flag.Bool("server", false, "run as server (otherwise client)")
	send := flag.Bool("send", false, "send RTP stream")
	cert := flag.String("cert", "localhost.pem", "TLS certificate file")
	key := flag.String("key", "localhost-key.pem", "TLS key file")
	addr := flag.String("addr", "localhost:8080", "listen address")
	destination := flag.String("destination", "out.ivf", "output file of receiver, only if ffmpeg=false")
	ffmpeg := flag.Bool("ffmpeg", false, "use ffmpeg instead of files for io")
	flag.Parse()
	return flags{
		server:      *server,
		send:        *send,
		cert:        *cert,
		key:         *key,
		addr:        *addr,
		destination: *destination,
		ffmpeg:      *ffmpeg,
	}
}

func setupAndRun(ctx context.Context) error {
	f := readConfig()
	log.Printf("got config: %v\n", f)
	conn, err := connect(ctx, f)
	if err != nil {
		return err
	}
	if f.send {
		runSender(ctx, conn)
	}
	return runReceiver(ctx, f, conn)
}

func connect(ctx context.Context, f flags) (quic.Connection, error) {
	if f.server {
		var tlsConfig *tls.Config
		var err error
		tlsConfig, err = generateTLSConfigWithCertAndKey(f.cert, f.key)
		if err != nil {
			log.Printf("failed to generate TLS config from cert file and key, generating in memory certs: %v", err)
			tlsConfig = generateTLSConfig()
		}
		listener, err := quic.ListenAddr(f.addr, tlsConfig, &quic.Config{
			EnableDatagrams: true,
			Tracer:          qlog.DefaultTracer,
		})
		if err != nil {
			return nil, err
		}
		conn, err := listener.Accept(ctx)
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
	conn, err := quic.DialAddr(context.Background(), f.addr, &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"roq-09"},
	}, &quic.Config{
		EnableDatagrams: true,
		Tracer:          qlog.DefaultTracer,
	})
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func runSender(ctx context.Context, conn quic.Connection) error {
	s, err := newSender(roq.NewQUICGoConnection(conn))
	if err != nil {
		return err
	}
	ffmpeg := exec.Command("ffmpeg", "-re", "-i", "testsrc.mp4", "-g", "30", "-b:v", "1M", "-f", "ivf", "-")
	reader, err := ffmpeg.StdoutPipe()
	if err != nil {
		return err
	}
	go func() {
		if err := ffmpeg.Run(); err != nil {
			panic(err)
		}
	}()
	fr, err := newIVFFrameReader(reader)
	if err != nil {
		return err
	}
	payloader := &codecs.VP8Payloader{}
	packetizer := rtp.NewPacketizer(1200, 96, 1, payloader, rtp.NewRandomSequencer(), 90_000)
	return s.send(ctx, 0, fr, packetizer)
}

func runReceiver(ctx context.Context, f flags, conn quic.Connection) error {
	r, err := newReceiver(roq.NewQUICGoConnection(conn))
	if err != nil {
		return err
	}
	var writer io.Writer
	if f.ffmpeg {
		ffmpeg := exec.Command("ffplay", "-v", "debug", "-")
		ffmpeg.Stdout = os.Stdout
		ffmpeg.Stderr = os.Stderr
		stdout, err := ffmpeg.StdinPipe()
		if err != nil {
			return err
		}
		go func() {
			if err := ffmpeg.Run(); err != nil {
				panic(err)
			}
		}()
		writer, err = newIVFWriterWith(stdout)
		if err != nil {
			return err
		}
	} else {
		fileWriter, err := newIVFWriter(f.destination)
		if err != nil {
			return err
		}
		defer fileWriter.Close()
		writer = fileWriter
	}
	errCh := make(chan error)
	go func() {
		errCh <- r.receive(0, writer)
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		err := r.Close()
		<-errCh
		return err
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

// Setup a bare-bones TLS config for the server
func generateTLSConfig() *tls.Config {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		panic(err)
	}
	template := x509.Certificate{SerialNumber: big.NewInt(1)}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		panic(err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   []string{"roq-09"},
	}
}
