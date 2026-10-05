// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package printer

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// fakeRows is a Printable of two instances.
type fakeRows struct{}

func (fakeRows) Columns() []string { return []string{"Name", "State", "IP"} }

func (fakeRows) Rows() []map[string]any {
	return []map[string]any{
		{"Name": "web", "State": "Running", "IP": "10.0.0.5"},
		{"Name": "db", "State": "Stopped", "IP": "-"},
	}
}

func TestPrintTable(t *testing.T) {
	var buf bytes.Buffer
	if err := Print(fakeRows{}, &buf, Options{Format: "table"}); err != nil {
		t.Fatalf("Print: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"web", "db", "Running", "10.0.0.5"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output is missing %q:\n%s", want, out)
		}
	}
}

func TestPrintJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := Print(fakeRows{}, &buf, Options{Format: "json"}); err != nil {
		t.Fatalf("Print: %v", err)
	}

	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0]["Name"] != "web" {
		t.Errorf("first row Name = %v, want web", rows[0]["Name"])
	}
}

func TestPrintShowsOnlySelectedColumns(t *testing.T) {
	var buf bytes.Buffer
	if err := Print(fakeRows{}, &buf, Options{Format: "json", Columns: []string{"Name"}}); err != nil {
		t.Fatalf("Print: %v", err)
	}

	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if _, ok := rows[0]["State"]; ok {
		t.Errorf("State should have been filtered out: %v", rows[0])
	}
	if rows[0]["Name"] != "web" {
		t.Errorf("Name column missing: %v", rows[0])
	}
}

func TestPrintRejectsUnknownColumn(t *testing.T) {
	var buf bytes.Buffer
	err := Print(fakeRows{}, &buf, Options{Format: "table", Columns: []string{"Nope"}})
	if err == nil {
		t.Fatal("expected an error for an unknown column")
	}
	// The message should tell the user what they can pick instead.
	for _, want := range []string{"Nope", "Name", "State", "IP"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

func TestPrintRejectsUnknownFormat(t *testing.T) {
	var buf bytes.Buffer
	if err := Print(fakeRows{}, &buf, Options{Format: "xml"}); err == nil {
		t.Error("expected an error for an unsupported format")
	}
}

func TestPrintAcceptsFormatAliases(t *testing.T) {
	for _, format := range []string{"table", "text", "TABLE", "JSON", "yaml", "yml"} {
		var buf bytes.Buffer
		if err := Print(fakeRows{}, &buf, Options{Format: format}); err != nil {
			t.Errorf("format %q should be accepted: %v", format, err)
		}
	}
}

func TestPrintYAML(t *testing.T) {
	var buf bytes.Buffer
	if err := Print(fakeRows{}, &buf, Options{Format: "yaml", Columns: []string{"Name", "IP"}}); err != nil {
		t.Fatalf("Print: %v", err)
	}

	var rows []map[string]any
	if err := yaml.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("output is not valid YAML: %v\n%s", err, buf.String())
	}
	if len(rows) != 2 || rows[1]["Name"] != "db" || rows[1]["IP"] != "-" {
		t.Errorf("rows = %v", rows)
	}
	if _, ok := rows[0]["State"]; ok {
		t.Errorf("State should have been filtered out: %v", rows[0])
	}
}

func TestPrintTemplateRunsOncePerRow(t *testing.T) {
	var buf bytes.Buffer
	if err := Print(fakeRows{}, &buf, Options{Format: "{{.Name}}={{.State | lower}}"}); err != nil {
		t.Fatalf("Print: %v", err)
	}

	if got, want := buf.String(), "web=running\ndb=stopped\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestPrintRejectsMalformedTemplate(t *testing.T) {
	var buf bytes.Buffer
	if err := Print(fakeRows{}, &buf, Options{Format: "{{.Name"}); err == nil {
		t.Error("expected an error for a malformed template")
	}
}

func TestPrintMatchesColumnsIgnoringCase(t *testing.T) {
	var buf bytes.Buffer
	if err := Print(fakeRows{}, &buf, Options{Format: "json", Columns: []string{"name", "ip"}}); err != nil {
		t.Fatalf("Print: %v", err)
	}

	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	// The keys are spelled as the columns are, not as they were asked for.
	if rows[0]["Name"] != "web" || rows[0]["IP"] != "10.0.0.5" {
		t.Errorf("rows[0] = %v", rows[0])
	}
}

func TestPrintTemplateAlignsTabs(t *testing.T) {
	var buf bytes.Buffer
	// A literal backslash-t, as a shell passes it through single quotes.
	if err := Print(fakeRows{}, &buf, Options{Format: `{{.Name}}\t{{.IP}}`}); err != nil {
		t.Fatalf("Print: %v", err)
	}

	if got, want := buf.String(), "web  10.0.0.5\ndb   -\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestPrintStructuredWritesJSONOrYAML(t *testing.T) {
	v := map[string]any{"host": map[string]any{"name": "compute-1"}}

	for _, format := range []string{"json", "yaml"} {
		var buf bytes.Buffer
		if err := PrintStructured(v, &buf, format); err != nil {
			t.Fatalf("PrintStructured(%s): %v", format, err)
		}

		var got map[string]map[string]string
		if err := yaml.Unmarshal(buf.Bytes(), &got); err != nil || got["host"]["name"] != "compute-1" {
			t.Errorf("PrintStructured(%s) wrote %q (%v)", format, buf.String(), err)
		}
	}

	for _, format := range []string{"table", "{{.Name}}", "xml"} {
		if err := PrintStructured(v, &bytes.Buffer{}, format); err == nil {
			t.Errorf("PrintStructured(%s) succeeded, want only JSON and YAML", format)
		}
	}
}

func TestFormatPredicates(t *testing.T) {
	for _, tt := range []struct {
		format            string
		table, structured bool
	}{
		{"table", true, false},
		{"TEXT", true, false},
		{"json", false, true},
		{"yml", false, true},
		{"{{.Name}}", false, false},
		{"xml", false, false},
	} {
		if got := IsTable(tt.format); got != tt.table {
			t.Errorf("IsTable(%q) = %v, want %v", tt.format, got, tt.table)
		}
		if got := IsStructured(tt.format); got != tt.structured {
			t.Errorf("IsStructured(%q) = %v, want %v", tt.format, got, tt.structured)
		}
	}
}
