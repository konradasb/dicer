// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

func TestStatusView(t *testing.T) {
	v := &statusView{headline: "● web"}
	v.block(
		field{"Active", []string{"running"}},
		field{"Empty", nil},
	)
	v.block(field{"Nothing", nil}) // a block with nothing in it goes too
	v.block(
		field{"Ports", []string{"8080->80/tcp", "53->53/udp"}},
		field{"ID", []string{"abc"}},
	)

	var buf bytes.Buffer
	if err := v.write(&buf); err != nil {
		t.Fatal(err)
	}

	// The labels are aligned to the longest shown: an empty field's does
	// not count.
	want := "● web\n" +
		"\n" +
		"    Active: running\n" +
		"\n" +
		"     Ports: 8080->80/tcp\n" +
		"            53->53/udp\n" +
		"        ID: abc\n"
	if buf.String() != want {
		t.Errorf("view =\n%s\nwant\n%s", buf.String(), want)
	}
}

func TestPalette(t *testing.T) {
	plain := palette{}
	if got := plain.status("failed"); got != "failed" {
		t.Errorf("a disabled palette painted %q", got)
	}

	coloured := palette{enabled: true}
	for word, colour := range map[string]string{
		"running": ansiGreen, "healthy": ansiGreen,
		"restarting": ansiYellow, "starting": ansiYellow,
		"failed": ansiRed, "unhealthy": ansiRed,
	} {
		if got := coloured.status(word); got != colour+word+ansiReset {
			t.Errorf("status(%q) = %q, want it in %q", word, got, colour)
		}
	}
	if got := coloured.status("stopped"); got != "stopped" {
		t.Errorf("stopped is painted %q, want it plain", got)
	}

	// Output that is not a terminal is never coloured.
	if paletteFor(&bytes.Buffer{}).enabled {
		t.Error("the palette for a buffer is enabled")
	}
}

func TestInstanceView(t *testing.T) {
	ago := func(d time.Duration) *timestamppb.Timestamp { return timestamppb.New(time.Now().Add(-d)) }
	code := func(c int32) *int32 { return &c }

	tests := []struct {
		name string
		inst *dicerdv1.Instance
		want []string
	}{
		{
			name: "running",
			inst: &dicerdv1.Instance{
				Id: "cjp4tifq1l2wvu9ny0b78xsb", Name: "grafana",
				ImageRef: "docker.io/grafana/grafana:latest",
				Vcpus:    1, MemoryBytes: 4 << 30, DiskBytes: 10 << 30,
				HypervisorType: dicerdv1.HypervisorType_HYPERVISOR_TYPE_CLOUD_HYPERVISOR,
				KernelName:     "k", NetworkName: "default",
				RestartPolicy: &dicerdv1.RestartPolicy{Mode: dicerdv1.RestartMode_RESTART_MODE_ALWAYS},
				CreateTime:    ago(14 * time.Hour),

				State: stateRunning, StartTime: ago(8 * time.Minute),
				HypervisorVersion: "v49.0.0", HypervisorPid: 102322,
				Ip: "172.20.225.60", Mac: "92:23:2f:b1:d6:de",
			},
			want: []string{
				"● grafana — docker.io/grafana/grafana:latest\n",
				"     Active: running since ",
				", 8 minutes ago\n",
				"    Restart: always\n",
				"    Machine: 1 vCPU, 4 GiB memory, 10 GiB disk\n" +
					"             cloud-hypervisor v49.0.0, pid 102322\n" +
					"             kernel k\n",
				"    Network: 172.20.225.60 on default (grafana)\n" +
					"             MAC 92:23:2f:b1:d6:de\n",
				"    Command: the image's\n",
				"         ID: cjp4tifq1l2wvu9ny0b78xsb\n",
				", 14 hours ago\n",
			},
		},
		{
			name: "failed, with why",
			inst: &dicerdv1.Instance{
				Name: "job", ImageRef: "app",
				State: stateFailed, ExitCode: code(1), FinishTime: ago(5 * time.Minute),
				StateError: "gave up after 3 restarts: exit code 1",
			},
			want: []string{
				"● job — app\n",
				"     Active: failed, exited (1) 5 minutes ago\n" +
					"             gave up after 3 restarts: exit code 1\n",
			},
		},
		{
			name: "restarting",
			inst: &dicerdv1.Instance{
				Name: "worker", ImageRef: "app",
				State: stateRestarting, RestartCount: 3,
				NextRestartTime: timestamppb.New(time.Now().Add(time.Minute)),
			},
			want: []string{"● worker — app\n", "     Active: restarting (restart 3), in "},
		},
		{
			name: "stopped",
			inst: &dicerdv1.Instance{Name: "db", ImageRef: "postgres", State: stateStopped},
			want: []string{"○ db — postgres\n", "     Active: stopped\n"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := instanceView(tt.inst, nil, palette{}).write(&buf); err != nil {
				t.Fatal(err)
			}
			for _, want := range tt.want {
				if !strings.Contains(buf.String(), want) {
					t.Errorf("view is missing %q:\n%s", want, buf.String())
				}
			}
		})
	}
}
