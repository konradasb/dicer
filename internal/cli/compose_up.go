// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/konradasb/dicer"
	"github.com/konradasb/dicer/internal/compose"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

func newComposeUpCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "up [SERVICE...]",
		Short: "Create and start the project's instances",
		Long: "Creates the networks and volumes the services use, pulls the images the host\n" +
			"does not have, and brings each service's instance up to date with the file:\n" +
			"creating the missing ones, recreating those whose definition has changed, and\n" +
			"starting those that are stopped. Services start after those they depend on,\n" +
			"and those that do not depend on each other start at the same time.\n\n" +
			"Naming services brings up those and what they depend on.\n\n" +
			"The instances' consoles are written out until they all stop, each line\n" +
			"marked with its instance's name. Ctrl+C stops them; a second Ctrl+C stops\n" +
			"waiting. With -d they run in the background instead.",
		Example: "  dicer compose up -d\n" +
			"  dicer compose up -d --wait\n" +
			"  dicer compose up web\n" +
			"  dicer compose up -d --force-recreate --remove-orphans",
		ValidArgsFunction: serviceNames,
		RunE:              runComposeUp,
	}

	flags := cmd.Flags()
	flags.SortFlags = false
	flags.BoolP("detach", "d", false, "Run in the background: return once the instances have started")
	flags.Bool("wait", false, "Return once every instance is running, and healthy if it is checked (implies -d)")
	flags.Bool("force-recreate", false, "Recreate every instance, even those whose definition has not changed")
	flags.Bool("no-recreate", false, "Leave instances that already exist as they are, even if their definition has changed")
	flags.Bool("remove-orphans", false, "Delete the project's instances whose service is no longer in the file")
	flags.String("pull", "missing", "When to pull images: missing, always or never")
	cmd.MarkFlagsMutuallyExclusive("force-recreate", "no-recreate")
	_ = cmd.RegisterFlagCompletionFunc("pull", fixedCompletions("missing", "always", "never"))

	return cmd
}

// upOptions are what up was asked to do.
type upOptions struct {
	forceRecreate bool
	noRecreate    bool
	removeOrphans bool
	pull          string
}

func runComposeUp(cmd *cobra.Command, args []string) error {
	flags := cmd.Flags()
	var opts upOptions
	opts.forceRecreate, _ = flags.GetBool("force-recreate")
	opts.noRecreate, _ = flags.GetBool("no-recreate")
	opts.removeOrphans, _ = flags.GetBool("remove-orphans")
	opts.pull, _ = flags.GetString("pull")
	if !slices.Contains([]string{"missing", "always", "never"}, opts.pull) {
		return usagef(cmd, "invalid --pull %q: want missing, always or never", opts.pull)
	}
	detach, _ := flags.GetBool("detach")
	wait, _ := flags.GetBool("wait")

	p, err := loadProject(cmd)
	if err != nil {
		return err
	}
	services, err := p.Order(args...)
	if err != nil {
		return usagef(cmd, "%s", err)
	}

	client, cleanup, err := newClient(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	u := &upper{
		cmd: cmd, client: client, project: p, opts: opts,
		out: &lines{out: cmd.ErrOrStderr()},
	}
	if err := u.prepare(services); err != nil {
		return err
	}
	if err := u.converge(services); err != nil {
		return err
	}

	switch {
	case wait:
		return u.waitReady(services)
	case detach:
		return nil
	default:
		return attach(cmd, client, u.out, services)
	}
}

// upper brings a project up.
type upper struct {
	cmd     *cobra.Command
	client  *dicer.Client
	project *compose.Project
	opts    upOptions
	out     *lines

	// existing are the project's instances before up began, by name.
	existing map[string]*dicerdv1.Instance

	// said are the lines once has written, by key.
	mu   sync.Mutex
	said map[string]bool
}

func (u *upper) ctx() context.Context { return u.cmd.Context() }

// prepare readies what the services need before any is created: their
// networks, volumes and images. It also deals with orphans.
func (u *upper) prepare(services []*compose.Service) error {
	var err error
	if u.existing, err = projectInstances(u.ctx(), u.client, u.project); err != nil {
		return err
	}

	if orphaned := orphans(u.project, u.existing); len(orphaned) > 0 {
		if !u.opts.removeOrphans {
			u.out.printf("Found instances of services no longer in the file: %s. "+
				"Delete them with --remove-orphans.", andList(orphaned))
		} else {
			for _, name := range orphaned {
				if _, err := u.client.DeleteInstance(u.ctx(), &dicerdv1.DeleteInstanceRequest{
					Name: name, Force: true,
				}); err != nil {
					return fmt.Errorf("delete orphan %s: %w", name, err)
				}
				delete(u.existing, name)
				u.out.printf("Instance %s deleted: its service is no longer in the file", name)
			}
		}
	}

	if err := u.ensureNetworks(services); err != nil {
		return err
	}
	if err := u.ensureVolumes(services); err != nil {
		return err
	}
	return u.ensureImages(services)
}

func (u *upper) ensureNetworks(services []*compose.Service) error {
	for _, key := range slices.Sorted(maps.Keys(u.project.Networks)) {
		n := u.project.Networks[key]
		used := slices.ContainsFunc(services, func(s *compose.Service) bool {
			return s.Instance.GetNetworkName() == n.Name
		})
		if !used {
			continue
		}

		_, err := u.client.GetNetwork(u.ctx(), &dicerdv1.GetNetworkRequest{Name: n.Name})
		switch {
		case err == nil:
			continue
		case status.Code(err) != codes.NotFound:
			return err
		case n.External:
			return fmt.Errorf("external network %s does not exist: create it with dicer network create", n.Name)
		}

		if _, err := u.client.CreateNetwork(u.ctx(), n.Request); err != nil {
			return fmt.Errorf("create network %s: %w", n.Name, err)
		}
		u.out.printf("Network %s created (%s)", n.Name, n.Request.GetSubnet())
	}
	return nil
}

func (u *upper) ensureVolumes(services []*compose.Service) error {
	for _, key := range slices.Sorted(maps.Keys(u.project.Volumes)) {
		v := u.project.Volumes[key]
		used := slices.ContainsFunc(services, func(s *compose.Service) bool {
			return slices.ContainsFunc(s.Instance.GetMounts(), func(m *dicerdv1.Mount) bool {
				return m.GetType() == dicerdv1.MountType_MOUNT_TYPE_VOLUME && m.GetSource() == v.Name
			})
		})
		if !used {
			continue
		}

		_, err := u.client.GetVolume(u.ctx(), &dicerdv1.GetVolumeRequest{Name: v.Name})
		switch {
		case err == nil:
			continue
		case status.Code(err) != codes.NotFound:
			return err
		case v.External:
			return fmt.Errorf("external volume %s does not exist: create it with dicer volume create", v.Name)
		}

		if _, err := u.client.CreateVolume(u.ctx(), v.Request); err != nil {
			return fmt.Errorf("create volume %s: %w", v.Name, err)
		}
		u.out.printf("Volume %s created (%s)", v.Name, size(v.Request.GetSizeBytes()))
	}
	return nil
}

// ensureImages pulls the services' images as --pull says, one at a time so
// that each one's progress can be shown.
func (u *upper) ensureImages(services []*compose.Service) error {
	if u.opts.pull == "never" {
		return nil
	}

	var refs []string
	for _, s := range services {
		if ref := s.Instance.GetImageRef(); !slices.Contains(refs, ref) {
			refs = append(refs, ref)
		}
	}

	for _, ref := range refs {
		var err error
		if u.opts.pull == "always" {
			err = pullShowingProgress(u.cmd, u.client, ref)
		} else {
			err = ensureImage(u.cmd, u.client, ref)
		}
		if err != nil {
			return fmt.Errorf("pull %s: %w", ref, err)
		}
	}
	return nil
}

// converge brings each service's instance up to date, each once those it
// depends on are ready, independent ones at once.
func (u *upper) converge(services []*compose.Service) error {
	type outcome struct {
		done chan struct{}
		err  error
	}
	outcomes := make(map[string]*outcome, len(services))
	for _, s := range services {
		outcomes[s.Name] = &outcome{done: make(chan struct{})}
	}

	var wg sync.WaitGroup
	for _, s := range services {
		o := outcomes[s.Name]
		wg.Go(func() {
			defer close(o.done)

			for _, dep := range s.DependsOn {
				d := outcomes[dep.Service]
				<-d.done
				if d.err != nil {
					o.err = fmt.Errorf("service %s was not started: %s did not come up", s.Name, dep.Service)
					return
				}
				if err := u.waitFor(u.project.Services[dep.Service], dep.Condition); err != nil {
					o.err = fmt.Errorf("service %s was not started: %w", s.Name, err)
					return
				}
			}

			o.err = u.up(s)
		})
	}
	wg.Wait()

	// The errors of services that were not started because another failed
	// only repeat it: the first failure explains the rest.
	var errs []error
	for _, s := range services {
		if err := outcomes[s.Name].err; err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs[0]
}

// up brings one service's instance up to date and running.
func (u *upper) up(s *compose.Service) error {
	name := s.Instance.GetName()
	inst, exists := u.existing[name]

	// The service's instances under other names, from before its
	// container_name changed: its old instance is replaced, as for any
	// other change, and first, since it may hold ports and volumes the new
	// one needs. With --no-recreate the old one is kept instead.
	renamed := slices.DeleteFunc(instancesOf(s, u.existing), func(n string) bool { return n == name })
	switch {
	case len(renamed) == 0:
	case u.opts.noRecreate && !exists:
		name, inst, exists = renamed[0], u.existing[renamed[0]], true
	case !u.opts.noRecreate:
		for _, old := range renamed {
			if _, err := u.client.DeleteInstance(u.ctx(), &dicerdv1.DeleteInstanceRequest{Name: old, Force: true}); err != nil {
				return fmt.Errorf("service %s: delete %s, its instance under its old name: %w", s.Name, old, err)
			}
			u.out.printf("Instance %s deleted: service %s is now instance %s", old, s.Name, name)
		}
	}

	if !exists {
		// Not the project's, but the name may be taken all the same.
		other, err := u.client.GetInstance(u.ctx(), &dicerdv1.GetInstanceRequest{Name: name})
		switch {
		case err == nil:
			return fmt.Errorf("service %s: instance %s already exists and is not this project's: "+
				"rename or delete it, or set the service's container_name", s.Name, other.GetName())
		case status.Code(err) != codes.NotFound:
			return err
		}
		return u.create(s, "started")
	}

	changed := inst.GetLabels()[compose.LabelConfigHash] != s.Instance.GetLabels()[compose.LabelConfigHash]
	if u.opts.forceRecreate || (changed && !u.opts.noRecreate) {
		if _, err := u.client.DeleteInstance(u.ctx(), &dicerdv1.DeleteInstanceRequest{Name: name, Force: true}); err != nil {
			return fmt.Errorf("service %s: delete %s to recreate it: %w", s.Name, name, err)
		}
		return u.create(s, "recreated")
	}

	switch inst.GetState() {
	case stateRunning, statePaused, stateStarting, stateRestarting:
		u.out.printf("Instance %s is up to date", name)
		return nil
	}

	start := time.Now()
	started, err := u.client.StartInstance(u.ctx(), &dicerdv1.StartInstanceRequest{Name: name})
	if err != nil {
		return fmt.Errorf("service %s: start %s: %w", s.Name, name, err)
	}
	u.out.printf("Instance %s started in %s (%s)", name, formatDuration(time.Since(start)), orDash(started.GetIp()))
	return nil
}

// create defines a service's instance and starts it.
func (u *upper) create(s *compose.Service, done string) error {
	req, ok := proto.Clone(s.Instance).(*dicerdv1.CreateInstanceRequest)
	if !ok {
		return errors.New("clone instance definition")
	}
	req.Start = true

	start := time.Now()
	inst, err := u.client.CreateInstance(u.ctx(), req)
	if err != nil {
		return fmt.Errorf("service %s: create %s: %w", s.Name, req.GetName(), err)
	}
	u.out.printf("Instance %s %s in %s (%s)", inst.GetName(), done, formatDuration(time.Since(start)), orDash(inst.GetIp()))
	return nil
}

// waitFor waits until a service meets a condition another depends on. A
// wait is announced, and its end, once however many services wait on it,
// and not at all if the condition is already met.
func (u *upper) waitFor(s *compose.Service, condition compose.Condition) error {
	name := s.Instance.GetName()
	switch condition {
	case compose.ConditionStarted:
		return nil
	case compose.ConditionHealthy:
		if err := u.checkHasHealthCheck(s); err != nil {
			return err
		}
	}

	waited := false
	err := pollInstance(u.ctx(), u.client, name, func(inst *dicerdv1.Instance) (bool, error) {
		ok, err := meets(inst, condition)
		if !ok && err == nil && !waited {
			waited = true
			u.once("wait "+name+" "+string(condition), func() {
				if condition == compose.ConditionHealthy {
					u.out.printf("Waiting for %s to be healthy", name)
				} else {
					u.out.printf("Waiting for %s to finish", name)
				}
			})
		}
		return ok, err
	})
	if err != nil || !waited {
		return err
	}

	u.once("done "+name+" "+string(condition), func() {
		if condition == compose.ConditionHealthy {
			u.out.printf("Instance %s is healthy", name)
		} else {
			u.out.printf("Instance %s finished", name)
		}
	})
	return nil
}

// once calls fn the first time it is given key.
func (u *upper) once(key string, fn func()) {
	u.mu.Lock()
	if u.said == nil {
		u.said = make(map[string]bool)
	}
	said := u.said[key]
	u.said[key] = true
	u.mu.Unlock()

	if !said {
		fn()
	}
}

// checkHasHealthCheck fails for a service that has no health check to wait
// for: none of its own, and none in its image.
func (u *upper) checkHasHealthCheck(s *compose.Service) error {
	check := s.Instance.GetHealthCheck()
	if check == nil {
		img, err := u.client.GetImage(u.ctx(), &dicerdv1.GetImageRequest{Ref: s.Instance.GetImageRef()})
		if err != nil {
			return err
		}
		check = img.GetHealthCheck()
	}
	if check == nil || check.GetDisabled() {
		return fmt.Errorf("service %s has no health check to wait for: give it a healthcheck", s.Name)
	}
	return nil
}

// meets reports whether an instance meets a condition yet, and fails if it
// never will.
func meets(inst *dicerdv1.Instance, condition compose.Condition) (bool, error) {
	name, state := inst.GetName(), inst.GetState()

	switch condition {
	case compose.ConditionHealthy:
		switch {
		case inst.GetHealth().GetStatus() == healthHealthy:
			return true, nil
		case inst.GetHealth().GetStatus() == healthUnhealthy:
			return false, fmt.Errorf("instance %s is unhealthy: %s", name, firstLine(inst.GetHealth().GetLastOutput()))
		case state == stateStopped || state == stateFailed:
			return false, fmt.Errorf("instance %s stopped before it was healthy", name)
		}
		return false, nil
	case compose.ConditionCompletedSuccessfully:
		switch {
		case state == stateFailed:
			return false, fmt.Errorf("instance %s failed: %s", name, firstLine(inst.GetStateError()))
		case state == stateStopped && inst.ExitCode != nil && inst.GetExitCode() == 0:
			return true, nil
		case state == stateStopped && inst.ExitCode != nil:
			return false, fmt.Errorf("instance %s exited with code %d", name, inst.GetExitCode())
		case state == stateStopped:
			return false, fmt.Errorf("instance %s stopped without saying how it ended", name)
		}
		return false, nil
	default:
		return isActive(state), nil
	}
}

// pollInstance looks at an instance until done says it is done, or fails.
func pollInstance(
	ctx context.Context, client *dicer.Client, name string, done func(*dicerdv1.Instance) (bool, error),
) error {
	for {
		inst, err := client.GetInstance(ctx, &dicerdv1.GetInstanceRequest{Name: name})
		if err != nil {
			return err
		}
		ok, err := done(inst)
		if err != nil || ok {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(composePollInterval):
		}
	}
}

// waitReady waits for every service to be running, and healthy if it or its
// image has a health check. A service that others wait on to finish is left to.
func (u *upper) waitReady(services []*compose.Service) error {
	awaitedToFinish := make(map[string]bool)
	for _, s := range services {
		for _, dep := range s.DependsOn {
			if dep.Condition == compose.ConditionCompletedSuccessfully {
				awaitedToFinish[dep.Service] = true
			}
		}
	}

	for _, s := range services {
		if awaitedToFinish[s.Name] {
			continue
		}
		name := s.Instance.GetName()
		checked := u.checkHasHealthCheck(s) == nil
		err := pollInstance(u.ctx(), u.client, name, func(inst *dicerdv1.Instance) (bool, error) {
			if checked {
				return meets(inst, compose.ConditionHealthy)
			}
			switch inst.GetState() {
			case stateRunning:
				return true, nil
			case stateStopped, stateFailed:
				return false, fmt.Errorf("instance %s is %s", name, enumName(inst.GetState()))
			}
			return false, nil
		})
		if err != nil {
			return err
		}
	}

	u.out.printf("Every instance is ready")
	return nil
}

// attach writes the services' consoles, each line marked with its
// instance's name, until they have all stopped. Ctrl+C stops them, in the
// reverse of the order they started; a second stops waiting for them.
func attach(cmd *cobra.Command, client *dicer.Client, out *lines, services []*compose.Service) error {
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	names := make([]string, 0, len(services))
	for _, s := range services {
		names = append(names, s.Instance.GetName())
	}
	width := prefixWidth(names)
	console := &lines{out: cmd.OutOrStdout()}

	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() {
			w := newPrefixedWriter(console, name, width, i)
			err := streamLogs(ctx, client, &dicerdv1.GetInstanceLogsRequest{Name: name, Follow: true}, w)
			w.flush()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				out.printf("Error: cannot read the console of %s: %s", name, errorMessage(err))
				return
			}
			out.printf("%s", endedLine(ctx, client, name))
		})
	}

	allStopped := make(chan struct{})
	go func() {
		wg.Wait()
		close(allStopped)
	}()

	interrupts := make(chan os.Signal, 2)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupts)

	stopping := false
	for {
		select {
		case <-allStopped:
			return nil
		case <-interrupts:
			if stopping {
				out.printf("The instances are left to stop on their own")
				return &exitError{code: statusInterrupted}
			}
			stopping = true
			out.printf("Stopping the instances; press Ctrl+C again to stop waiting for them")
			go func() {
				for _, name := range slices.Backward(names) {
					_, err := client.StopInstance(ctx, &dicerdv1.StopInstanceRequest{Name: name})
					if err != nil && !errors.Is(err, context.Canceled) && status.Code(err) != codes.FailedPrecondition {
						out.printf("Error: stop %s: %s", name, errorMessage(err))
					}
				}
			}()
		}
	}
}

// endedLine says how an instance whose console has ended ended.
func endedLine(ctx context.Context, client *dicer.Client, name string) string {
	inst, err := client.GetInstance(ctx, &dicerdv1.GetInstanceRequest{Name: name})
	switch {
	case err != nil:
		return fmt.Sprintf("Instance %s stopped", name)
	case inst.ExitCode != nil:
		return fmt.Sprintf("Instance %s exited with code %d", name, inst.GetExitCode())
	case inst.GetState() == stateFailed:
		return fmt.Sprintf("Instance %s failed: %s", name, firstLine(inst.GetStateError()))
	default:
		return fmt.Sprintf("Instance %s stopped", name)
	}
}
