// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
)

const (
	// logPollInterval is how often a followed log is checked for new
	// output.
	logPollInterval = 200 * time.Millisecond

	// logChunkSize bounds how much is read, and so sent, at a time.
	logChunkSize = 32 * 1024
)

// LogSource is which of an instance's logs to read.
type LogSource string

const (
	// LogSourceGuest is the guest's serial console. It is the default.
	LogSourceGuest LogSource = "guest"

	// LogSourceHypervisor is the hypervisor's log. It is discarded when the
	// instance stops.
	LogSourceHypervisor LogSource = "hypervisor"
)

// LogOptions says which of an instance's logs to read, and how much.
type LogOptions struct {
	// Source is which log. Empty is the guest's console.
	Source LogSource

	// TailLines limits the output to the last lines. Zero means all of it.
	TailLines int

	// Follow keeps writing new output until the instance stops or the
	// context is cancelled.
	Follow bool
}

// StreamLogs writes an instance's log to w. With opts.Follow it keeps writing
// until the instance stops or ctx is done.
func (m *Manager) StreamLogs(ctx context.Context, inst types.InstanceSpec, opts LogOptions, w io.Writer) error {
	path, err := m.logPath(inst, opts.Source)
	if err != nil {
		return err
	}

	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return errdefs.NotFound("instance %q has no %s log yet", inst.Name, opts.Source)
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if opts.TailLines > 0 {
		if err := seekToTail(f, opts.TailLines); err != nil {
			return err
		}
	}

	buf := make([]byte, logChunkSize)
	ended := false
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}

		if !opts.Follow || ended {
			return nil
		}

		// Once the instance has stopped, read once more for its last output.
		// One still starting has not: its boot is what there is to follow.
		if rt, err := m.Runtime(inst); err == nil && !rt.State.HoldsResources() {
			ended = true
			continue
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(logPollInterval):
		}
	}
}

// logPath returns the file a log source is written to.
func (m *Manager) logPath(inst types.InstanceSpec, source LogSource) (string, error) {
	switch source {
	case LogSourceGuest, "":
		return m.serialLogPath(inst), nil
	case LogSourceHypervisor:
		return m.hypervisorLogPath(inst.ID), nil
	default:
		return "", errdefs.InvalidArgument("unknown log source %q: want %s or %s",
			source, LogSourceGuest, LogSourceHypervisor)
	}
}

// seekToTail positions f at the start of its last n lines, reading backwards
// from the end.
func seekToTail(f *os.File, n int) error {
	end, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}

	buf := make([]byte, logChunkSize)
	newlines := 0
	offset := end

	for offset > 0 {
		size := min(int64(len(buf)), offset)
		offset -= size

		if _, err := f.ReadAt(buf[:size], offset); err != nil && !errors.Is(err, io.EOF) {
			return err
		}

		// Walk this block backwards, stopping at the newline that begins
		// the nth line from the end.
		for i := size - 1; i >= 0; i-- {
			if buf[i] != '\n' {
				continue
			}
			// A trailing newline ends the last line.
			if offset+i == end-1 {
				continue
			}
			if newlines++; newlines == n {
				_, err := f.Seek(offset+i+1, io.SeekStart)
				return err
			}
		}
	}

	// Fewer lines than asked for: the whole file is the tail.
	_, err = f.Seek(0, io.SeekStart)

	return err
}
