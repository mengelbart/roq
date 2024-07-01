package main

import (
	"errors"
	"io"
	"log"

	"github.com/pion/interceptor/pkg/jitterbuffer"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3/pkg/media/h264writer"
	"github.com/pion/webrtc/v3/pkg/media/ivfwriter"
)

type rtpWriter interface {
	WriteRTP(*rtp.Packet) error
	Close() error
}

type fileWriter struct {
	buffer *jitterbuffer.JitterBuffer
	writer rtpWriter
}

func newFileWriter(filename string, codec string) (*fileWriter, error) {
	var w rtpWriter
	var err error

	switch codec {
	case "vp8":
		w, err = ivfwriter.New(filename)
	case "h264":
		w, err = h264writer.New(filename)
	default:
		return nil, errors.New("unknown codec")
	}
	if err != nil {
		return nil, err
	}
	jb := jitterbuffer.New()
	return &fileWriter{
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

func newIVFWriterWith(writer io.Writer, codec string) (*fileWriter, error) {
	var w rtpWriter
	var err error
	wr := &wrapper{
		w: writer,
	}

	switch codec {
	case "vp8":
		w, err = ivfwriter.NewWith(wr)
	case "h264":
		w = h264writer.NewWith(wr)
	default:
		return nil, errors.New("unknown codec")
	}
	if err != nil {
		return nil, err
	}
	jb := jitterbuffer.New()
	return &fileWriter{
		buffer: jb,
		writer: w,
	}, nil
}

func (w *fileWriter) Write(buf []byte) (int, error) {
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

func (w *fileWriter) Close() error {
	return w.writer.Close()
}
