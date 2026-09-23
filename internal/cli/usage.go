// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// usageError is a command line that cannot be run as given: an argument
// missing or too many, a flag unknown, malformed or left out. It is reported
// with the command's usage:
//
//	Error: dicer rm needs at least one instance name
//
//	Usage:  dicer rm NAME... [flags]
//	Run 'dicer rm --help' for more.
type usageError struct {
	cmd *cobra.Command
	msg string
}

func (e *usageError) Error() string { return e.msg }

// usagef returns a usageError for cmd.
func usagef(cmd *cobra.Command, format string, args ...any) error {
	return &usageError{cmd: cmd, msg: fmt.Sprintf(format, args...)}
}

// writeUsageError reports a usageError.
func writeUsageError(w io.Writer, e *usageError) {
	_, _ = fmt.Fprintf(w, "Error: %s\n\nUsage:  %s\nRun '%s --help' for more.\n",
		e.msg, e.cmd.UseLine(), e.cmd.CommandPath())
}

// noArgs accepts no arguments.
func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usagef(cmd, "%s takes no arguments, but got %s", cmd.CommandPath(), quoteAll(args))
	}
	return nil
}

// needs validates arguments against required and optional noun phrases such
// as "an instance name".
func needs(required []string, optional ...string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < len(required) {
			return usagef(cmd, "%s needs %s", cmd.CommandPath(), andList(required[len(args):]))
		}
		if limit := len(required) + len(optional); len(args) > limit {
			return usagef(cmd, "%s got an unexpected argument %q", cmd.CommandPath(), args[limit])
		}
		return nil
	}
}

// one accepts one argument, described as a noun phrase: one("an instance
// name").
func one(what string) cobra.PositionalArgs { return needs([]string{what}) }

// oneOrMore accepts one argument or more, of what the plural describes:
// oneOrMore("instance name") needs "at least one instance name".
func oneOrMore(what string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return usagef(cmd, "%s needs at least one %s", cmd.CommandPath(), what)
		}
		return nil
	}
}

// oneThenCommand accepts one argument, described as what, then anything
// after it: a command to run.
func oneThenCommand(what string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return usagef(cmd, "%s needs %s", cmd.CommandPath(), what)
		}
		return nil
	}
}

// requiredAnnotation marks a flag a command cannot run without. Its value
// is an example of one, for the message that asks for it.
const requiredAnnotation = "dicer_required"

// requireFlag marks a flag as one cmd needs, e.g. requireFlag(cmd, "size",
// "10GiB"). Its usage says so.
func requireFlag(cmd *cobra.Command, name, example string) {
	f := cmd.Flags().Lookup(name)
	f.Usage += " (required)"
	_ = cmd.Flags().SetAnnotation(name, requiredAnnotation, []string{example})
}

// checkRequiredFlags asks for any flag requireFlag marked that was not
// given: "dicer volume create needs --size, e.g. --size 10GiB".
func checkRequiredFlags(cmd *cobra.Command) error {
	var missing []*pflag.Flag
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if _, ok := f.Annotations[requiredAnnotation]; ok && !f.Changed {
			missing = append(missing, f)
		}
	})

	switch len(missing) {
	case 0:
		return nil
	case 1:
		f := missing[0]
		return usagef(cmd, "%s needs --%s, e.g. --%s %s",
			cmd.CommandPath(), f.Name, f.Name, f.Annotations[requiredAnnotation][0])
	default:
		names := make([]string, 0, len(missing))
		for _, f := range missing {
			names = append(names, "--"+f.Name)
		}
		return usagef(cmd, "%s needs %s", cmd.CommandPath(), andList(names))
	}
}

// pflag's words for a flag it could not parse: `invalid argument "lots"
// for "-m, --memory" flag: ...`.
var badFlagValue = regexp.MustCompile(`^invalid argument "(.*)" for "(?:-\w, )?--([\w-]+)" flag`)

// flagError turns an error parsing flags into a usageError, in words a
// person would use.
func flagError(cmd *cobra.Command, err error) error {
	msg := err.Error()

	if m := badFlagValue.FindStringSubmatch(msg); m != nil {
		value, name := m[1], m[2]
		want := ""
		if f := cmd.Flags().Lookup(name); f != nil {
			want = wantForType(f.Value.Type())
		}
		if want != "" {
			return usagef(cmd, "invalid --%s %q: want %s", name, value, want)
		}
		return usagef(cmd, "invalid --%s %q", name, value)
	}

	msg = strings.Replace(msg, "flag needs an argument: ", "a value is needed for ", 1)
	msg = strings.Replace(msg, "unknown shorthand flag: ", "unknown flag ", 1)
	msg = strings.Replace(msg, "unknown flag: ", "unknown flag ", 1)
	return usagef(cmd, "%s", msg)
}

// wantForType describes what a flag of a pflag type takes.
func wantForType(t string) string {
	switch t {
	case "int", "int32", "int64", "uint", "uint32", "uint64":
		return "a whole number"
	case "bool":
		return "true or false"
	case "duration":
		return "a duration like 30s, 5m or 1h"
	default:
		return ""
	}
}

// cobra's words for a command it does not have, and its suggestions.
var unknownCommand = regexp.MustCompile(`^unknown command "(.*)" for "(.*)"(?s:\n\nDid you mean this\?\n(.*))?`)

// unknownCommandMessage rewords cobra's error for a command it does not
// have, suggestions and all, as one line and a pointer to the help:
//
//	Error: dicer has no command "snapshots" (did you mean snapshot?)
//	Run 'dicer --help' for a list of commands.
//
// It returns false for any other error.
func unknownCommandMessage(err error) (string, bool) {
	var e *usageError
	if errors.As(err, &e) {
		return "", false
	}

	m := unknownCommand.FindStringSubmatch(err.Error())
	if m == nil {
		return "", false
	}

	name, parent := m[1], m[2]
	msg := fmt.Sprintf("%s has no command %q", parent, name)
	if suggestions := strings.Fields(m[3]); len(suggestions) > 0 {
		msg += " (did you mean " + orList(suggestions) + "?)"
	}
	return fmt.Sprintf("Error: %s\nRun '%s --help' for a list of commands.\n", msg, parent), true
}

// quoteAll quotes each argument: `"a", "b"`.
func quoteAll(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = fmt.Sprintf("%q", a)
	}
	return strings.Join(quoted, ", ")
}

// andList joins phrases as a sentence would: "a", "a and b", "a, b and c".
func andList(items []string) string {
	if len(items) == 1 {
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
