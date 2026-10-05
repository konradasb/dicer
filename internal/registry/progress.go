// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package registry

import (
	"io"

	gcr "github.com/google/go-containerregistry/pkg/v1"
)

// Phase is the part of a pull that is currently working.
type Phase string

const (
	// PhaseDownloading is fetching layers from the registry, the only part
	// with a byte count worth reporting.
	PhaseDownloading Phase = "downloading"

	// PhaseUnpacking is writing those layers out as a root filesystem.
	PhaseUnpacking Phase = "unpacking"
)

// Progress reports how far a pull has got. Downloaded and Total are bytes of
// compressed layers, and are zero outside PhaseDownloading.
type Progress struct {
	Phase      Phase
	Downloaded int64
	Total      int64
}

// ProgressFunc receives a pull's progress. It may be nil, and is called from
// the goroutine doing the pull, so it should not block for long.
type ProgressFunc func(Progress)

// report passes p to f, if f is not nil.
func (f ProgressFunc) report(p Progress) {
	if f != nil {
		f(p)
	}
}

// countingImage passes count the compressed bytes read from an image's
// layers.
type countingImage struct {
	gcr.Image
	count func(n int64)
}

// Layers implements gcr.Image.
func (i countingImage) Layers() ([]gcr.Layer, error) {
	layers, err := i.Image.Layers()
	if err != nil {
		return nil, err
	}

	counted := make([]gcr.Layer, len(layers))
	for n, l := range layers {
		counted[n] = countingLayer{Layer: l, count: i.count}
	}

	return counted, nil
}

// LayerByDigest implements gcr.Image.
func (i countingImage) LayerByDigest(h gcr.Hash) (gcr.Layer, error) {
	l, err := i.Image.LayerByDigest(h)
	if err != nil {
		return nil, err
	}
	return countingLayer{Layer: l, count: i.count}, nil
}

// LayerByDiffID implements gcr.Image.
func (i countingImage) LayerByDiffID(h gcr.Hash) (gcr.Layer, error) {
	l, err := i.Image.LayerByDiffID(h)
	if err != nil {
		return nil, err
	}
	return countingLayer{Layer: l, count: i.count}, nil
}

// countingLayer passes count the compressed bytes read out of a layer, which
// is what crosses the network.
type countingLayer struct {
	gcr.Layer
	count func(n int64)
}

// Compressed implements gcr.Layer.
func (l countingLayer) Compressed() (io.ReadCloser, error) {
	rc, err := l.Layer.Compressed()
	if err != nil {
		return nil, err
	}
	return &countingReader{ReadCloser: rc, count: l.count}, nil
}

// countingReader passes count the bytes read through it.
type countingReader struct {
	io.ReadCloser
	count func(n int64)
}

// Read implements io.Reader.
func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.count(int64(n))
	}
	return n, err
}
