// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestUnaryInterceptorCountsByCode(t *testing.T) {
	const method = "/dicerd.v1.DaemonService/StartInstance"

	tests := []struct {
		name     string
		handler  grpc.UnaryHandler
		wantCode string
	}{
		{
			name:     "success",
			handler:  func(context.Context, any) (any, error) { return "ok", nil },
			wantCode: codes.OK.String(),
		},
		{
			name: "status error keeps its code",
			handler: func(context.Context, any) (any, error) {
				return nil, status.Error(codes.NotFound, "no such instance")
			},
			wantCode: codes.NotFound.String(),
		},
		{
			// A handler returning a plain error is Unknown to the client,
			// so that is what the metric must say too.
			name: "plain error is unknown",
			handler: func(context.Context, any) (any, error) {
				return nil, errors.New("boom")
			},
			wantCode: codes.Unknown.String(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(Options{})
			interceptor := m.UnaryServerInterceptor()

			_, _ = interceptor(
				context.Background(), nil,
				&grpc.UnaryServerInfo{FullMethod: method},
				tt.handler,
			)

			if got := testutil.ToFloat64(m.grpc.requests.WithLabelValues(method, tt.wantCode)); got != 1 {
				t.Errorf("requests{method=%q, code=%q} = %v, want 1", method, tt.wantCode, got)
			}
			if got := testutil.CollectAndCount(m.grpc.duration); got != 1 {
				t.Errorf("duration series = %d, want 1", got)
			}
		})
	}
}

func TestUnaryInterceptorPassesTheResponseThrough(t *testing.T) {
	m := New(Options{})

	wantErr := status.Error(codes.InvalidArgument, "bad name")
	resp, err := m.UnaryServerInterceptor()(
		context.Background(), "request",
		&grpc.UnaryServerInfo{FullMethod: "/svc/Method"},
		func(context.Context, any) (any, error) { return "response", wantErr },
	)

	if resp != "response" {
		t.Errorf("response = %v, want %q", resp, "response")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want %v", err, wantErr)
	}
}

func TestStreamInterceptorCountsCalls(t *testing.T) {
	const method = "/dicerd.v1.DaemonService/GetInstanceLogs"

	m := New(Options{})

	err := m.StreamServerInterceptor()(
		nil, nil,
		&grpc.StreamServerInfo{FullMethod: method},
		func(any, grpc.ServerStream) error { return status.Error(codes.Canceled, "client left") },
	)
	if status.Code(err) != codes.Canceled {
		t.Fatalf("error = %v, want a Canceled status", err)
	}

	if got := testutil.ToFloat64(m.grpc.requests.WithLabelValues(method, codes.Canceled.String())); got != 1 {
		t.Errorf("requests{method=%q, code=Canceled} = %v, want 1", method, got)
	}
}
