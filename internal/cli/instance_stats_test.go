// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"encoding/json"
	"testing"

	"google.golang.org/grpc"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// fakeInstanceStats are two instances, as the daemon sends them.
func fakeInstanceStats() []*dicerdv1.InstanceStats {
	return []*dicerdv1.InstanceStats{
		{
			Id: "id-db", Name: "db", CpuPercent: 152.5, Vcpus: 2,
			ResidentMemoryBytes: 512 << 20, MemoryBytes: 2 << 30,
			NetworkReceiveBytes: 1 << 20, NetworkTransmitBytes: 40 << 10,
			DiskReadBytes: 0, DiskWrittenBytes: 3 << 20,
		},
		{
			Id: "id-web", Name: "web", CpuPercent: 2, Vcpus: 1,
			ResidentMemoryBytes: 768 << 20, MemoryBytes: 1 << 30,
			NetworkReceiveBytes: 1536, NetworkTransmitBytes: 512,
			DiskReadBytes: 10 << 20,
		},
	}
}

// GetInstanceStats sends fakeInstanceStats once, as the daemon does without Follow.
func (d *fakeInstanceDaemon) GetInstanceStats(
	_ *dicerdv1.GetInstanceStatsRequest, stream grpc.ServerStreamingServer[dicerdv1.GetInstanceStatsResponse],
) error {
	return stream.Send(&dicerdv1.GetInstanceStatsResponse{Instances: fakeInstanceStats()})
}

func TestInstanceStatsRowsShowWhatIsUsedOfTheHost(t *testing.T) {
	p := &printableInstanceStats{Instances: fakeInstanceStats()}

	rows := p.Rows()
	if len(rows) != 2 {
		t.Fatalf("Rows() has %d rows, want 2", len(rows))
	}

	want := map[string]any{
		"ID":       "id-db",
		"Name":     "db",
		"CPUPerc":  "152.50%",
		"MemUsage": "512 MiB / 2 GiB",
		"MemPerc":  "25.00%",
		"NetIO":    "1 MiB / 40 KiB",
		"BlockIO":  "0 B / 3 MiB",
	}
	for _, col := range p.Columns() {
		if rows[0][col] != want[col] {
			t.Errorf("%s = %q, want %q", col, rows[0][col], want[col])
		}
	}
}

// TestStatsTemplatesAndJSONUseTheColumnNames checks that the column names
// are a --format template's fields and JSON's keys.
func TestStatsTemplatesAndJSONUseTheColumnNames(t *testing.T) {
	serveFakeDaemon(t, newFakeInstanceDaemon())

	out, err := run(t, "stats", "--no-stream", "--format", "{{.Name}} {{.CPUPerc}} {{.MemPerc}}")
	if err != nil {
		t.Fatalf("stats --format template: %v\n%s", err, out)
	}
	if out != "db 152.50% 25.00%\nweb 2.00% 75.00%\n" {
		t.Errorf("template output = %q", out)
	}

	out, err = run(t, "stats", "--no-stream", "--format", "json")
	if err != nil {
		t.Fatalf("stats --format json: %v\n%s", err, out)
	}
	var rows []map[string]string
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("output is not a JSON array: %v\n%s", err, out)
	}
	if len(rows) != 2 || rows[1]["Name"] != "web" || rows[1]["NetIO"] != "1.5 KiB / 512 B" {
		t.Errorf("JSON rows = %v, want web's NetIO under its column name", rows)
	}
}
