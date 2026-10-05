// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// instanceStatsStream is a GetInstanceStats stream that collects what is
// sent.
type instanceStatsStream struct {
	grpc.ServerStream
	ctx  context.Context
	sent chan *dicerdv1.GetInstanceStatsResponse
}

func newInstanceStatsStream(ctx context.Context) *instanceStatsStream {
	return &instanceStatsStream{ctx: ctx, sent: make(chan *dicerdv1.GetInstanceStatsResponse, 10)}
}

func (s *instanceStatsStream) Context() context.Context { return s.ctx }

func (s *instanceStatsStream) Send(resp *dicerdv1.GetInstanceStatsResponse) error {
	s.sent <- resp
	return nil
}

// newInstanceStatsServer returns a Server reading stats every millisecond.
func newInstanceStatsServer(t *testing.T) *Server {
	t.Helper()

	s, definitions := newTestServer(t)
	if err := definitions.CreateInstance(types.InstanceSpec{ID: "i-1", Name: "web"}); err != nil {
		t.Fatal(err)
	}
	s.statsInterval = time.Millisecond

	return s
}

func TestGetInstanceStatsWithoutFollowSendsOneBatch(t *testing.T) {
	s := newInstanceStatsServer(t)
	stream := newInstanceStatsStream(t.Context())

	if err := s.GetInstanceStats(&dicerdv1.GetInstanceStatsRequest{Names: []string{"web"}}, stream); err != nil {
		t.Fatalf("GetInstanceStats: %v", err)
	}
	close(stream.sent)

	var batches []*dicerdv1.GetInstanceStatsResponse
	for batch := range stream.sent {
		batches = append(batches, batch)
	}
	if len(batches) != 1 {
		t.Fatalf("sent %d batches, want 1", len(batches))
	}
	// web is defined but not running, so there is nothing to read.
	if got := batches[0].GetInstances(); len(got) != 0 {
		t.Errorf("batch = %v, want no instances", got)
	}
	if batches[0].GetReadTime() == nil {
		t.Error("batch has no read time")
	}
}

func TestGetInstanceStatsFollowsUntilTheClientGoes(t *testing.T) {
	s := newInstanceStatsServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	stream := newInstanceStatsStream(ctx)

	done := make(chan error, 1)
	go func() { done <- s.GetInstanceStats(&dicerdv1.GetInstanceStatsRequest{Follow: true}, stream) }()

	for range 3 {
		select {
		case <-stream.sent:
		case <-time.After(5 * time.Second):
			t.Fatal("no batch sent while following")
		}
	}

	cancel()
	// Batches are still taken, so a Send under way when the client went
	// cannot block.
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("GetInstanceStats = %v once the client went, want nil", err)
			}
			return
		case <-stream.sent:
		case <-time.After(5 * time.Second):
			t.Fatal("GetInstanceStats still following after the client went")
		}
	}
}

func TestGetInstanceStatsRejectsAnUnknownName(t *testing.T) {
	s := newInstanceStatsServer(t)
	stream := newInstanceStatsStream(t.Context())

	err := s.GetInstanceStats(&dicerdv1.GetInstanceStatsRequest{Names: []string{"web", "nope"}}, stream)

	wantClass(t, err, errdefs.ErrNotFound)
	if len(stream.sent) != 0 {
		t.Errorf("sent %d batches before failing, want none", len(stream.sent))
	}
}

func TestInstanceStatsBatchMeasuresCPUSinceThePreviousRead(t *testing.T) {
	started := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	read := started.Add(time.Minute)

	previous := []types.InstanceStats{
		{InstanceID: "i-web", StartedAt: started, ReadAt: read, CPUTime: 5 * time.Second},
		// Restarted since, so its totals began again.
		{InstanceID: "i-db", StartedAt: started, ReadAt: read, CPUTime: time.Hour},
	}
	current := []types.InstanceStats{
		{
			InstanceID: "i-web", Name: "web", StartedAt: started, ReadAt: read.Add(time.Second),
			Committed: types.Resources{VCPUs: 2, MemoryBytes: 1 << 30}, CPUTime: 6500 * time.Millisecond,
			ResidentMemoryBytes: 1 << 29, DiskReadBytes: 1, DiskWrittenBytes: 2,
			NetworkReceiveBytes: 3, NetworkTransmitBytes: 4,
			NetworkReceiveDrops: 5, NetworkTransmitDrops: 6,
			NetworkReceiveErrors: 7, NetworkTransmitErrors: 8,
			NetworkReceivePackets: 9, NetworkTransmitPackets: 10,
		},
		{InstanceID: "i-db", Name: "db", StartedAt: started.Add(time.Minute), ReadAt: read.Add(time.Second)},
		// Started since the previous read.
		{InstanceID: "i-new", Name: "new", StartedAt: read, ReadAt: read.Add(time.Second)},
	}

	batch := instanceStatsBatch(previous, current)

	if len(batch.GetInstances()) != 1 {
		t.Fatalf("batch = %v, want only web, the one read twice by the same VMM", batch.GetInstances())
	}
	web := batch.GetInstances()[0]
	if web.GetName() != "web" || web.GetId() != "i-web" {
		t.Errorf("instance = %s (%s), want web (i-web)", web.GetName(), web.GetId())
	}
	if got := web.GetCpuPercent(); got != 150 {
		t.Errorf("cpu_percent = %v, want 150", got)
	}
	if got := web.GetCpuTime().AsDuration(); got != 6500*time.Millisecond {
		t.Errorf("cpu_time = %v, want 6.5s", got)
	}
	if web.GetVcpus() != 2 || web.GetMemoryBytes() != 1<<30 || web.GetResidentMemoryBytes() != 1<<29 {
		t.Errorf("vcpus, memory, resident = %d, %d, %d; want 2, %d, %d",
			web.GetVcpus(), web.GetMemoryBytes(), web.GetResidentMemoryBytes(), 1<<30, 1<<29)
	}
	if web.GetDiskReadBytes() != 1 || web.GetDiskWrittenBytes() != 2 ||
		web.GetNetworkReceiveBytes() != 3 || web.GetNetworkTransmitBytes() != 4 {
		t.Errorf("disk and network = %d, %d, %d, %d; want 1, 2, 3, 4",
			web.GetDiskReadBytes(), web.GetDiskWrittenBytes(),
			web.GetNetworkReceiveBytes(), web.GetNetworkTransmitBytes())
	}
	if web.GetNetworkReceiveDrops() != 5 || web.GetNetworkTransmitDrops() != 6 ||
		web.GetNetworkReceiveErrors() != 7 || web.GetNetworkTransmitErrors() != 8 {
		t.Errorf("network drops and errors = %d, %d, %d, %d; want 5, 6, 7, 8",
			web.GetNetworkReceiveDrops(), web.GetNetworkTransmitDrops(),
			web.GetNetworkReceiveErrors(), web.GetNetworkTransmitErrors())
	}

	if web.GetNetworkReceivePackets() != 9 || web.GetNetworkTransmitPackets() != 10 {
		t.Errorf("network packets = %d, %d; want 9, 10",
			web.GetNetworkReceivePackets(), web.GetNetworkTransmitPackets())
	}
}
