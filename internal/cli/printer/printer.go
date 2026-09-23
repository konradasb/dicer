// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package printer renders command output as a table, as JSON or YAML, or
// through a Go template.
//
// Commands describe their output by implementing Printable rather than
// formatting it themselves, so every command supports --format and --columns
// without repeating the logic.
package printer

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"text/template"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"
	"gopkg.in/yaml.v3"
)

// outputType is a rendering Print supports.
type outputType string

const (
	tableType    outputType = "table"
	jsonType     outputType = "json"
	yamlType     outputType = "yaml"
	templateType outputType = "template"
)

// Printable is implemented by anything a command can print.
//
// Cols names the available columns in display order; KV returns one map per
// row, keyed by column name. The set of valid columns is derived from Cols,
// so implementations do not restate it.
type Printable interface {
	Cols() []string
	KV() []map[string]any
}

// Defaulted is implemented by a Printable with more columns than a table
// shows by default. Asked for no columns in particular, a table shows these;
// JSON, YAML and templates still have every column.
type Defaulted interface {
	DefaultCols() []string
}

// Options are how Print renders.
type Options struct {
	// Format is table, json, yaml, or a Go template.
	Format string

	// Columns are those to show. Empty means the default ones for a table,
	// and all of them otherwise.
	Columns []string

	// AllColumns shows every column in a table, not just the default ones.
	AllColumns bool
}

// Print writes item to out as opts say: a table, JSON, YAML, or a Go
// template such as '{{.Name}} {{.State}}' applied to each row.
func Print(item Printable, out io.Writer, opts Options) error {
	outputFormat, err := parseFormat(opts.Format)
	if err != nil {
		return err
	}

	switch outputFormat {
	case jsonType:
		return printJSON(item, out, opts.Columns)
	case yamlType:
		return printYAML(item, out, opts.Columns)
	case templateType:
		return printTemplate(item, out, opts.Format)
	case tableType:
		cols := opts.Columns
		if d, ok := item.(Defaulted); ok && len(cols) == 0 && !opts.AllColumns {
			cols = d.DefaultCols()
		}
		return printTable(item, out, cols)
	default:
		return fmt.Errorf("unsupported format %q", opts.Format)
	}
}

// IsStructured reports whether format asks for machine-readable output --
// JSON or YAML -- rather than something for a person to read.
func IsStructured(format string) bool {
	t, err := parseFormat(format)
	return err == nil && (t == jsonType || t == yamlType)
}

// IsYAML reports whether format asks for YAML.
func IsYAML(format string) bool {
	t, err := parseFormat(format)
	return err == nil && t == yamlType
}

// parseFormat converts a format string to an outputType. Anything holding
// "{{" is a template.
func parseFormat(format string) (outputType, error) {
	if strings.Contains(format, "{{") {
		return templateType, nil
	}

	switch strings.ToLower(format) {
	case "table", "text":
		return tableType, nil
	case "json":
		return jsonType, nil
	case "yaml", "yml":
		return yamlType, nil
	default:
		return "", fmt.Errorf("unsupported format %q: want table, json, yaml, or a Go template such as '{{.Name}}'",
			format)
	}
}

// validateColumns validates the requested columns and returns the final
// column list. Columns match regardless of case -- "-c name,ip" is as good
// as "-c Name,IP" -- and are returned as Cols spells them.
func validateColumns(item Printable, includeCols []string) ([]string, error) {
	available := item.Cols()
	if len(includeCols) == 0 || includeCols[0] == "" {
		return available, nil
	}

	cols := make([]string, 0, len(includeCols))
	for _, c := range includeCols {
		name := strings.TrimSpace(c)
		i := slices.IndexFunc(available, func(a string) bool { return strings.EqualFold(a, name) })
		if i < 0 {
			return nil, fmt.Errorf("no column %q: want one of %s",
				c, strings.Join(available, ", "))
		}
		cols = append(cols, available[i])
	}

	return cols, nil
}

// printTable prints the chosen columns of item as an aligned, borderless
// table.
func printTable(item Printable, out io.Writer, includeCols []string) error {
	cols, err := validateColumns(item, includeCols)
	if err != nil {
		return err
	}

	table := newTable(out)
	table.Header(toAny(cols)...)
	for _, r := range item.KV() {
		row := make([]string, len(cols))
		for i, c := range cols {
			row[i] = cell(r[c])
		}
		if err := table.Append(row); err != nil {
			return fmt.Errorf("render table: %w", err)
		}
	}

	if err := table.Render(); err != nil {
		return fmt.Errorf("render table: %w", err)
	}
	return nil
}

// newTable returns a left-aligned table with no borders or separators, its
// columns set apart by two spaces.
func newTable(out io.Writer) *tablewriter.Table {
	padding := tw.CellPadding{Global: tw.Padding{Right: "  "}}
	return tablewriter.NewTable(out,
		tablewriter.WithRenderer(renderer.NewBlueprint()),
		tablewriter.WithRendition(tw.Rendition{
			Borders: tw.BorderNone,
			Symbols: tw.NewSymbols(tw.StyleNone),
			Settings: tw.Settings{
				Separators: tw.SeparatorsNone,
				Lines:      tw.LinesNone,
			},
		}),
		tablewriter.WithHeaderAlignment(tw.AlignLeft),
		tablewriter.WithRowAlignment(tw.AlignLeft),
		tablewriter.WithHeaderAutoFormat(tw.On),
		tablewriter.WithRowAutoWrap(tw.WrapNone),
		tablewriter.WithConfig(tablewriter.Config{
			Header:   tw.CellConfig{Padding: padding},
			Row:      tw.CellConfig{Padding: padding},
			Behavior: tw.Behavior{TrimSpace: tw.Off},
		}),
	)
}

// cell formats a value for a table cell: "N/A" for a missing one.
func cell(v any) string {
	switch v := v.(type) {
	case nil:
		return "N/A"
	case float32, float64:
		return fmt.Sprintf("%f", v)
	default:
		return fmt.Sprint(v)
	}
}

// toAny converts a []string to a []any.
func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// selectRows returns the rows of item, holding only the chosen columns.
func selectRows(item Printable, includeCols []string) ([]map[string]any, error) {
	cols, err := validateColumns(item, includeCols)
	if err != nil {
		return nil, err
	}

	rows := item.KV()
	result := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		row := make(map[string]any, len(cols))
		for _, c := range cols {
			row[c] = r[c]
		}
		result = append(result, row)
	}

	return result, nil
}

// printYAML prints the output in YAML format.
func printYAML(item Printable, out io.Writer, includeCols []string) error {
	result, err := selectRows(item, includeCols)
	if err != nil {
		return err
	}

	encoder := yaml.NewEncoder(out)
	encoder.SetIndent(2)
	if err := encoder.Encode(result); err != nil {
		return fmt.Errorf("encode YAML: %w", err)
	}

	return encoder.Close()
}

// templateEscapes turns the escapes a shell leaves alone into what they
// stand for, so '{{.Name}}\t{{.IP}}' separates with a tab, as in Docker.
var templateEscapes = strings.NewReplacer(`\t`, "\t", `\n`, "\n")

// printTemplate executes a Go template once per row, each on its own line:
// '{{.Name}}\t{{.IP}}' prints a name and an address a line, with the tabs
// lined up into columns. A row's fields are its columns, as Cols names them.
func printTemplate(item Printable, out io.Writer, format string) error {
	tmpl, err := template.New("format").Funcs(templateFuncs).Parse(templateEscapes.Replace(format))
	if err != nil {
		return fmt.Errorf("invalid format template: %w", err)
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, row := range item.KV() {
		if err := tmpl.Execute(tw, row); err != nil {
			return fmt.Errorf("execute format template: %w", err)
		}
		if _, err := io.WriteString(tw, "\n"); err != nil {
			return err
		}
	}

	return tw.Flush()
}

// templateFuncs are the functions a format template can call beyond the
// built-in ones, after Docker's.
var templateFuncs = template.FuncMap{
	"json": func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	},
	"join":  strings.Join,
	"lower": strings.ToLower,
	"upper": strings.ToUpper,
	"split": strings.Split,
	"title": func(s string) string {
		if s == "" {
			return s
		}
		return strings.ToUpper(s[:1]) + s[1:]
	},
}

// printJSON prints the output in JSON format.
func printJSON(item Printable, out io.Writer, includeCols []string) error {
	result, err := selectRows(item, includeCols)
	if err != nil {
		return err
	}

	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return fmt.Errorf("encode JSON: %w", err)
	}

	return nil
}
