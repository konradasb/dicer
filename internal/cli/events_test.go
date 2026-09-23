// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dicer-sh/dicer"
)

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.Local)

	for in, want := range map[string]time.Time{
		"1h":                  now.Add(-time.Hour),
		"90m":                 now.Add(-90 * time.Minute),
		"2026-09-20":          time.Date(2026, 9, 20, 0, 0, 0, 0, time.Local),
		"2026-09-20 08:30":    time.Date(2026, 9, 20, 8, 30, 0, 0, time.Local),
		"2026-09-20 08:30:15": time.Date(2026, 9, 20, 8, 30, 15, 0, time.Local),
		"10:30":               time.Date(2026, 9, 22, 10, 30, 0, 0, time.Local),
	} {
		got, err := parseSince(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("parseSince(%q) = %v, %v; want %v", in, got, err, want)
		}
	}

	if _, err := parseSince("yesterday", now); err == nil {
		t.Error("parseSince accepted yesterday")
	}
}

func testEvent(action dicer.EventAction, message string) dicer.Event {
	return dicer.Event{
		Time: time.Date(2026, 9, 1, 9, 49, 48, 0, time.Local),
		Kind: dicer.KindInstance, ID: "abc", Name: "grafana", Action: action, Message: message,
		Attributes: map[string]string{"failing_streak": "3"},
	}
}

// writeEvents writes list as one batch of dicer events.
func writeEvents(t *testing.T, format string, list ...dicer.Event) string {
	t.Helper()
	var buf bytes.Buffer
	if err := (&eventTable{w: &buf, format: format}).write(list); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func imageEvent(name string, action dicer.EventAction, message string) dicer.Event {
	e := testEvent(action, message)
	e.Kind, e.ID, e.Name = dicer.KindImage, "", name

	return e
}

// Each column is as wide as what is in it, and an image is named as people
// write it.
func TestEventColumnsFitTheEvents(t *testing.T) {
	got := writeEvents(t, "text",
		testEvent("unhealthy", "Check failed 3 times: timed out after 5s"),
		imageEvent("docker.io/library/busybox:latest", "pulled", "sha256:1cfa4e2b09e1, 3.19 MiB boot disk"),
		testEvent("healthy", ""),
	)
	want := "2026-09-01 09:49:48  Instance  grafana         Unhealthy  Check failed 3 times: timed out after 5s\n" +
		"2026-09-01 09:49:48  Image     busybox:latest  Pulled     sha256:1cfa4e2b09e1, 3.19 MiB boot disk\n" +
		"2026-09-01 09:49:48  Instance  grafana         Healthy\n"
	if got != want {
		t.Errorf("text =\n%s\nwant\n%s", got, want)
	}
}

// What follows the history widens the columns if it must, and never
// narrows them.
func TestFollowedEventsWidenTheColumns(t *testing.T) {
	var buf bytes.Buffer
	table := &eventTable{w: &buf, format: "text"}
	_ = table.write([]dicer.Event{testEvent("started", "a"), testEvent("healthy", "b")})
	_ = table.write([]dicer.Event{testEvent("stopped", "c")})
	_ = table.write([]dicer.Event{imageEvent("ghcr.io/example/app:1.2", "pulled", "d")})

	want := "2026-09-01 09:49:48  Instance  grafana  Started  a\n" +
		"2026-09-01 09:49:48  Instance  grafana  Healthy  b\n" +
		"2026-09-01 09:49:48  Instance  grafana  Stopped  c\n" +
		"2026-09-01 09:49:48  Image     ghcr.io/example/app:1.2  Pulled   d\n"
	if buf.String() != want {
		t.Errorf("text =\n%s\nwant\n%s", buf.String(), want)
	}
}

func TestShorten(t *testing.T) {
	for _, tt := range []struct {
		in   string
		n    int
		want string
	}{
		{"grafana", 40, "grafana"},
		{"ghcr.io/some-org/some-team/a-very-long-image-name:1.2.3", 40, "ghcr.io/some-org/so…ong-image-name:1.2.3"},
		{"abcdefgh", 5, "ab…gh"},
	} {
		if got := shorten(tt.in, tt.n); got != tt.want || width(got) > tt.n {
			t.Errorf("shorten(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

func TestWriteEventJSON(t *testing.T) {
	line := writeEvents(t, "json", testEvent("started", ""))
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, line)
	}
	if record["action"] != "started" || record["name"] != "grafana" || strings.Count(line, "\n") != 1 {
		t.Errorf("json = %s, want one line with the API's field names", line)
	}
}

// inspect ends with what happened to the instance lately, lined up.
func TestInspectShowsRecentEvents(t *testing.T) {
	inst := dicer.Instance{
		Spec:   dicer.InstanceSpec{Name: "grafana", ImageRef: "grafana/grafana"},
		Status: dicer.InstanceStatus{State: dicer.StateRunning},
	}
	recent := []dicer.Event{
		testEvent("unhealthy", "Check failed 3 times: timed out after 5s"),
		testEvent("restarting", "Restart 1 in 1s"),
		testEvent("started", "Restart 1, cloud-hypervisor v49.0.0, IP 172.20.0.2"),
		testEvent("healthy", ""),
	}

	var buf bytes.Buffer
	if err := instanceView(inst, recent, palette{}).write(&buf); err != nil {
		t.Fatal(err)
	}

	want := "     Events: 2026-09-01 09:49:48  Unhealthy   Check failed 3 times: timed out after 5s\n" +
		"             2026-09-01 09:49:48  Restarting  Restart 1 in 1s\n" +
		"             2026-09-01 09:49:48  Started     Restart 1, cloud-hypervisor v49.0.0, IP 172.20.0.2\n" +
		"             2026-09-01 09:49:48  Healthy\n"
	if !strings.HasSuffix(buf.String(), want) {
		t.Errorf("inspect ends with\n%s\nwant\n%s", buf.String()[max(0, buf.Len()-len(want)-100):], want)
	}

	// No events, no block.
	buf.Reset()
	if err := instanceView(inst, nil, palette{}).write(&buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "Events") {
		t.Errorf("an instance with no events shows an Events block:\n%s", buf.String())
	}
}

func TestEventColours(t *testing.T) {
	p := palette{enabled: true}
	for action, colour := range map[string]string{
		"started": ansiGreen, "healthy": ansiGreen,
		"restarting": ansiYellow, "collected": ansiYellow,
		"died": ansiRed, "unhealthy": ansiRed,
	} {
		if got := p.event(action, action); got != colour+action+ansiReset {
			t.Errorf("event(%s) = %q, want it in %q", action, got, colour)
		}
	}
	if got := p.event("created", "created"); got != "created" {
		t.Errorf("created is painted %q, want it plain", got)
	}
}

// inspect is brief about today: the time says enough.
func TestEventTime(t *testing.T) {
	now := time.Now()
	if got := eventTime(now); got != now.Format(time.TimeOnly) {
		t.Errorf("eventTime(today) = %q, want the time alone", got)
	}
	earlier := now.AddDate(0, 0, -3)
	if got := eventTime(earlier); got != earlier.Format(time.DateTime) {
		t.Errorf("eventTime(3 days ago) = %q, want the date too", got)
	}
}

func TestEventLabel(t *testing.T) {
	for in, want := range map[string]string{
		"created":           "Created",
		"snapshot_restored": "Snapshot restored",
		"instance":          "Instance",
		"":                  "",
	} {
		if got := eventLabel(in); got != want {
			t.Errorf("eventLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// The API's own values are what scripts match on: JSON keeps them.
func TestJSONEventsKeepTheAPIsValues(t *testing.T) {
	line := writeEvents(t, "json", imageEvent("docker.io/library/busybox:latest", "snapshot_created", "before"))
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatal(err)
	}
	if record["action"] != "snapshot_created" || record["kind"] != "image" || record["message"] != "before" ||
		record["name"] != "docker.io/library/busybox:latest" {
		t.Errorf("json = %s, want the API's values", line)
	}
}
