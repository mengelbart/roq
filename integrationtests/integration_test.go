package integrationtests_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"testing"

	"github.com/mengelbart/roq"
	"github.com/pion/rtp"
	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/assert"
)

func listen(t *testing.T) *quic.Listener {
	tlsConfig := generateTLSConfig()
	listener, err := quic.ListenAddr("localhost:0", tlsConfig, &quic.Config{EnableDatagrams: true})
	assert.NoError(t, err)
	return listener
}
func accept(t *testing.T, ctx context.Context, listener *quic.Listener) *roq.Session {
	conn, err := listener.Accept(ctx)
	assert.NoError(t, err)
	assert.NoError(t, err)
	s, err := roq.NewSession(roq.NewQUICGoConnection(conn), true, nil)
	assert.NoError(t, err)
	return s
}

func dial(t *testing.T, ctx context.Context, addr string) *roq.Session {
	conn, err := quic.DialAddr(ctx, addr, generateTLSConfig(), &quic.Config{EnableDatagrams: true})
	assert.NoError(t, err)
	s, err := roq.NewSession(roq.NewQUICGoConnection(conn), true, nil)
	assert.NoError(t, err)
	return s
}

func TestIntegration(t *testing.T) {
	t.Run("simple_stream", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		l := listen(t)
		go func() {
			sender := accept(t, ctx, l)
			f, err := sender.NewSendFlow(0)
			assert.NoError(t, err)
			s, err := f.NewSendStream(ctx)
			assert.NoError(t, err)
			for i := 0; i < 10; i++ {
				pkt := rtp.Packet{
					Payload: make([]byte, 1500),
				}
				buf, err := pkt.Marshal()
				assert.NoError(t, err)
				n, err := s.WriteRTPBytes(buf)
				assert.NoError(t, err)
				assert.Equal(t, pkt.Header.MarshalSize()+1500, n)
			}
		}()
		receiver := dial(t, ctx, l.Addr().String())
		f, err := receiver.NewReceiveFlow(0)
		assert.NoError(t, err)
		buf := make([]byte, 2000)
		for i := 0; i < 10; i++ {
			n, err := f.Read(buf)
			assert.NoError(t, err)
			pkt := &rtp.Packet{}
			err = pkt.Unmarshal(buf[:n])
			assert.NoError(t, err)
			assert.Equal(t, 1500, len(pkt.Payload))
		}
	})
	t.Run("simple_datagram", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		l := listen(t)
		go func() {
			sender := accept(t, ctx, l)
			f, err := sender.NewSendFlow(0)
			assert.NoError(t, err)
			for i := 0; i < 10; i++ {
				pkt := rtp.Packet{
					Payload: make([]byte, 1200),
				}
				buf, err := pkt.Marshal()
				assert.NoError(t, err)
				err = f.WriteRTPBytes(buf)
				assert.NoError(t, err)
			}
		}()
		receiver := dial(t, ctx, l.Addr().String())
		f, err := receiver.NewReceiveFlow(0)
		assert.NoError(t, err)
		buf := make([]byte, 2000)
		for i := 0; i < 10; i++ {
			n, err := f.Read(buf)
			assert.NoError(t, err)
			pkt := &rtp.Packet{}
			err = pkt.Unmarshal(buf[:n])
			assert.NoError(t, err)
			assert.Equal(t, 1200, len(pkt.Payload))
		}
	})
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
		InsecureSkipVerify: true,
		Certificates:       []tls.Certificate{tlsCert},
		NextProtos:         []string{"roq-10"},
	}
}
