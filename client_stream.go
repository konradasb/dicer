// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"context"
	"errors"
	"io"

	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// PullImage fetches an image and converts it into a disk a guest can boot,
// reporting progress as it goes. It returns the image it pulled.
//
// onProgress may be nil, and is called from the calling goroutine as each
// message arrives, so it should not block for long. An image already held is
// not fetched again, and the pull reports only the stages it actually runs.
func (c *Client) PullImage(ctx context.Context, ref string, onProgress func(PullProgress)) (Image, error) {
	stream, err := c.daemon.PullImage(ctx, &dicerdv1.PullImageRequest{Ref: ref})
	if err != nil {
		return Image{}, err
	}

	var pulled Image
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return pulled, nil
		}
		if err != nil {
			return Image{}, err
		}

		progress := pullProgressFromProto(msg)
		if progress.Image != nil {
			pulled = *progress.Image
		}
		if onProgress != nil {
			onProgress(progress)
		}
	}
}

// LogSource is which of an instance's logs to read.
type LogSource string

const (
	// LogSourceGuest is the guest's serial console: the kernel's boot
	// messages, dicer-init's, and whatever the workload writes to the
	// console. It is the default.
	LogSourceGuest LogSource = "guest"

	// LogSourceHypervisor is the hypervisor's own log, which explains a
	// guest that never booted. It is discarded when the instance stops.
	LogSourceHypervisor LogSource = "hypervisor"
)

// LogOptions says which of an instance's logs to read, and how much.
type LogOptions struct {
	// Source is which log. Empty is the guest's console.
	Source LogSource

	// TailLines limits the output to the last lines. Zero means all of it.
	TailLines int

	// Follow keeps the stream open, sending new output until the instance
	// stops or the context is cancelled.
	Follow bool
}

// InstanceLogs streams an instance's log to w.
//
// It returns when the log ends, when the context is cancelled, or on the
// first error. A followed log ends when the instance stops.
func (c *Client) InstanceLogs(ctx context.Context, name string, opts LogOptions, w io.Writer) error {
	source := dicerdv1.LogSource_LOG_SOURCE_GUEST
	if opts.Source == LogSourceHypervisor {
		source = dicerdv1.LogSource_LOG_SOURCE_HYPERVISOR
	}

	stream, err := c.daemon.GetInstanceLogs(ctx, &dicerdv1.GetInstanceLogsRequest{
		Name:      name,
		Source:    source,
		TailLines: int32(opts.TailLines),
		Follow:    opts.Follow,
	})
	if err != nil {
		return err
	}

	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := w.Write(chunk.GetData()); err != nil {
			return err
		}
	}
}

// EventOptions says which events to read, and whether to keep reading.
type EventOptions struct {
	// Filter picks the events to report. The zero filter picks every event.
	Filter EventFilter

	// Limit takes only the last this many events of the history. Zero means
	// all of it.
	Limit int

	// Follow keeps the stream open, reporting new events until the context
	// is cancelled.
	Follow bool
}

// Events streams what happens on the host to onEvent, which is called once
// per event from the calling goroutine.
//
// The history comes first, then -- if the options follow -- whatever happens
// next. onCaughtUp, which may be nil, is called once between the two, even if
// the history is empty, so that a caller can lay the history out as a whole
// before it shows any of it.
//
// It returns when the stream ends, when the context is cancelled, or on the
// first error.
func (c *Client) Events(ctx context.Context, opts EventOptions, onEvent func(Event), onCaughtUp func()) error {
	req := &dicerdv1.GetEventsRequest{
		Kind:   string(opts.Filter.Kind),
		Id:     opts.Filter.ID,
		Since:  timestamp(opts.Filter.Since),
		Limit:  int32(opts.Limit),
		Follow: opts.Follow,
	}
	if len(opts.Filter.Names) > 0 {
		// The API takes one name; a filter on several is the caller's to
		// apply, which Matches does, so that both sides agree on what a
		// filter means.
		req.Name = opts.Filter.Names[0]
	}

	stream, err := c.daemon.GetEvents(ctx, req)
	if err != nil {
		return err
	}

	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		for _, e := range msg.GetEvents() {
			event := eventFromProto(e)
			if opts.Filter.Matches(event) && onEvent != nil {
				onEvent(event)
			}
		}
		if msg.GetCaughtUp() && onCaughtUp != nil {
			onCaughtUp()
		}
	}
}

// CopyToInstance writes a tar archive read from r into an instance at path.
//
// The archive's contents go into path if it is a directory, in its place if
// it is a file, and at it if nothing is there. A relative path is taken from
// the guest's root directory.
func (c *Client) CopyToInstance(ctx context.Context, name, path string, r io.Reader) error {
	stream, err := c.daemon.CopyToInstance(ctx)
	if err != nil {
		return err
	}

	if err := stream.Send(&dicerdv1.CopyToInstanceRequest{
		Payload: &dicerdv1.CopyToInstanceRequest_Start{
			Start: &dicerdv1.CopyToInstanceStart{Name: name, Path: path},
		},
	}); err != nil {
		return err
	}

	buf := make([]byte, copyChunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if sendErr := stream.Send(&dicerdv1.CopyToInstanceRequest{
				Payload: &dicerdv1.CopyToInstanceRequest_Data{Data: buf[:n]},
			}); sendErr != nil {
				return sendErr
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}

	_, err = stream.CloseAndRecv()

	return err
}

// CopyFromInstance reads a file or directory out of an instance and writes it
// to w as a tar archive. A relative path is taken from the guest's root
// directory.
func (c *Client) CopyFromInstance(ctx context.Context, name, path string, w io.Writer) error {
	stream, err := c.daemon.CopyFromInstance(ctx, &dicerdv1.CopyFromInstanceRequest{Name: name, Path: path})
	if err != nil {
		return err
	}

	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := w.Write(chunk.GetData()); err != nil {
			return err
		}
	}
}

// copyChunkSize is how much of an archive is sent in one message. It is well
// under gRPC's default message limit, and large enough that the overhead per
// message does not show.
const copyChunkSize = 32 * 1024
