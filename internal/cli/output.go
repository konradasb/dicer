// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/docker/go-units"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/dicer-sh/dicer/internal/cli/printer"
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
	key := p.Cols()[0]
	for _, c := range p.Cols() {
		if c == "Name" {
			key = c
			break
		}
	}

	for _, row := range p.KV() {
		if _, err := fmt.Fprintln(w, row[key]); err != nil {
			return err
		}
	}
	return nil
}

// confirm asks a yes-or-no question on the terminal, defaulting to no. With
// no terminal to ask on -- a script, CI -- there is no one to answer, and it
// goes ahead.
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

// date renders a timestamp as a calendar date, e.g. "2036-09-21".
func date(t time.Time) string {
	if t.IsZero() {
		return "-"
	}

	return t.Local().Format(time.DateOnly)
}

// sizeUnits are the units sizes are shown in: binary ones, the same that
// sizes are given in -- --memory 512MiB is shown back as 512 MiB, not as
// 536.9MB.
var sizeUnits = []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}

// size renders a byte count, e.g. "1.5 GiB".
func size(bytes int64) string {
	value, unit := scaleBytes(bytes, bytes)
	return formatNumber(value) + " " + unit
}

// sizeOf renders an amount of a total in the total's unit, so the two read
// together: "1.5 of 30.3 GiB" rather than "1.5 GiB of 30.3 GiB", and never
// "512 MiB of 2 GiB".
func sizeOf(part, total int64) string {
	value, unit := scaleBytes(total, total)
	partValue, _ := scaleBytes(part, total)
	return formatNumber(partValue) + " of " + formatNumber(value) + " " + unit
}

// scaleBytes returns bytes in the largest unit that leaves reference at
// least 1, and that unit.
func scaleBytes(bytes, reference int64) (float64, string) {
	value, ref := float64(bytes), float64(reference)
	i := 0
	for ref >= 1024 && i < len(sizeUnits)-1 {
		value /= 1024
		ref /= 1024
		i++
	}
	return value, sizeUnits[i]
}

// formatNumber renders a number to at most one decimal place, dropping a
// trailing ".0": 30.3, 1, 4.
func formatNumber(f float64) string {
	return strconv.FormatFloat(math.Round(f*10)/10, 'f', -1, 64)
}

// orDash renders an empty string as "-", so a table cell is never blank.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
