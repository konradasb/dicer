// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// grpcMetrics measures the API this daemon serves.
type grpcMetrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func (m *Metrics) newGRPCMetrics() grpcMetrics {
	return grpcMetrics{
		requests: m.counterVec(Description{
			Name:   "dicer_grpc_requests_total",
			Labels: []string{"method", "code"},
			Help:   "gRPC calls served, by full method and response code.",
			Doc:    "`code` is the gRPC status code, such as `OK` or `NotFound`.",
			Group:  GroupAPI,
		}),

		// Most calls are a file read and a reply; starting an instance and
		// pulling an image are the long tail, and a log stream runs for as
		// long as the client stays.
		duration: m.histogramVec(Description{
			Name:   "dicer_grpc_request_duration_seconds",
			Labels: []string{"method"},
			Help:   "Time a gRPC call took, from the first byte to the final status.",
			Doc:    "For a stream, such as `dicer logs -f`, it is how long the client stayed.",
			Group:  GroupAPI,
		}, prometheus.ExponentialBuckets(0.001, 4, 9)),
	}
}

// UnaryServerInterceptor times unary calls and counts them by method and
// response code.
func (m *Metrics) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (any, error) {
		started := time.Now()
		resp, err := handler(ctx, req)
		m.recordCall(info.FullMethod, err, time.Since(started))

		return resp, err
	}
}

// StreamServerInterceptor does the same for streaming calls. Their duration
// is the lifetime of the stream, which for a log follow is however long the
// client stayed -- worth knowing, but not a latency.
func (m *Metrics) StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler,
	) error {
		started := time.Now()
		err := handler(srv, ss)
		m.recordCall(info.FullMethod, err, time.Since(started))

		return err
	}
}

// recordCall records one finished call. The code label comes from the gRPC
// status, so a handler returning a plain error is counted as Unknown -- which
// is what the client sees.
func (m *Metrics) recordCall(method string, err error, d time.Duration) {
	m.grpc.requests.WithLabelValues(method, status.Code(err).String()).Inc()
	m.grpc.duration.WithLabelValues(method).Observe(d.Seconds())
}
