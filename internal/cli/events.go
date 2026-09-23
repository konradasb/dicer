// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/konradasb/dicer"
	"github.com/konradasb/dicer/internal/image/reference"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// inspectEvents is how many of an instance's events inspect shows.
const inspectEvents = 10

// maxNameWidth is the widest the name column grows: a longer name is cut
// short in the middle, keeping an image's registry and tag.
const maxNameWidth = 40

func newEventsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Show what has happened to the instances and images on the host",
		Long: "Shows what has happened on the host: instances created, started, stopped,\n" +
			"crashed and restarted, health checks failing and recovering, images pulled\n" +
			"and collected. The daemon keeps them, so they explain what happened while\n" +
			"nobody was looking, and they survive it restarting.\n\n" +
			"Like logs, it prints what is kept and exits; -f keeps following new\n" +
			"events. With --format json it prints one JSON object a line, for scripts.",
		Example: "  dicer events\n" +
			"  dicer events -f\n" +
			"  dicer events --name web --since 1h\n" +
			"  dicer events --kind image -n 20\n" +
			"  dicer events -f --format json | jq -r 'select(.action == \"died\") | .name'",
		Args: noArgs,
		RunE: runEventsCommand,
	}

	cmd.Flags().SortFlags = false
	cmd.Flags().BoolP("follow", "f", false, "Keep writing new events as they happen")
	cmd.Flags().Int32P("tail", "n", 0, "Show only the last events (default: all kept)")
	cmd.Flags().String("since", "", "Show only events since a time, or for a duration: 2026-09-22, 10:30, 1h")
	cmd.Flags().String("kind", "", "Show only events about one kind of resource: instance or image")
	cmd.Flags().String("name", "", "Show only events about the resource with this name")
	cmd.Flags().String("format", "text", "Output format: text or json")
	_ = cmd.RegisterFlagCompletionFunc("kind", fixedCompletions("instance", "image"))
	_ = cmd.RegisterFlagCompletionFunc("format", fixedCompletions("text", "json"))

	return cmd
}

func runEventsCommand(cmd *cobra.Command, _ []string) error {
	flags := cmd.Flags()
	follow, _ := flags.GetBool("follow")
	tail, _ := flags.GetInt32("tail")
	sinceFlag, _ := flags.GetString("since")
	kind, _ := flags.GetString("kind")
	name, _ := flags.GetString("name")
	format, _ := flags.GetString("format")

	if format != "text" && format != "json" {
		return usagef(cmd, "unsupported format %q: want text or json", format)
	}
	req := &dicerdv1.GetEventsRequest{Name: name, Limit: tail, Follow: follow}
	if kind != "" {
		k, err := parseEnum[dicerdv1.EventKind]("--kind", kind)
		if err != nil {
			return usagef(cmd, "%s", err)
		}
		req.Kind = k
	}
	if sinceFlag != "" {
		since, err := parseSince(sinceFlag, time.Now())
		if err != nil {
			return usagef(cmd, "%s", err)
		}
		req.Since = timestamp(since)
	}

	client, cleanup, err := newClient(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	// Gather the history to size the columns, then show new events as they
	// come.
	table := &eventTable{w: cmd.OutOrStdout(), format: format, p: paletteFor(cmd.OutOrStdout())}

	var (
		history  []*dicerdv1.Event
		caughtUp bool
		writeErr error
	)
	err = streamEvents(cmd.Context(), client, req,
		func(e *dicerdv1.Event) {
			if writeErr != nil {
				return
			}
			if !caughtUp {
				history = append(history, e)

				return
			}
			writeErr = table.write([]*dicerdv1.Event{e})
		},
		func() {
			caughtUp = true
			if writeErr == nil {
				writeErr = table.write(history)
			}
			history = nil
		})
	if writeErr != nil {
		return writeErr
	}
	if err != nil && cmd.Context().Err() == nil && !errors.Is(err, io.EOF) {
		return err
	}

	// A stream that ended before it caught up still has a history to show.
	if !caughtUp {
		return table.write(history)
	}

	return nil
}

// eventTable writes events as dicer events shows them, each column as wide
// as the widest of it so far:
//
//	2026-09-22 09:49:48  Instance  grafana         Unhealthy  Health check "tcp :3000" failed 3 times in a row: connection refused
//	2026-09-22 09:49:51  Image     busybox:latest  Pulled     Pulled image docker.io/library/busybox:latest (sha256:1cfa4e2b09e1) in 829ms: ...
//
// or, for scripts, one JSON object a line.
type eventTable struct {
	w      io.Writer
	format string
	p      palette

	kind, name, action int
}

// write widens the columns to fit list, then writes it.
func (t *eventTable) write(list []*dicerdv1.Event) error {
	for _, e := range list {
		t.kind = max(t.kind, width(eventLabel(enumName(e.GetKind()))))
		t.name = max(t.name, width(eventName(e)))
		t.action = max(t.action, width(eventLabel(enumName(e.GetAction()))))
	}
	for _, e := range list {
		if err := t.writeOne(e); err != nil {
			return err
		}
	}
	return nil
}

func (t *eventTable) writeOne(e *dicerdv1.Event) error {
	if t.format == "json" {
		data, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(e)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(t.w, "%s\n", data)

		return err
	}

	line := timeOf(e.GetTime()).Local().Format(time.DateTime) + "  " +
		pad(eventLabel(enumName(e.GetKind())), t.kind) + "  " +
		pad(eventName(e), t.name) + "  " +
		eventAction(e, t.action, t.p)
	_, err := fmt.Fprintln(t.w, line)
	return err
}

// eventName is the name of an event's resource as the table shows it: an
// image's as people write it, and none wider than maxNameWidth.
func eventName(e *dicerdv1.Event) string {
	name := e.GetName()
	if e.GetKind() == dicerdv1.EventKind_EVENT_KIND_IMAGE {
		name = reference.Familiar(name)
	}
	return shorten(name, maxNameWidth)
}

// shorten cuts s to n characters by removing its middle.
func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	head := (n - 1) / 2
	tail := n - 1 - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// width is how many characters s takes up on a terminal.
func width(s string) int { return utf8.RuneCountInString(s) }

// pad pads s with spaces to n characters.
func pad(s string, n int) string {
	return s + strings.Repeat(" ", max(n-width(s), 0))
}

// eventAction formats an event's coloured action, padded to width, and its
// message.
func eventAction(e *dicerdv1.Event, width int, p palette) string {
	action := eventLabel(enumName(e.GetAction()))
	if e.GetMessage() == "" {
		return p.event(e.GetAction(), action)
	}

	return p.event(e.GetAction(), pad(action, width)) + "  " + e.GetMessage()
}

// eventLabel humanises a kind or action: "snapshot-created" is "Snapshot
// created".
func eventLabel(s string) string {
	return capitalize(strings.ReplaceAll(s, "-", " "))
}

// capitalize upper-cases the first letter of s.
func capitalize(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

// eventLines are an instance's recent events as inspect shows them, the
// time alone for today's.
//
//	09:49:48  Unhealthy   Health check "tcp :3000" failed 3 times in a row: connection refused
func eventLines(recent []*dicerdv1.Event, p palette) []string {
	actionWidth := 0
	for _, e := range recent {
		actionWidth = max(actionWidth, width(eventLabel(enumName(e.GetAction()))))
	}

	lines := make([]string, 0, len(recent))
	for _, e := range recent {
		lines = append(lines, eventTime(timeOf(e.GetTime()))+"  "+eventAction(e, actionWidth, p))
	}

	return lines
}

// eventTime says when an event was, briefly: the time alone if it was
// today, the date too if not.
func eventTime(t time.Time) string {
	t = t.Local()
	if y, m, d := t.Date(); time.Now().Year() == y && time.Now().Month() == m && time.Now().Day() == d {
		return t.Format(time.TimeOnly)
	}
	return t.Format(time.DateTime)
}

// recentEvents returns an instance's last events, for inspect. A daemon too
// old to keep events has none to show, which is no error.
func recentEvents(ctx context.Context, client *dicer.Client, instanceID string) ([]*dicerdv1.Event, error) {
	var out []*dicerdv1.Event

	err := streamEvents(ctx, client, &dicerdv1.GetEventsRequest{
		Kind:  dicerdv1.EventKind_EVENT_KIND_INSTANCE,
		Id:    instanceID,
		Limit: inspectEvents,
	}, func(e *dicerdv1.Event) { out = append(out, e) }, nil)
	switch {
	case errors.Is(err, io.EOF), err == nil:
		return out, nil
	case status.Code(err) == codes.Unimplemented:
		return nil, nil
	default:
		return nil, err
	}
}

// parseSince reads --since: a duration back from now (90m, 1h), a date
// (2026-09-22), a time today (10:30, 10:30:15), or both (2026-09-22 10:30).
func parseSince(s string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	for _, layout := range []string{time.DateTime, "2006-01-02 15:04", time.DateOnly, time.RFC3339} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	for _, layout := range []string{time.TimeOnly, "15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			y, m, d := now.Date()
			return time.Date(y, m, d, t.Hour(), t.Minute(), t.Second(), 0, time.Local), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid --since %q: want a duration (1h), a date (2026-09-22) or a time (10:30)", s)
}
