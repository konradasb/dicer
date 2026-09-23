// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package archive

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// ChunkSize is the most an archive is sent in one message. gRPC's default
// limit on a received message is 4MiB; this stays well clear of it, and is
// large enough that a tar's 512-byte blocks are not each a message.
const ChunkSize = 256 << 10

// Send packs src and hands the archive to send in chunks of at most
// ChunkSize, as a stream of messages carries it. It returns how many bytes
// of archive were sent.
func Send(src string, send func(chunk []byte) error) (int64, error) {
	cw := &chunkWriter{send: send}
	bw := bufio.NewWriterSize(cw, ChunkSize)

	if err := Pack(bw, src); err != nil {
		return cw.sent, err
	}
	if err := bw.Flush(); err != nil {
		return cw.sent, err
	}

	return cw.sent, nil
}

// Receive unpacks at dest an archive arriving in chunks from recv, which
// returns io.EOF after the last.
func Receive(recv func() ([]byte, error), dest string) error {
	return Unpack(&chunkReader{recv: recv}, dest)
}

// chunkWriter sends each write as one chunk.
type chunkWriter struct {
	send func([]byte) error
	sent int64
}

func (w *chunkWriter) Write(p []byte) (int, error) {
	// A copy: the buffer is reused as soon as this returns, and a message
	// may not be serialised until later.
	if err := w.send(bytes.Clone(p)); err != nil {
		return 0, err
	}
	w.sent += int64(len(p))

	return len(p), nil
}

// chunkReader reads chunks as one continuous stream.
type chunkReader struct {
	recv func() ([]byte, error)
	buf  []byte
}

func (r *chunkReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		chunk, err := r.recv()
		if errors.Is(err, io.EOF) {
			return 0, io.EOF
		}
		if err != nil {
			return 0, err
		}
		r.buf = chunk
	}

	n := copy(p, r.buf)
	r.buf = r.buf[n:]

	return n, nil
}
