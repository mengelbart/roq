package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"os"
	"os/exec"
	"strings"
	"time"

	mqlog "github.com/mengelbart/qlog"
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
	codec       string
	datagrams   bool
}

func main() {
	if err := setupAndRun(); err != nil {
		log.Fatal(err)
	}
	log.Println("BYE")
}

func readConfig() flags {
	testcase := os.Getenv("TESTCASE")
	if len(testcase) > 0 {
		return parseFlagsFromEnv(testcase)
	}
	return parseFlags()
}

func parseFlagsFromEnv(_ string) flags {
	return flags{
		server:      os.Getenv("ENDPOINT") == "server",
		send:        os.Getenv("ROLE") == "sender",
		cert:        os.Getenv("CERT"),
		key:         os.Getenv("KEY"),
		addr:        os.Getenv("ADDR"),
		destination: os.Getenv("DESTINATION"),
		ffmpeg:      len(os.Getenv("FFMPEG")) > 0,
		codec:       os.Getenv("CODEC"),
		datagrams:   os.Getenv("DATAGRAMS") == "TRUE",
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
	codec := flag.String("codec", "vp8", "codec: vp8 or h264")
	datagrams := flag.Bool("datagrams", false, "send datagrams instead of streams")
	flag.Parse()
	return flags{
		server:      *server,
		send:        *send,
		cert:        *cert,
		key:         *key,
		addr:        *addr,
		destination: *destination,
		ffmpeg:      *ffmpeg,
		codec:       *codec,
		datagrams:   *datagrams,
	}
}

func setupAndRun() error {
	f := readConfig()
	keyLog, err := getSSLKeyLog()
	if err != nil {
		return err
	}
	log.Printf("got config: %v, keylog: %v\n", f, keyLog)
	if keyLog != nil {
		defer func() {
			log.Printf("closing keylog")
			keyLog.Close()
		}()
	}
	conn, err := connect(context.Background(), f, keyLog)
	if err != nil {
		return err
	}
	if f.send {
		return runSender(f, conn)
	}
	return runReceiver(f, conn)
}

func connect(ctx context.Context, f flags, keyLog io.Writer) (quic.Connection, error) {
	if f.server {
		tlsConfig, err := generateTLSConfig(f.cert, f.key, keyLog)
		tlsConfig.InsecureSkipVerify = true
		if err != nil {
			return nil, err
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
		KeyLogWriter:       keyLog,
	}, &quic.Config{
		EnableDatagrams: true,
		Tracer:          qlog.DefaultTracer,
	})
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func runSender(f flags, conn quic.Connection) error {
	role := "server"
	if !f.server {
		role = "client"
	}
	qlogfile := getQLOGWriter("roq", role)
	if qlogfile != nil {
		defer qlogfile.Close()
	}
	var qlogger *mqlog.Logger
	if qlogfile != nil {
		qlogger = mqlog.NewQLOGHandler(qlogfile, "roq qlog", role)
	}
	s, err := newSender(roq.NewQUICGoConnection(conn), qlogger, f.datagrams)
	if err != nil {
		return err
	}
	ffmpeg := exec.Command("ffmpeg", "-v", "debug", "-re", "-f", "lavfi", "-i", "testsrc=duration=10", "-g", "30", "-b:v", "1M", "-f", "ivf", "-")
	reader, err := ffmpeg.StdoutPipe()
	if err != nil {
		return err
	}
	if err = ffmpeg.Start(); err != nil {
		return err
	}
	fr, err := newIVFFrameReader(reader)
	if err != nil {
		return err
	}
	payloader := &codecs.VP8Payloader{}
	packetizer := rtp.NewPacketizer(1200, 96, 1, payloader, rtp.NewRandomSequencer(), 90_000)
	err = s.send(0, fr, packetizer)
	if err != nil {
		log.Printf("error while sending packets: %v", err)
		// TODO: Close process gracefully
		return ffmpeg.Process.Kill()
	}
	time.Sleep(time.Second)
	s.Close()
	return ffmpeg.Wait()
}

func runReceiver(f flags, conn quic.Connection) error {
	role := "client"
	if f.server {
		role = "server"
	}
	qlogfile := getQLOGWriter("roq", role)
	if qlogfile != nil {
		defer qlogfile.Close()
	}
	var qlogger *mqlog.Logger
	if qlogfile != nil {
		qlogger = mqlog.NewQLOGHandler(qlogfile, "roq qlog", role)
	}
	r, err := newReceiver(roq.NewQUICGoConnection(conn), qlogger)
	if err != nil {
		return err
	}
	var writer io.WriteCloser
	if f.ffmpeg {
		ffplay := exec.Command("ffplay", "-v", "debug", "-")
		ffplay.Stdout = os.Stdout
		ffplay.Stderr = os.Stderr
		stdout, err := ffplay.StdinPipe()
		if err != nil {
			return err
		}
		if err = ffplay.Start(); err != nil {
			return err
		}
		defer func() {
			log.Println("WAITING FOR FFPLAY")
			//state, err := ffplay.Process.Wait()
			//log.Printf("ffmpeg returned %v, err: %v", state, err)
			ffplay.Process.Kill()
		}()
		writer, err = newIVFWriterWith(stdout, f.codec)
		if err != nil {
			return err
		}
	} else {
		fileWriter, err := newFileWriter(f.destination, f.codec)
		if err != nil {
			return err
		}
		writer = fileWriter
	}
	return r.receive(0, writer)
}

func getSSLKeyLog() (io.WriteCloser, error) {
	filename := os.Getenv("SSLKEYLOGFILE")
	if len(filename) == 0 {
		return nil, nil
	}
	f, err := os.Create(filename)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func generateTLSConfig(certFile, keyFile string, keyLog io.Writer) (*tls.Config, error) {
	tlsConfig, err := generateTLSConfigWithCertAndKey(certFile, keyFile, keyLog)
	if err != nil {
		log.Printf("failed to generate TLS config from cert file and key, generating in memory certs: %v", err)
		tlsConfig, err = generateTLSConfigWithNewCert(keyLog)
	}
	return tlsConfig, err
}

func generateTLSConfigWithCertAndKey(certFile, keyFile string, keyLog io.Writer) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"roq-09"},
		KeyLogWriter: keyLog,
	}, nil
}

// Setup a bare-bones TLS config for the server
func generateTLSConfigWithNewCert(keyLog io.Writer) (*tls.Config, error) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		return nil, err
	}
	template := x509.Certificate{SerialNumber: big.NewInt(1)}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   []string{"roq-09"},
		KeyLogWriter: keyLog,
	}, nil
}

func getQLOGWriter(id, vantagePoint string) io.WriteCloser {
	qlogDir := os.Getenv("QLOGDIR")
	if qlogDir == "" {
		return nil
	}
	if _, err := os.Stat(qlogDir); os.IsNotExist(err) {
		if err := os.MkdirAll(qlogDir, 0o755); err != nil {
			log.Fatalf("failed to create qlog dir %s: %v", qlogDir, err)
		}
	}
	path := fmt.Sprintf("%s/%s_%s.qlog", strings.TrimRight(qlogDir, "/"), id, vantagePoint)
	f, err := os.Create(path)
	if err != nil {
		log.Printf("Failed to create qlog file %s: %s", path, err.Error())
		return nil
	}
	return f
}
