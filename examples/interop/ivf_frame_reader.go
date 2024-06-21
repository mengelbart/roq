package main

import (
	"io"

	"github.com/pion/webrtc/v3/pkg/media/ivfreader"
)

type ivfFrameReader struct {
	header *ivfreader.IVFFileHeader
	reader *ivfreader.IVFReader
}

func newIVFFrameReader(r io.Reader) (*ivfFrameReader, error) {
	ivf, header, err := ivfreader.NewWith(r)
	if err != nil {
		return nil, err
	}
	return &ivfFrameReader{
		header: header,
		reader: ivf,
	}, nil
}

func (r *ivfFrameReader) Read() ([]byte, error) {
	frame, _, err := r.reader.ParseNextFrame()
	return frame, err
}
