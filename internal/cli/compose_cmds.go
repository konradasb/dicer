// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/konradasb/dicer"
	"github.com/konradasb/dicer/internal/compose"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// composeSession is what most commands start with: the project, and a
// connection to the daemon.
type composeSession struct {
	cmd     *cobra.Command
	project *compose.Project
	client  *dicer.Client
	close   func()
}

func openCompose(cmd *cobra.Command) (*composeSession, error) {
	p, err := loadProject(cmd)
	if err != nil {
		return nil, err
	}
	client, cleanup, err := newClient(cmd)
	if err != nil {
		return nil, err
	}
	return &composeSession{cmd: cmd, project: p, client: client, close: cleanup}, nil
}

func newComposeDownCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Stop and delete the project's instances and networks",
		Long: "Stops and deletes the project's instances, in the reverse of the order they\n" +
			"start in, then deletes the networks the file declares. Volumes are kept, so\n" +
			"that their data outlives the instances, unless --volumes is given. External\n" +
			"networks and volumes are never deleted.",
		Example: "  dicer compose down\n" +
			"  dicer compose down --volumes --remove-orphans",
		Args: noArgs,
		RunE: runComposeDown,
	}

	cmd.Flags().BoolP("volumes", "v", false, "Delete the volumes the file declares too, and the data on them")
	cmd.Flags().Bool("remove-orphans", false, "Delete the project's instances whose service is no longer in the file too")

	return cmd
}

func runComposeDown(cmd *cobra.Command, _ []string) error {
	removeVolumes, _ := cmd.Flags().GetBool("volumes")
	removeOrphans, _ := cmd.Flags().GetBool("remove-orphans")

	s, err := openCompose(cmd)
	if err != nil {
		return err
	}
	defer s.close()
	ctx, p, client := cmd.Context(), s.project, s.client
	out := &lines{out: cmd.ErrOrStderr()}

	instances, err := projectInstances(ctx, client, p)
	if err != nil {
		return err
	}

	order, err := p.Order()
	if err != nil {
		return err
	}
	var names []string
	for _, svc := range slices.Backward(order) {
		names = append(names, instancesOf(svc, instances)...)
	}
	if orphaned := orphans(p, instances); len(orphaned) > 0 {
		if removeOrphans {
			names = append(orphaned, names...)
		} else {
			out.printf("Found instances of services no longer in the file: %s. "+
				"Delete them with --remove-orphans.", andList(orphaned))
		}
	}

	for _, name := range names {
		start := time.Now()
		if _, err := client.DeleteInstance(ctx, &dicerdv1.DeleteInstanceRequest{Name: name, Force: true}); err != nil {
			return fmt.Errorf("delete %s: %w", name, err)
		}
		out.printf("Instance %s deleted in %s", name, formatDuration(time.Since(start)))
	}

	for _, key := range slices.Sorted(maps.Keys(p.Networks)) {
		n := p.Networks[key]
		if n.External {
			continue
		}
		_, err := client.DeleteNetwork(ctx, &dicerdv1.DeleteNetworkRequest{Name: n.Name})
		switch {
		case err == nil:
			out.printf("Network %s deleted", n.Name)
		case status.Code(err) == codes.NotFound:
		default:
			out.printf("Network %s kept: %s", n.Name, errorMessage(err))
		}
	}

	if !removeVolumes {
		return nil
	}
	for _, key := range slices.Sorted(maps.Keys(p.Volumes)) {
		v := p.Volumes[key]
		if v.External {
			continue
		}
		_, err := client.DeleteVolume(ctx, &dicerdv1.DeleteVolumeRequest{Name: v.Name})
		switch {
		case err == nil:
			out.printf("Volume %s deleted", v.Name)
		case status.Code(err) == codes.NotFound:
		default:
			out.printf("Volume %s kept: %s", v.Name, errorMessage(err))
		}
	}
	return nil
}

// printableService is a project's instances as ps lists them.
type printableService struct {
	Instances []*dicerdv1.Instance
}

func (p *printableService) Cols() []string {
	return []string{"Name", "Service", "Image", "State", "Status", "IP", "Ports"}
}

func (p *printableService) DefaultCols() []string {
	return []string{"Name", "Service", "Image", "Status", "IP", "Ports"}
}

func (p *printableService) KV() []map[string]any {
	kv := make([]map[string]any, 0, len(p.Instances))
	for _, inst := range p.Instances {
		kv = append(kv, map[string]any{
			"Name":    inst.GetName(),
			"Service": orDash(inst.GetLabels()[compose.LabelService]),
			"Image":   inst.GetImageRef(),
			"State":   stateName(inst.GetState()),
			"Status":  instanceStatus(inst),
			"IP":      orDash(inst.GetIp()),
			"Ports":   orDash(formatPorts(inst.GetPorts())),
		})
	}
	return kv
}

func newComposePsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ps [SERVICE...]",
		Short: "List the project's instances",
		Example: "  dicer compose ps\n" +
			"  dicer compose ps -q web\n" +
			"  dicer compose ps --format json",
		ValidArgsFunction: serviceNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openCompose(cmd)
			if err != nil {
				return err
			}
			defer s.close()

			instances, err := projectInstances(cmd.Context(), s.client, s.project)
			if err != nil {
				return err
			}
			for _, name := range args {
				if _, ok := s.project.Services[name]; !ok {
					return usagef(cmd, "no such service: %s", name)
				}
			}

			var shown []*dicerdv1.Instance
			for _, name := range slices.Sorted(maps.Keys(instances)) {
				inst := instances[name]
				if len(args) == 0 || slices.Contains(args, inst.GetLabels()[compose.LabelService]) {
					shown = append(shown, inst)
				}
			}
			return render(cmd, &printableService{Instances: shown})
		},
	}

	addOutputFlags(cmd, true)
	cmd.Flags().Bool("wide", false, "Show every column")
	cmd.MarkFlagsMutuallyExclusive("wide", "columns")
	// Accepted for docker compose compatibility; stopped instances are
	// always listed.
	cmd.Flags().BoolP("all", "a", false, "Accepted for Docker compatibility; every instance is always listed")
	_ = cmd.Flags().MarkHidden("all")

	return cmd
}

func newComposeLogsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs [SERVICE...]",
		Short: "Show the consoles of the project's instances",
		Long: "Shows the guest consoles of the services' instances, each line marked with\n" +
			"its instance's name. With -f, keeps writing new output until they stop.",
		Example: "  dicer compose logs\n" +
			"  dicer compose logs -f web worker\n" +
			"  dicer compose logs -n 20 db",
		ValidArgsFunction: serviceNames,
		RunE:              runComposeLogs,
	}

	cmd.Flags().BoolP("follow", "f", false, "Keep writing new output until the instances stop")
	cmd.Flags().Int32P("tail", "n", 0, "Show only the last lines of each (default: all)")
	cmd.Flags().Bool("no-prefix", false, "Do not mark each line with its instance's name")

	return cmd
}

func runComposeLogs(cmd *cobra.Command, args []string) error {
	follow, _ := cmd.Flags().GetBool("follow")
	tail, _ := cmd.Flags().GetInt32("tail")
	noPrefix, _ := cmd.Flags().GetBool("no-prefix")

	s, err := openCompose(cmd)
	if err != nil {
		return err
	}
	defer s.close()

	services, err := s.project.Select(args...)
	if err != nil {
		return usagef(cmd, "%s", err)
	}
	instances, err := projectInstances(cmd.Context(), s.client, s.project)
	if err != nil {
		return err
	}

	var names []string
	for _, svc := range services {
		if inst, err := serviceInstance(svc, instances); err == nil {
			names = append(names, inst.GetName())
		}
	}

	console := &lines{out: cmd.OutOrStdout()}
	width := prefixWidth(names)
	errs := make([]error, len(names))

	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() {
			w := newPrefixedWriter(console, name, width, i)
			if noPrefix {
				w.prefix = ""
			}
			err := streamLogs(cmd.Context(), s.client, &dicerdv1.GetInstanceLogsRequest{
				Name: name, TailLines: tail, Follow: follow,
			}, w)
			w.flush()
			// An instance never started has no console yet: nothing to show.
			if status.Code(err) != codes.NotFound {
				errs[i] = err
			}
		})
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// newComposeLifecycleCommand makes start, stop or restart: do, for each
// service named, or every one, in order.
func newComposeLifecycleCommand(
	use, short string, reverse bool,
	do func(s *composeSession, inst *dicerdv1.Instance, out *lines) error,
) *cobra.Command {
	return &cobra.Command{
		Use:               use + " [SERVICE...]",
		Short:             short,
		ValidArgsFunction: serviceNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openCompose(cmd)
			if err != nil {
				return err
			}
			defer s.close()

			services, err := s.project.Select(args...)
			if err != nil {
				return usagef(cmd, "%s", err)
			}
			if reverse {
				slices.Reverse(services)
			}
			instances, err := projectInstances(cmd.Context(), s.client, s.project)
			if err != nil {
				return err
			}

			out := &lines{out: cmd.ErrOrStderr()}
			for _, svc := range services {
				inst, err := serviceInstance(svc, instances)
				if err != nil {
					return err
				}
				if err := do(s, inst, out); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func newComposeStartCommand() *cobra.Command {
	cmd := newComposeLifecycleCommand("start", "Start the project's stopped instances", false, startService)
	cmd.Long = "Starts the services' instances that are not running, each after those it\n" +
		"depends on. It does not create them: see dicer compose up."
	return cmd
}

func newComposeStopCommand() *cobra.Command {
	cmd := newComposeLifecycleCommand("stop", "Stop the project's running instances", true, stopService)
	cmd.Long = "Stops the services' instances that are running, each before those it depends\n" +
		"on, and keeps them to be started again."
	return cmd
}

func newComposeRestartCommand() *cobra.Command {
	cmd := newComposeLifecycleCommand("restart", "Restart the project's instances", false,
		func(s *composeSession, inst *dicerdv1.Instance, out *lines) error {
			if err := stopService(s, inst, out); err != nil {
				return err
			}
			return startInstance(s, inst.GetName(), out)
		})
	cmd.Long = "Stops each of the services' instances if it is running, then starts it. It is\n" +
		"how a changed file mount takes effect; a changed definition needs\n" +
		"dicer compose up."
	return cmd
}

func startService(s *composeSession, inst *dicerdv1.Instance, out *lines) error {
	if isActive(inst.GetState()) {
		return nil
	}
	return startInstance(s, inst.GetName(), out)
}

// startInstance starts an instance, whatever state it was last seen in.
func startInstance(s *composeSession, name string, out *lines) error {
	start := time.Now()
	started, err := s.client.StartInstance(s.cmd.Context(), &dicerdv1.StartInstanceRequest{Name: name})
	if err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	out.printf("Instance %s started in %s (%s)", started.GetName(), formatDuration(time.Since(start)), orDash(started.GetIp()))
	return nil
}

func stopService(s *composeSession, inst *dicerdv1.Instance, out *lines) error {
	switch inst.GetState() {
	case stateRunning, statePaused, stateStarting, stateRestarting:
	default:
		return nil
	}

	start := time.Now()
	if _, err := s.client.StopInstance(s.cmd.Context(), &dicerdv1.StopInstanceRequest{Name: inst.GetName()}); err != nil {
		return fmt.Errorf("stop %s: %w", inst.GetName(), err)
	}
	out.printf("Instance %s stopped in %s", inst.GetName(), formatDuration(time.Since(start)))
	return nil
}

func newComposePullCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "pull [SERVICE...]",
		Short: "Pull the services' images",
		Long: "Pulls each service's image, even one the host already has, so that a tag\n" +
			"that has moved is brought up to date. Instances pick a new image up when\n" +
			"they are next created: see dicer compose up --force-recreate.",
		ValidArgsFunction: serviceNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openCompose(cmd)
			if err != nil {
				return err
			}
			defer s.close()

			services, err := s.project.Select(args...)
			if err != nil {
				return usagef(cmd, "%s", err)
			}

			var pulled []string
			for _, svc := range services {
				ref := svc.Instance.GetImageRef()
				if slices.Contains(pulled, ref) {
					continue
				}
				pulled = append(pulled, ref)
				if err := pullShowingProgress(cmd, s.client, ref); err != nil {
					return fmt.Errorf("pull %s: %w", ref, err)
				}
			}
			return nil
		},
	}
}

func newComposeExecCommand() *cobra.Command {
	cmd := newInstanceExecCommand()
	cmd.Use = "exec [flags] SERVICE [COMMAND [ARG...]]"
	cmd.Short = "Run a command inside a service's running instance"
	cmd.Long = "Runs a command inside a service's instance, /bin/sh if none is given, as\n" +
		"dicer exec does. Flags go before the service: everything after it is the\n" +
		"command's."
	cmd.Example = "  dicer compose exec db\n" +
		"  dicer compose exec db psql -U postgres\n" +
		"  dicer compose exec -T web cat /etc/nginx/nginx.conf > nginx.conf"
	cmd.Args = oneThenCommand("a service")
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveDefault
		}
		return serviceNames(cmd, args, toComplete)
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		s, err := openCompose(cmd)
		if err != nil {
			return err
		}
		defer s.close()

		svc, ok := s.project.Services[args[0]]
		if !ok {
			return usagef(cmd, "no such service: %s", args[0])
		}
		instances, err := projectInstances(cmd.Context(), s.client, s.project)
		if err != nil {
			return err
		}
		inst, err := serviceInstance(svc, instances)
		if err != nil {
			return err
		}
		return runInstanceExecCommand(cmd, append([]string{inst.GetName()}, args[1:]...))
	}
	return cmd
}

func newComposeConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Check the compose file and show it resolved",
		Long: "Checks the compose file, and shows it as dicer compose reads it: with its\n" +
			"anchors expanded and its x- extensions left out. It does not talk to the\n" +
			"daemon.\n\n" +
			"Variables are shown as they are written, ${DB_PASSWORD}, so that the output\n" +
			"can be shared without the values in .env or the environment: they are\n" +
			"still substituted to check the file. --interpolate shows the values.",
		Example: "  dicer compose config\n" +
			"  dicer compose config --interpolate\n" +
			"  dicer compose config --services\n" +
			"  dicer compose -f staging.yaml config -q && echo valid",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := loadProject(cmd)
			if err != nil {
				return err
			}

			w := cmd.OutOrStdout()
			switch {
			case mustBool(cmd, "quiet"):
				return nil
			case mustBool(cmd, "services"):
				for _, name := range p.ServiceNames() {
					_, _ = fmt.Fprintln(w, name)
				}
			case mustBool(cmd, "volumes"):
				for _, key := range slices.Sorted(maps.Keys(p.Volumes)) {
					_, _ = fmt.Fprintln(w, key)
				}
			case mustBool(cmd, "networks"):
				for _, key := range slices.Sorted(maps.Keys(p.Networks)) {
					_, _ = fmt.Fprintln(w, key)
				}
			case mustBool(cmd, "interpolate"):
				_, _ = w.Write(p.Resolved)
			default:
				_, _ = w.Write(p.Written)
			}
			return nil
		},
	}

	cmd.Flags().BoolP("quiet", "q", false, "Only check the file; print nothing")
	cmd.Flags().Bool("services", false, "List the services, one a line")
	cmd.Flags().Bool("volumes", false, "List the volumes, one a line")
	cmd.Flags().Bool("networks", false, "List the networks, one a line")
	cmd.Flags().Bool("interpolate", false, "Show variables' values, from .env and the environment, instead of the variables")
	cmd.MarkFlagsMutuallyExclusive("quiet", "services", "volumes", "networks", "interpolate")

	return cmd
}

// mustBool returns a boolean flag's value.
func mustBool(cmd *cobra.Command, name string) bool {
	v, _ := cmd.Flags().GetBool(name)
	return v
}
