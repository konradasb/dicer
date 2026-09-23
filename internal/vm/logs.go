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

	"github.com/dicer-sh/dicer"
)

// LogSource is one of the logs an instance produces.
type LogSource string

const (
	// LogSourceGuest is the guest's serial console: the kernel's boot
	// messages, dicer-init's, and whatever the workload writes to the
	// console. It is kept with the instance, so it outlives a stop and is
	// there to explain one.
	LogSourceGuest LogSource = "guest"

	// LogSourceHypervisor is the VMM's own log, which explains a guest that
	// never got as far as booting. It lives in the runtime directory, so a
	// stop or a reboot takes it.
	LogSourceHypervisor LogSource = "hypervisor"
)

// LogOptions selects what to read and how much of it.
type LogOptions struct {
	// Source is the log to read. Defaults to the guest's console.
	Source LogSource

	// TailLines limits the output to the last lines, most useful on a
	// console that has been logging since boot. Zero means all of it.
	TailLines int

	// Follow keeps the log open, writing new output as it arrives, until
	// the instance stops or the caller gives up.
	Follow bool
}

const (
	// logPollInterval is how often a followed log is checked for new
	// output. The console is a file the hypervisor appends to, with nothing
	// to wait on.
	logPollInterval = 200 * time.Millisecond

	// logChunkSize bounds how much is read, and so sent, at a time.
	logChunkSize = 32 * 1024
)

// StreamLogs writes an instance's log to w.
//
// Without Follow it writes what is there and returns. With it, it keeps
// writing until the instance stops or ctx is done -- a log that has stopped
// growing because its instance stopped has nothing more to say.
func (m *Manager) StreamLogs(ctx context.Context, inst dicer.InstanceSpec, opts LogOptions, w io.Writer) error {
	path, err := m.logPath(inst, opts.Source)
	if err != nil {
		return err
	}

	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return dicer.NotFound("instance %q has no %s log yet", inst.Name, opts.Source)
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
			// There may be more waiting; read it before deciding anything.
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}

		if !opts.Follow || ended {
			return nil
		}

		// At the end of the file: either the instance is still running and
		// may write more, or it is not. If not, what it wrote between the
		// read above and its end is read once more before stopping: the
		// last words of a guest are often the ones that explain it.
		if rt, err := m.Runtime(inst); err == nil && !rt.State.IsActive() {
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
func (m *Manager) logPath(inst dicer.InstanceSpec, source LogSource) (string, error) {
	switch source {
	case LogSourceGuest, "":
		return m.serialLogPath(inst), nil
	case LogSourceHypervisor:
		return m.hypervisorLogPath(inst.ID), nil
	default:
		return "", dicer.InvalidArgument("unknown log source %q: want %s or %s",
			source, LogSourceGuest, LogSourceHypervisor)
	}
}

// seekToTail positions f at the start of its last n lines, reading backwards
// from the end so that a long console log is not read in full to show the
// last few lines of it.
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
			// The newline at the very end closes the last line rather than
			// starting one.
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
