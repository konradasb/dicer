// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// waitHost is a daemon whose instance stops with stopped once the test says
// so, by closing stop.
type waitHost struct {
	stopped *dicerdv1.WaitInstanceResponse
	stop    chan struct{}

	// req is the request the daemon was sent; waiting is closed once it
	// is waiting, before it sends its headers.
	req     *dicerdv1.WaitInstanceRequest
	waiting chan struct{}
}

func newWaitHost(stopped *dicerdv1.WaitInstanceResponse) *waitHost {
	return &waitHost{stopped: stopped, stop: make(chan struct{}), waiting: make(chan struct{})}
}

func (h *waitHost) daemon() *fakeDaemon {
	return &fakeDaemon{
		waitInstance: func(req *dicerdv1.WaitInstanceRequest, stream grpc.ServerStreamingServer[dicerdv1.WaitInstanceResponse]) error {
			h.req = req
			if req.GetName() != "job" {
				return status.Error(codes.NotFound, "no instance "+req.GetName())
			}
			// The client may return as soon as it has the headers.
			close(h.waiting)
			if err := stream.SendHeader(nil); err != nil {
				return err
			}

			select {
			case <-h.stop:
				return stream.Send(h.stopped)
			case <-stream.Context().Done():
				return stream.Context().Err()
			}
		},
	}
}

func TestWaitReturnsTheExitCode(t *testing.T) {
	tests := []struct {
		name    string
		stopped *dicerdv1.WaitInstanceResponse
		want    int
	}{
		{"exited", &dicerdv1.WaitInstanceResponse{
			State: dicerdv1.InstanceState_INSTANCE_STATE_FAILED, ExitCode: new(int32(3)),
		}, 3},
		{"stopped", &dicerdv1.WaitInstanceResponse{State: dicerdv1.InstanceState_INSTANCE_STATE_STOPPED}, 0},
		{"failed without saying how", &dicerdv1.WaitInstanceResponse{
			State: dicerdv1.InstanceState_INSTANCE_STATE_FAILED, StateError: "kernel panic",
		}, UnknownExitCode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := newWaitHost(tt.stopped)
			close(host.stop)
			c := connect(t, host.daemon())

			got, err := c.Instances.Wait(t.Context(), "job", WaitOptions{ID: "id1"})
			if err != nil || got != tt.want {
				t.Errorf("Wait = %d, %v; want %d", got, err, tt.want)
			}
			if host.req.GetId() != "id1" || host.req.GetNextStop() {
				t.Errorf("the daemon was asked %v", host.req)
			}
		})
	}
}

// TestWaiterReturnsOnceTheDaemonIsWaiting checks that Waiter returns only
// once the daemon is waiting, which is what lets a caller start the
// instance after it.
func TestWaiterReturnsOnceTheDaemonIsWaiting(t *testing.T) {
	host := newWaitHost(&dicerdv1.WaitInstanceResponse{
		State: dicerdv1.InstanceState_INSTANCE_STATE_STOPPED, ExitCode: new(int32(0)),
	})
	c := connect(t, host.daemon())

	w, err := c.Instances.Waiter(t.Context(), "job", WaitOptions{NextStop: true})
	if err != nil {
		t.Fatalf("Waiter: %v", err)
	}
	defer func() { _ = w.Close() }()

	select {
	case <-host.waiting:
	default:
		t.Fatal("Waiter returned before the daemon was waiting")
	}
	if !host.req.GetNextStop() {
		t.Error("the daemon was not asked for the next stop")
	}

	close(host.stop)
	if got, err := w.Wait(); err != nil || got != 0 {
		t.Errorf("Wait = %d, %v; want 0", got, err)
	}
}

func TestWaitForAnInstanceThatDoesNotExistIsNotFound(t *testing.T) {
	c := connect(t, newWaitHost(nil).daemon())

	if _, err := c.Instances.Wait(t.Context(), "other", WaitOptions{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Wait = %v, want ErrNotFound", err)
	}
}

// TestWaitForHealthAsksTheDaemonForIt checks that a wait for
// WaitConditionHealthy is sent as such, and returns 0 once the daemon says
// the instance is healthy.
func TestWaitForHealthAsksTheDaemonForIt(t *testing.T) {
	host := newWaitHost(&dicerdv1.WaitInstanceResponse{State: dicerdv1.InstanceState_INSTANCE_STATE_RUNNING})
	close(host.stop)
	c := connect(t, host.daemon())

	got, err := c.Instances.Wait(t.Context(), "job", WaitOptions{Condition: WaitConditionHealthy})
	if err != nil || got != 0 {
		t.Errorf("Wait = %d, %v; want 0", got, err)
	}
	if host.req.GetCondition() != dicerdv1.WaitCondition_WAIT_CONDITION_HEALTHY {
		t.Errorf("the daemon was asked %v", host.req)
	}
}

func TestWaitForAnUnknownConditionIsInvalid(t *testing.T) {
	c := connect(t, newWaitHost(nil).daemon())

	if _, err := c.Instances.Wait(t.Context(), "job", WaitOptions{Condition: "ready"}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("Wait = %v, want ErrInvalidArgument", err)
	}
}
