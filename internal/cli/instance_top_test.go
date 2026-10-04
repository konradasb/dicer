// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// fakeProcesses are a guest's init, a worker and a zombie, as the daemon
// sends them.
func fakeProcesses() []*dicerdv1.Process {
	started := timestamppb.New(time.Now().Add(-2 * time.Hour))
	return []*dicerdv1.Process{
		{
			Pid: 1, User: "root", State: "S", Name: "init", Command: []string{"/init"},
			StartTime: started, CpuTime: durationpb.New(210 * time.Millisecond), ResidentMemoryBytes: 9 << 20,
		},
		{
			Pid: 216, Ppid: 1, User: "www-data", State: "S", Name: "nginx",
			Command:   []string{"nginx", "-g", "daemon off;"},
			StartTime: started, CpuTime: durationpb.New(1873 * time.Millisecond), ResidentMemoryBytes: 22 << 20,
		},
		{Pid: 217, Ppid: 216, User: "www-data", State: "Z", Name: "sh", StartTime: started},
	}
}

// ListInstanceProcesses sends fakeProcesses for any instance.
func (d *fakeInstanceDaemon) ListInstanceProcesses(
	context.Context, *dicerdv1.ListInstanceProcessesRequest,
) (*dicerdv1.ListInstanceProcessesResponse, error) {
	return &dicerdv1.ListInstanceProcessesResponse{Processes: fakeProcesses()}, nil
}

func TestProcessRowsShowTheGuestsProcesses(t *testing.T) {
	p := &printableProcesses{Processes: fakeProcesses()}

	rows := p.KV()
	if len(rows) != 3 {
		t.Fatalf("KV() has %d rows, want 3", len(rows))
	}

	want := map[string]any{
		"PID":     "216",
		"PPID":    "1",
		"User":    "www-data",
		"State":   "S",
		"Started": "2 hours ago",
		"CPUTime": "1.87s",
		"RSS":     "22 MiB",
		"Command": "nginx -g daemon off;",
	}
	for _, col := range p.Cols() {
		if rows[1][col] != want[col] {
			t.Errorf("%s = %q, want %q", col, rows[1][col], want[col])
		}
	}

	if got := rows[2]["Command"]; got != "[sh]" {
		t.Errorf("zombie's Command = %q, want its name in brackets", got)
	}
}

// TestTopTemplatesAndJSONUseTheColumnNames checks that the column names are
// a --format template's fields and JSON's keys.
func TestTopTemplatesAndJSONUseTheColumnNames(t *testing.T) {
	serveInstanceDaemon(t, newFakeInstanceDaemon())

	out, err := run(t, "top", "web", "--format", "{{.PID}} {{.Command}}")
	if err != nil {
		t.Fatalf("top --format template: %v\n%s", err, out)
	}
	if out != "1 /init\n216 nginx -g daemon off;\n217 [sh]\n" {
		t.Errorf("template output = %q", out)
	}

	out, err = run(t, "top", "web", "--format", "json")
	if err != nil {
		t.Fatalf("top --format json: %v\n%s", err, out)
	}
	var rows []map[string]string
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("output is not a JSON array: %v\n%s", err, out)
	}
	if len(rows) != 3 || rows[1]["User"] != "www-data" || rows[1]["RSS"] != "22 MiB" {
		t.Errorf("JSON rows = %v, want nginx's User and RSS under their column names", rows)
	}
}
