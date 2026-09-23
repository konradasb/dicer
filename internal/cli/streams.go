// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/konradasb/dicer"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// The API's streaming calls, driven as the CLI needs them.

// pullImage pulls an image, passing each progress message to onProgress,
// and returns the image the last one carries.
func pullImage(
	ctx context.Context, client *dicer.Client, ref string, onProgress func(*dicerdv1.PullImageProgress),
) (*dicerdv1.Image, error) {
	stream, err := client.PullImage(ctx, &dicerdv1.PullImageRequest{Ref: ref})
	if err != nil {
		return nil, err
	}

	var pulled *dicerdv1.Image
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return pulled, nil
		}
		if err != nil {
			return nil, err
		}

		if img := msg.GetImage(); img != nil {
			pulled = img
		}
		if onProgress != nil {
			onProgress(msg)
		}
	}
}

// streamEvents reads the events req asks for, passing each to onEvent, and
// calls onCaughtUp, if not nil, once the history has been read.
func streamEvents(
	ctx context.Context, client *dicer.Client, req *dicerdv1.GetEventsRequest,
	onEvent func(*dicerdv1.Event), onCaughtUp func(),
) error {
	stream, err := client.GetEvents(ctx, req)
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
			onEvent(e)
		}
		if msg.GetCaughtUp() && onCaughtUp != nil {
			onCaughtUp()
		}
	}
}

// streamLogs writes the log req asks for to w.
func streamLogs(ctx context.Context, client *dicer.Client, req *dicerdv1.GetInstanceLogsRequest, w io.Writer) error {
	stream, err := client.GetInstanceLogs(ctx, req)
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

// copyChunkSize is how much of an archive is sent in one message.
const copyChunkSize = 32 * 1024

// sendArchive sends the tar archive r reads into an instance at path.
func sendArchive(ctx context.Context, client *dicer.Client, name, path string, r io.Reader) error {
	stream, err := client.CopyToInstance(ctx)
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

// receiveArchive writes a tar archive of path in an instance to w.
func receiveArchive(ctx context.Context, client *dicer.Client, name, path string, w io.Writer) error {
	stream, err := client.CopyFromInstance(ctx, &dicerdv1.CopyFromInstanceRequest{Name: name, Path: path})
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

// execStream is a command running inside a guest. Send, resize and
// closeSend may be called from another goroutine than Recv, and are
// serialised against each other, since gRPC forbids concurrent sends.
type execStream struct {
	dicerdv1.DaemonService_ExecInstanceClient

	sendMu sync.Mutex
}

// startExec starts the command start describes.
func startExec(ctx context.Context, client *dicer.Client, start *dicerdv1.ExecInstanceStart) (*execStream, error) {
	stream, err := client.ExecInstance(ctx)
	if err != nil {
		return nil, err
	}

	// If the daemon already closed the stream, the first Recv says why.
	if err := stream.Send(&dicerdv1.ExecInstanceRequest{
		Payload: &dicerdv1.ExecInstanceRequest_Start{Start: start},
	}); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}

	return &execStream{DaemonService_ExecInstanceClient: stream}, nil
}

// sendStdin writes to the command's standard input. The bytes are copied, so
// the caller may reuse its buffer.
func (s *execStream) sendStdin(p []byte) error {
	return s.send(&dicerdv1.ExecInstanceRequest{
		Payload: &dicerdv1.ExecInstanceRequest_Stdin{Stdin: bytes.Clone(p)},
	})
}

// resize tells the guest the terminal's new size.
func (s *execStream) resize(rows, cols uint16) error {
	return s.send(&dicerdv1.ExecInstanceRequest{
		Payload: &dicerdv1.ExecInstanceRequest_Resize{
			Resize: &dicerdv1.ExecInstanceResize{Rows: uint32(rows), Cols: uint32(cols)},
		},
	})
}

// closeSend says there is no more input. Output goes on arriving.
func (s *execStream) closeSend() error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	return s.CloseSend()
}

func (s *execStream) send(req *dicerdv1.ExecInstanceRequest) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	return s.Send(req)
}

// timeOf returns the time ts holds: zero for an unset one.
func timeOf(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

// timestamp returns t as the API takes it: unset for the zero time.
func timestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}
