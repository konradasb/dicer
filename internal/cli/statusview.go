// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"io"
	"strings"
)

// statusView lays details out as systemctl status does: a headline, then
// labelled fields in blocks, their labels right-aligned to one column, a
// field with several lines continuing under its first, and a blank line
// between blocks:
//
//	● grafana — docker.io/grafana/grafana:latest
//
//	     Active: running since 09:53:20, 8 minutes ago
//	    Restart: always
//
//	    Network: 172.20.225.60 on default
//	             MAC 92:23:2f:b1:d6:de
//
// Empty fields and blocks are left out.
type statusView struct {
	headline string
	blocks   [][]field
}

// field is one labelled field of a statusView.
type field struct {
	label string
	lines []string
}

// labelIndent is the space left of the longest label, as systemctl leaves.
const labelIndent = 4

// block adds a block of fields.
func (v *statusView) block(fields ...field) {
	v.blocks = append(v.blocks, fields)
}

// write writes the view to w.
func (v *statusView) write(w io.Writer) error {
	width := 0
	for _, block := range v.blocks {
		for _, f := range block {
			if len(f.lines) > 0 {
				width = max(width, len(f.label))
			}
		}
	}
	width += labelIndent

	var b strings.Builder
	b.WriteString(v.headline + "\n")
	for _, block := range v.blocks {
		wroteBlock := false
		for _, f := range block {
			if len(f.lines) == 0 {
				continue
			}
			if !wroteBlock {
				b.WriteString("\n")
				wroteBlock = true
			}
			fmt.Fprintf(&b, "%*s: %s\n", width, f.label, f.lines[0])
			for _, line := range f.lines[1:] {
				fmt.Fprintf(&b, "%*s  %s\n", width, "", line)
			}
		}
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// oneLine is the lines of a field that has one value, or none if it is empty.
func oneLine(value string) []string {
	if value == "" {
		return nil
	}
	return []string{value}
}
