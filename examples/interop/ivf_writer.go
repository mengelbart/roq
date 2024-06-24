package main

import (
	"errors"
	"io"
	"log"

	"github.com/pion/interceptor/pkg/jitterbuffer"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3/pkg/media/ivfwriter"
)

type ivfWriter struct {
	buffer *jitterbuffer.JitterBuffer
	writer *ivfwriter.IVFWriter
}

func newIVFWriter(filename string) (*ivfWriter, error) {
	w, err := ivfwriter.New(filename)
	if err != nil {
		return nil, err
	}
	jb := jitterbuffer.New()
	return &ivfWriter{
		buffer: jb,
		writer: w,
	}, nil
}

type wrapper struct {
	w io.Writer
}

func (w *wrapper) Write(buf []byte) (int, error) {
	log.Printf("receiving frame of size %v", len(buf))
	return w.w.Write(buf)
}

func newIVFWriterWith(writer io.Writer) (*ivfWriter, error) {
	wr := &wrapper{
		w: writer,
	}
	w, err := ivfwriter.NewWith(wr)
	if err != nil {
		return nil, err
	}
	jb := jitterbuffer.New()
	return &ivfWriter{
		buffer: jb,
		writer: w,
	}, nil
}

func (w *ivfWriter) Write(buf []byte) (int, error) {
	c := make([]byte, len(buf))
	n := copy(c, buf)
	if n != len(buf) {
		return n, errors.New("copy error")
	}
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(c); err != nil {
		return len(buf), err
	}
	w.buffer.Push(pkt)
	p, err := w.buffer.Pop()
	if err != nil {
		return len(buf), nil
	}
	if p != nil {
		err = w.writer.WriteRTP(p)
		if err != nil {
			return len(buf), err
		}
	}
	// err = w.writer.WriteRTP(pkt)
	// return len(buf), err
	return len(buf), nil
}

func (w *ivfWriter) Close() error {
	return w.writer.Close()
}
