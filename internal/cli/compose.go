// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// dicer compose: a project's instances, networks and volumes, described in
// one file and run together.

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/konradasb/dicer"
	"github.com/konradasb/dicer/internal/compose"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// The environment variables the compose command's flags default to.
const (
	composeFileEnv    = "DICER_COMPOSE_FILE"
	composeProjectEnv = "DICER_COMPOSE_PROJECT_NAME"
)

// composePollInterval is how often an instance is looked at while waiting for
// it to become healthy, or to finish.
var composePollInterval = 500 * time.Millisecond

func newComposeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compose",
		Short: "Run a project of instances described in a compose file",
		Long: "Runs the instances a compose file describes, with the networks and volumes\n" +
			"they use.\n\n" +
			"The file is dicer-compose.yaml or compose.yaml, in this directory or the\n" +
			"nearest one above it that has one, unless -f names another. The project is\n" +
			"named by -p, else the file's name key, else its directory, and its instances\n" +
			"are named PROJECT-SERVICE.\n\n" +
			"The project's flags go before the command: dicer compose -f FILE up.",
		Example: "  dicer compose up -d\n" +
			"  dicer compose ps\n" +
			"  dicer compose logs -f web\n" +
			"  dicer compose exec db psql -U postgres\n" +
			"  dicer compose -f staging.yaml down",
	}

	// Local, not persistent: the root command traverses, so these are read
	// before the command and a command's own flags may use the same letters,
	// as docker compose's do. logs -f is --follow.
	flags := cmd.Flags()
	flags.StringP("file", "f", "",
		"Compose file (default $"+composeFileEnv+", then dicer-compose.yaml or compose.yaml here or above)")
	flags.StringP("project-name", "p", "",
		"Project name (default $"+composeProjectEnv+", then the file's name, then its directory's)")
	flags.String("project-directory", "", "Directory relative paths are taken from (default: the file's)")
	flags.String("env-file", "", "File of variables to substitute (default: .env in the project directory)")
	_ = cmd.MarkFlagFilename("file", "yaml", "yml")
	_ = cmd.MarkFlagFilename("env-file")
	_ = cmd.MarkFlagDirname("project-directory")

	cmd.AddCommand(
		newComposeUpCommand(),
		newComposeDownCommand(),
		newComposePsCommand(),
		newComposeLogsCommand(),
		newComposeStartCommand(),
		newComposeStopCommand(),
		newComposeRestartCommand(),
		newComposePullCommand(),
		newComposeExecCommand(),
		newComposeConfigCommand(),
	)

	return cmd
}

// composeCommand returns the compose command cmd is, or is below, whose
// flags name the project.
func composeCommand(cmd *cobra.Command) *cobra.Command {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Name() == "compose" && c.HasParent() && !c.Parent().HasParent() {
			return c
		}
	}
	return cmd
}

// loadProject reads the compose file the compose command's flags name.
func loadProject(cmd *cobra.Command) (*compose.Project, error) {
	flags := composeCommand(cmd).Flags()
	file, _ := flags.GetString("file")
	if file == "" {
		file = os.Getenv(composeFileEnv)
	}
	name, _ := flags.GetString("project-name")
	if name == "" {
		name = os.Getenv(composeProjectEnv)
	}
	dir, _ := flags.GetString("project-directory")
	envFile, _ := flags.GetString("env-file")

	return compose.Load(compose.Options{
		File:        file,
		ProjectDir:  dir,
		ProjectName: name,
		EnvFile:     envFile,
	})
}

// projectInstances returns the instances the daemon has of project p,
// services and orphans alike, keyed by name.
func projectInstances(ctx context.Context, client *dicer.Client, p *compose.Project) (map[string]*dicerdv1.Instance, error) {
	resp, err := client.ListInstances(ctx, &dicerdv1.ListInstancesRequest{})
	if err != nil {
		return nil, err
	}

	out := make(map[string]*dicerdv1.Instance)
	for _, inst := range resp.GetInstances() {
		if inst.GetLabels()[compose.LabelProject] == p.Name {
			out[inst.GetName()] = inst
		}
	}
	return out, nil
}

// orphans are the project's instances whose service is no longer in the
// file, by name.
func orphans(p *compose.Project, instances map[string]*dicerdv1.Instance) []string {
	var names []string
	for name, inst := range instances {
		if _, ok := p.ServiceFor(inst); !ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// instancesOf returns the names of a service's instances among the
// project's, found by their label: the one named as the file names it now,
// and any it had under another name before its container_name changed,
// which up has yet to replace. Sorted, the current name first.
func instancesOf(s *compose.Service, instances map[string]*dicerdv1.Instance) []string {
	var current, renamed []string
	for name, inst := range instances {
		switch {
		case inst.GetLabels()[compose.LabelService] != s.Name:
		case name == s.Instance.GetName():
			current = append(current, name)
		default:
			renamed = append(renamed, name)
		}
	}
	slices.Sort(renamed)
	return append(current, renamed...)
}

// serviceInstance returns the instance of a service, or an error saying it
// has none yet: the one named as the file names it, or else the one it had
// under an earlier name.
func serviceInstance(s *compose.Service, instances map[string]*dicerdv1.Instance) (*dicerdv1.Instance, error) {
	names := instancesOf(s, instances)
	if len(names) == 0 {
		return nil, fmt.Errorf("service %s has no instance: create it with dicer compose up", s.Name)
	}
	return instances[names[0]], nil
}

// serviceNames completes the arguments of a command that takes services.
func serviceNames(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	p, err := loadProject(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return slices.DeleteFunc(p.ServiceNames(), func(name string) bool {
		return slices.Contains(args, name)
	}), cobra.ShellCompDirectiveNoFileComp
}

// lines writes whole lines from goroutines working at once, so that theirs
// do not interleave.
type lines struct {
	mu  sync.Mutex
	out io.Writer
}

func (l *lines) printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintf(l.out, format+"\n", args...)
}

// prefixColors tell apart the services whose output is interleaved, as
// docker compose's do.
var prefixColors = []string{ansiCyan, ansiYellow, ansiGreen, "\x1b[35m", "\x1b[34m", "\x1b[91m", "\x1b[92m", "\x1b[93m"}

// prefixedWriter writes each line written to it with a prefix naming where
// it came from: "shop-web  | ".
type prefixedWriter struct {
	lines  *lines
	prefix string
	buf    []byte
}

func newPrefixedWriter(l *lines, name string, width, index int) *prefixedWriter {
	prefix := fmt.Sprintf("%-*s | ", width, name)
	if p := paletteFor(l.out); p.enabled {
		prefix = p.paint(prefixColors[index%len(prefixColors)], prefix)
	}
	return &prefixedWriter{lines: l, prefix: prefix}
}

func (w *prefixedWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := slices.Index(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		w.lines.printf("%s%s", w.prefix, strings.TrimRight(string(w.buf[:i]), "\r"))
		w.buf = w.buf[i+1:]
	}
}

// flush writes what is left of a last line with no newline.
func (w *prefixedWriter) flush() {
	if len(w.buf) > 0 {
		w.lines.printf("%s%s", w.prefix, strings.TrimRight(string(w.buf), "\r"))
		w.buf = nil
	}
}

// prefixWidth is how wide the widest of names is, so that prefixes line up.
func prefixWidth(names []string) int {
	width := 0
	for _, n := range names {
		width = max(width, len(n))
	}
	return width
}
