// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package printer renders a Printable as a table, JSON, YAML or a Go
// template.
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

// format is a rendering Print supports.
type format string

const (
	tableFormat    format = "table"
	jsonFormat     format = "json"
	yamlFormat     format = "yaml"
	templateFormat format = "template"
)

// Printable is implemented by anything a command can print. Columns names
// the columns in display order; Rows returns one map per row, keyed by
// column.
//
// A Printable with more columns than a table shows by default also has a
// method DefaultColumns() []string. Asked for no columns in particular, a
// table shows those; JSON, YAML and templates still have every column.
type Printable interface {
	Columns() []string
	Rows() []map[string]any
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
	f, err := parseFormat(opts.Format)
	if err != nil {
		return err
	}

	switch f {
	case jsonFormat:
		return printJSON(item, out, opts.Columns)
	case yamlFormat:
		return printYAML(item, out, opts.Columns)
	case templateFormat:
		return printTemplate(item, out, opts.Format)
	case tableFormat:
		columns := opts.Columns
		defaulted, ok := item.(interface{ DefaultColumns() []string })
		if ok && len(columns) == 0 && !opts.AllColumns {
			columns = defaulted.DefaultColumns()
		}
		return printTable(item, out, columns)
	default:
		return fmt.Errorf("unsupported format %q", opts.Format)
	}
}

// PrintStructured writes v to out as JSON or YAML, as the format s names.
// It is for what is not a Printable: a whole record rather than rows. Any
// other format is an error.
func PrintStructured(v any, out io.Writer, s string) error {
	f, err := parseFormat(s)
	if err != nil {
		return err
	}

	switch f {
	case jsonFormat:
		return encodeJSON(out, v)
	case yamlFormat:
		return encodeYAML(out, v)
	default:
		return fmt.Errorf("unsupported format %q: want json or yaml", s)
	}
}

// IsStructured reports whether the format s names is machine-readable --
// JSON or YAML -- rather than something for a person to read.
func IsStructured(s string) bool {
	f, err := parseFormat(s)
	return err == nil && (f == jsonFormat || f == yamlFormat)
}

// IsTable reports whether the format s names is a table.
func IsTable(s string) bool {
	f, err := parseFormat(s)
	return err == nil && f == tableFormat
}

// parseFormat returns the format s names. Anything holding "{{" is a
// template.
func parseFormat(s string) (format, error) {
	if strings.Contains(s, "{{") {
		return templateFormat, nil
	}

	switch strings.ToLower(s) {
	case "table", "text":
		return tableFormat, nil
	case "json":
		return jsonFormat, nil
	case "yaml", "yml":
		return yamlFormat, nil
	default:
		return "", fmt.Errorf("unsupported format %q: want table, json, yaml, or a Go template such as '{{.Name}}'", s)
	}
}

// selectedColumns returns the columns named, matched case-insensitively and
// spelled as Columns spells them, or every column if none are named. Naming
// a column item does not have is an error.
func selectedColumns(item Printable, names []string) ([]string, error) {
	available := item.Columns()
	if len(names) == 0 || names[0] == "" {
		return available, nil
	}

	columns := make([]string, 0, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		i := slices.IndexFunc(available, func(a string) bool { return strings.EqualFold(a, trimmed) })
		if i < 0 {
			return nil, fmt.Errorf("no column %q: want one of %s", name, strings.Join(available, ", "))
		}
		columns = append(columns, available[i])
	}

	return columns, nil
}

// printTable prints the named columns of item as an aligned, borderless
// table.
func printTable(item Printable, out io.Writer, names []string) error {
	columns, err := selectedColumns(item, names)
	if err != nil {
		return err
	}

	// Headers are uppercased here rather than by tablewriter, whose own
	// formatting splits them at digits and punctuation: SHA 256,
	// ADDRESS  /  MASK.
	headers := make([]any, len(columns))
	for i, column := range columns {
		headers[i] = strings.ToUpper(column)
	}

	table := newTable(out)
	table.Header(headers...)
	for _, values := range item.Rows() {
		row := make([]string, len(columns))
		for i, column := range columns {
			row[i] = cell(values[column])
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
		tablewriter.WithRowAutoWrap(tw.WrapNone),
		tablewriter.WithConfig(tablewriter.Config{
			// printTable formats the headers itself.
			Header:   tw.CellConfig{Padding: padding, Formatting: tw.CellFormatting{AutoFormat: tw.Off}},
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

// selectedRows returns the rows of item holding only the named columns, as
// selectedColumns picks them.
func selectedRows(item Printable, names []string) ([]map[string]any, error) {
	columns, err := selectedColumns(item, names)
	if err != nil {
		return nil, err
	}

	rows := item.Rows()
	selected := make([]map[string]any, 0, len(rows))
	for _, values := range rows {
		row := make(map[string]any, len(columns))
		for _, column := range columns {
			row[column] = values[column]
		}
		selected = append(selected, row)
	}

	return selected, nil
}

// printYAML prints the named columns of item as a YAML sequence of
// mappings.
func printYAML(item Printable, out io.Writer, names []string) error {
	rows, err := selectedRows(item, names)
	if err != nil {
		return err
	}

	return encodeYAML(out, rows)
}

// encodeYAML writes v to out as YAML, indented by two spaces.
func encodeYAML(out io.Writer, v any) error {
	encoder := yaml.NewEncoder(out)
	encoder.SetIndent(2)
	if err := encoder.Encode(v); err != nil {
		return fmt.Errorf("encode YAML: %w", err)
	}

	return encoder.Close()
}

// templateEscapes turns the escapes a shell leaves alone into what they
// stand for, so '{{.Name}}\t{{.IP}}' separates with a tab, as in Docker.
var templateEscapes = strings.NewReplacer(`\t`, "\t", `\n`, "\n")

// printTemplate executes a Go template once per row, each on its own line:
// '{{.Name}}\t{{.IP}}' prints a name and an address a line, with the tabs
// lined up into columns. A row's fields are its columns, as Columns names
// them.
func printTemplate(item Printable, out io.Writer, text string) error {
	rowTemplate, err := template.New("format").Funcs(templateFuncs).Parse(templateEscapes.Replace(text))
	if err != nil {
		return fmt.Errorf("invalid format template: %w", err)
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, row := range item.Rows() {
		if err := rowTemplate.Execute(w, row); err != nil {
			return fmt.Errorf("execute format template: %w", err)
		}
		if _, err := io.WriteString(w, "\n"); err != nil {
			return err
		}
	}

	return w.Flush()
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

// printJSON prints the named columns of item as an indented JSON array of
// objects.
func printJSON(item Printable, out io.Writer, names []string) error {
	rows, err := selectedRows(item, names)
	if err != nil {
		return err
	}

	return encodeJSON(out, rows)
}

// encodeJSON writes v to out as JSON, indented by two spaces.
func encodeJSON(out io.Writer, v any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(v); err != nil {
		return fmt.Errorf("encode JSON: %w", err)
	}

	return nil
}
