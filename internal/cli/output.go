// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/docker/go-units"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/konradasb/dicer/internal/cli/printer"
)

// formatUsage describes --format.
const formatUsage = "Output format: table, json, yaml, or a Go template, e.g. '{{.Name}}\\t{{.State}}'"

// addOutputFlags adds --format, and --columns and --quiet when the output is
// a table of several rows.
func addOutputFlags(cmd *cobra.Command, columns bool) {
	cmd.Flags().String("format", "table", formatUsage)
	_ = cmd.RegisterFlagCompletionFunc("format", completeFormats)

	if columns {
		cmd.Flags().StringSliceP("columns", "c", nil,
			"Columns to display, comma-separated and in any case (default: all)")
		cmd.Flags().BoolP("quiet", "q", false, "Only display names, one a line")
		cmd.MarkFlagsMutuallyExclusive("quiet", "columns")
		cmd.MarkFlagsMutuallyExclusive("quiet", "format")
	}
}

// completeFormats completes --format with the named formats. A template is
// the user's to write.
func completeFormats(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return []string{
		"table\tAligned columns for reading",
		"json\tA JSON array",
		"yaml\tA YAML sequence",
	}, cobra.ShellCompDirectiveNoFileComp
}

// render writes p to the command's output in the form the flags added by
// addOutputFlags ask for.
func render(cmd *cobra.Command, p printer.Printable) error {
	return renderTo(cmd, cmd.OutOrStdout(), p)
}

// renderTo writes p to w as render does.
func renderTo(cmd *cobra.Command, w io.Writer, p printer.Printable) error {
	format, _ := cmd.Flags().GetString("format")

	var columns []string
	if cmd.Flags().Lookup("columns") != nil {
		columns, _ = cmd.Flags().GetStringSlice("columns")
	}

	if quiet, _ := cmd.Flags().GetBool("quiet"); quiet {
		return printNames(w, p)
	}

	wide, _ := cmd.Flags().GetBool("wide")

	return printer.Print(p, w, printer.Options{
		Format:     format,
		Columns:    columns,
		AllColumns: wide,
	})
}

// printNames writes what identifies each row -- its Name, or its first
// column if it has none -- one a line, for 'dicer rm $(dicer ps -q)'.
func printNames(w io.Writer, p printer.Printable) error {
	key := p.Columns()[0]
	for _, c := range p.Columns() {
		if c == "Name" {
			key = c
			break
		}
	}

	for _, row := range p.Rows() {
		if _, err := fmt.Fprintln(w, row[key]); err != nil {
			return err
		}
	}
	return nil
}

// confirm asks a yes-or-no question, defaulting to no. Without a terminal it
// returns yes.
func confirm(cmd *cobra.Command, question string) (bool, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return true, nil
	}

	cmd.Printf("%s [y/N] ", question)
	answer, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}

	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// age renders a timestamp as a duration before now, e.g. "3 hours ago".
func age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}

	return units.HumanDuration(time.Since(t)) + " ago"
}

// orDash renders an empty string as "-", so a table cell is never blank.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// writeRecords writes messages as a JSON or YAML array, each as record
// renders it.
func writeRecords[M proto.Message](w io.Writer, format string, messages []M) error {
	records := make([]any, 0, len(messages))
	for _, m := range messages {
		r, err := record(m)
		if err != nil {
			return err
		}
		records = append(records, r)
	}

	return writeStructured(w, format, records)
}

// record returns a message as JSON and YAML output show it: its fields as
// the API names them, and its enums by name.
func record(m proto.Message) (any, error) {
	data, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	if err != nil {
		return nil, err
	}

	var r any
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return r, nil
}

// writeStructured writes v as JSON or YAML, as format asks, for a command
// whose table is not a Printable's.
func writeStructured(w io.Writer, format string, v any) error {
	if !printer.IsStructured(format) {
		return fmt.Errorf("unsupported format %q: want table, json or yaml", format)
	}
	return printer.PrintStructured(v, w, format)
}
