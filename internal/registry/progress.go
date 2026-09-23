// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package registry

import (
	"io"
	"sync/atomic"

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

// Event reports how far a pull has got. Downloaded and Total are bytes of
// compressed layers, and are zero outside PhaseDownloading.
type Event struct {
	Phase      Phase
	Downloaded int64
	Total      int64
}

// EventFunc receives pull events. It may be nil, and is called from the
// goroutine doing the pull, so it should not block for long.
type EventFunc func(Event)

func (f EventFunc) send(ev Event) {
	if f != nil {
		f(ev)
	}
}

// progressImage reports the bytes read from an image's layers.
//
// The registry client has no progress hook for reads, and the layout writer
// pulls blobs itself, so the count has to come from the layers it reads.
type progressImage struct {
	gcr.Image
	report func(n int64)
}

func (p progressImage) Layers() ([]gcr.Layer, error) {
	layers, err := p.Image.Layers()
	if err != nil {
		return nil, err
	}

	wrapped := make([]gcr.Layer, len(layers))
	for i, l := range layers {
		wrapped[i] = progressLayer{Layer: l, report: p.report}
	}

	return wrapped, nil
}

func (p progressImage) LayerByDigest(h gcr.Hash) (gcr.Layer, error) {
	l, err := p.Image.LayerByDigest(h)
	if err != nil {
		return nil, err
	}
	return progressLayer{Layer: l, report: p.report}, nil
}

func (p progressImage) LayerByDiffID(h gcr.Hash) (gcr.Layer, error) {
	l, err := p.Image.LayerByDiffID(h)
	if err != nil {
		return nil, err
	}
	return progressLayer{Layer: l, report: p.report}, nil
}

// progressLayer counts the compressed bytes read out of a layer, which is
// what crosses the network.
type progressLayer struct {
	gcr.Layer
	report func(n int64)
}

func (l progressLayer) Compressed() (io.ReadCloser, error) {
	rc, err := l.Layer.Compressed()
	if err != nil {
		return nil, err
	}
	return &countingReader{ReadCloser: rc, report: l.report}, nil
}

type countingReader struct {
	io.ReadCloser
	report func(n int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if n > 0 && c.report != nil {
		c.report(int64(n))
	}
	return n, err
}

// counter accumulates downloaded bytes across layers pulled in parallel.
type counter struct{ n atomic.Int64 }

func (c *counter) add(n int64) int64 { return c.n.Add(n) }
