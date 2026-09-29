// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/konradasb/dicer/internal/cli"
)

// cliIntro opens the section's index.
const cliIntro = "`dicer` is the command line for `dicerd`. It talks to the daemon on this host, through " +
	"its socket, unless `--remote` names another, or a remote of `dicer remote` is current: see " +
	"[Remote access]({{< relref \"/docs/guides/remote-access\" >}}). Whoever can use the socket " +
	"can do anything the daemon can, which is as much as root: run `dicer` as root, or as a member " +
	"of the socket's group.\n\n" +
	"The common commands are shortcuts for the management commands most used: `dicer run` is " +
	"`dicer instance run`, `dicer ps` is `dicer instance list`."

// writeCLI replaces dir with a page per command; the root command's is the
// section's index.
func writeCLI(dir string) error {
	root := cli.NewCommand()
	root.InitDefaultVersionFlag()

	if err := os.RemoveAll(dir); err != nil {
		return err
	}

	index, err := cliIndex(root)
	if err != nil {
		return err
	}

	m := meta{
		title: "Command line", weight: 1, icon: "terminal",
		description: "Every dicer command, with its flags and examples.",
		collapsed:   true,
	}
	if err := writePage(filepath.Join(dir, "_index.md"), m, index); err != nil {
		return err
	}

	for _, cmd := range descendants(root) {
		page := commandPage(cmd)
		m := meta{title: cmd.CommandPath(), description: cmd.Short}

		if err := writePage(filepath.Join(dir, pageName(cmd)+".md"), m, page); err != nil {
			return err
		}
	}

	return nil
}

// commandPaths returns every dicer command's path, the root's included.
func commandPaths() map[string]bool {
	root := cli.NewCommand()

	paths := map[string]bool{root.CommandPath(): true}
	for _, c := range descendants(root) {
		paths[c.CommandPath()] = true
	}

	return paths
}

// descendants returns every command under cmd that a person can run or
// group others under, depth first.
func descendants(cmd *cobra.Command) []*cobra.Command {
	var out []*cobra.Command

	for _, c := range cmd.Commands() {
		if !c.IsAvailableCommand() {
			continue
		}
		out = append(out, c)
		out = append(out, descendants(c)...)
	}

	return out
}

// isGroup reports whether cmd only holds others, as dicer instance does: run
// alone, it shows its help.
func isGroup(cmd *cobra.Command) bool {
	return cmd.HasAvailableSubCommands()
}

// pageName returns the page a command is documented on: dicer_instance_list
// for dicer instance list.
func pageName(cmd *cobra.Command) string {
	return strings.ReplaceAll(cmd.CommandPath(), " ", "_")
}

// commandLink returns a link to a command's page, its path as the text.
func commandLink(cmd *cobra.Command) string {
	return fmt.Sprintf("[`%s`]({{< relref \"/docs/reference/cli/%s\" >}})", cmd.CommandPath(), pageName(cmd))
}

// commandRow returns a command's row in a table of commands.
func commandRow(cmd *cobra.Command) string {
	return fmt.Sprintf("| %s | %s. |\n", commandLink(cmd), cell(cmd.Short))
}

// cliIndex returns the section index's body: what dicer is, its commands in
// the groups its help shows them in, and the flags every command takes.
func cliIndex(root *cobra.Command) ([]byte, error) {
	var b bytes.Buffer

	b.WriteString(cliIntro + "\n\n## Commands\n")

	grouped := map[string][]*cobra.Command{}
	for _, c := range root.Commands() {
		if !c.IsAvailableCommand() {
			continue
		}
		if c.GroupID == "" {
			return nil, fmt.Errorf("command %q is in no group: give it one in cli.NewCommand", c.Name())
		}
		grouped[c.GroupID] = append(grouped[c.GroupID], c)
	}

	for _, g := range root.Groups() {
		fmt.Fprintf(&b, "\n### %s\n\n| Command | Description |\n|---|---|\n", groupTitle(g.Title))
		for _, c := range grouped[g.ID] {
			b.WriteString(commandRow(c))
		}
	}

	b.WriteString("\n## Global flags\n\n")
	writeFlags(&b, root.PersistentFlags(), "Every command takes them, and `-h`, `--help`.")
	b.WriteString("\n`dicer --version` prints the version, as `dicer version` does.\n")

	return b.Bytes(), nil
}

// groupTitle returns a help group's title as a heading: "Common Commands:"
// is "Common commands".
func groupTitle(title string) string {
	first, rest, _ := strings.Cut(strings.TrimSuffix(title, ":"), " ")
	return strings.TrimSpace(first + " " + strings.ToLower(rest))
}

// commandPage returns a command's page body: what it does, how it is run, its
// flags, and, for a group, its commands.
func commandPage(cmd *cobra.Command) []byte {
	var b bytes.Buffer

	text := cmd.Long
	if text == "" {
		text = cmd.Short + "."
	}
	b.WriteString(asCode(quotedCommands(text), nil, commandPaths()) + "\n")

	if !isGroup(cmd) {
		fmt.Fprintf(&b, "\n## Usage\n\n```console\n$ %s\n```\n", cmd.UseLine())

		if len(cmd.Aliases) > 0 {
			aliases := make([]string, len(cmd.Aliases))
			for i, a := range cmd.Aliases {
				aliases[i] = "`" + cmd.Parent().CommandPath() + " " + a + "`"
			}
			fmt.Fprintf(&b, "\nAlso run as %s.\n", strings.Join(aliases, ", "))
		}
	}

	if cmd.Example != "" {
		fmt.Fprintf(&b, "\n## Examples\n\n```console\n%s\n```\n", consoleExample(cmd.Example))
	}

	if isGroup(cmd) {
		b.WriteString("\n## Commands\n\n| Command | Description |\n|---|---|\n")
		for _, c := range descendants(cmd) {
			b.WriteString(commandRow(c))
		}
	}

	if flags := ownFlags(cmd); flags.HasAvailableFlags() {
		b.WriteString("\n## Flags\n\n")
		writeFlags(&b, flags, "")
	}

	if !isGroup(cmd) {
		b.WriteString("\n## Global flags\n\n")
		writeFlags(&b, cmd.InheritedFlags(), "")
	}

	return b.Bytes()
}

// quotedCommand matches a command quoted in help text: 'dicer ps'.
var quotedCommand = regexp.MustCompile(`'(dicer(?: [^']*)?)'`)

// quotedCommands writes the commands help text quotes as code.
func quotedCommands(text string) string {
	return quotedCommand.ReplaceAllString(text, "`$1`")
}

// consoleExample returns a command's examples as a console session: each
// command, less the indentation help gives it, after a prompt.
func consoleExample(example string) string {
	lines := strings.Split(strings.TrimRight(example, "\n"), "\n")

	continued := false
	for i, line := range lines {
		line = strings.TrimPrefix(line, "  ")
		if strings.TrimSpace(line) != "" && !continued {
			line = "$ " + line
		}
		lines[i] = line
		continued = strings.HasSuffix(line, "\\")
	}

	return strings.Join(lines, "\n")
}

// ownFlags returns the flags a command declares, without --help, which every
// command has.
func ownFlags(cmd *cobra.Command) *pflag.FlagSet {
	out := pflag.NewFlagSet(cmd.Name(), pflag.ContinueOnError)
	cmd.NonInheritedFlags().VisitAll(func(f *pflag.Flag) {
		if f.Name != "help" && f.Name != "version" {
			out.AddFlag(f)
		}
	})

	return out
}

// zeroDefaults are the defaults a flag's row leaves out, as help does.
var zeroDefaults = map[string]bool{"": true, "false": true, "0": true, "0s": true, "[]": true}

// writeFlags writes a table of flags, sorted by name, and a note under it.
func writeFlags(b *bytes.Buffer, flags *pflag.FlagSet, note string) {
	var rows []string

	flags.VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "version" {
			return
		}

		varname, usage := pflag.UnquoteUsage(f)

		name := "`--" + f.Name
		if varname != "" {
			name += " " + varname
		}
		name += "`"
		if f.Shorthand != "" {
			name = "`-" + f.Shorthand + "`, " + name
		}

		text := asCode(strings.TrimSuffix(usage, ".")+".", nil, commandPaths())
		if def := f.DefValue; !zeroDefaults[def] && !strings.Contains(usage, "(default") {
			text += " Default: `" + def + "`."
		}

		rows = append(rows, fmt.Sprintf("| %s | %s |", name, cell(text)))
	})

	b.WriteString("| Flag | Description |\n|---|---|\n")
	for _, r := range rows {
		b.WriteString(r + "\n")
	}

	if note != "" {
		b.WriteString("\n" + note + "\n")
	}
}

// cell makes text safe in a table's cell: a pipe would end the cell, even in
// code, and an angle bracket outside code would be taken for HTML.
func cell(text string) string {
	parts := strings.Split(text, "`")
	for i := range parts {
		parts[i] = strings.ReplaceAll(parts[i], "|", `\|`)
		if i%2 == 0 {
			parts[i] = strings.ReplaceAll(parts[i], "<", "&lt;")
		}
	}

	return strings.Join(parts, "`")
}
