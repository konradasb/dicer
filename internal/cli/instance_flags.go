// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Instance flags: turning command-line flags into a create or an update,
// and parsing the individual flag values.

package cli

import (
	"bufio"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/docker/go-units"
	"github.com/spf13/cobra"

	"github.com/konradasb/dicer"
	"github.com/konradasb/dicer/internal/naming"
)

// instanceCreate is what create and run ask of the daemon: an instance's
// definition, and how to create it.
type instanceCreate struct {
	spec dicer.InstanceSpec
	opts dicer.CreateOptions
}

// parseRestartPolicy parses a restart policy as --restart takes it,
// "on-failure:5".
func parseRestartPolicy(s string) (dicer.RestartPolicy, error) {
	modeName, retries, hasRetries := strings.Cut(s, ":")
	mode, err := parseChoice("restart policy", modeName, restartModes)
	if err != nil {
		return dicer.RestartPolicy{}, err
	}
	p := dicer.RestartPolicy{Mode: mode}

	if hasRetries {
		n, err := strconv.ParseInt(retries, 10, 32)
		if err != nil || n < 0 {
			return dicer.RestartPolicy{}, fmt.Errorf("invalid restart policy %q: want on-failure:N, with N a whole number", s)
		}
		p.MaxRetries = int(n)
	}

	return p, nil
}

// addInstanceSpecFlags adds the flags that describe an instance, shared by
// create, run and update (which passes withDefaults false).
func addInstanceSpecFlags(cmd *cobra.Command, withDefaults bool) {
	vcpus, memory, disk := 1, "512MiB", "10GiB"
	if !withDefaults {
		vcpus, memory, disk = 0, "", ""
	}

	flags := cmd.Flags()
	flags.String("kernel", "", "Kernel to boot with (default: the default kernel)")
	flags.String("kernel-args", "", "Kernel command line arguments")
	flags.String("hypervisor-type", "", "Hypervisor: cloud-hypervisor or firecracker (default: cloud-hypervisor)")
	flags.String("hypervisor-version", "", "Hypervisor version (default: the hypervisor's default version, which 'dicer info' shows)")
	flags.Int("vcpus", vcpus, "Number of virtual CPUs")
	flags.StringP("memory", "m", memory, "Memory, e.g. 512MiB or 2GiB")
	flags.Int("max-vcpus", 0, "Most vCPUs 'dicer resize' can give the running instance, on Cloud Hypervisor (0: none)")
	flags.String("max-memory", "", "Most memory 'dicer resize' can give the running instance, e.g. 4GiB (0: none)")
	flags.String("disk", disk, "Overlay disk size, e.g. 10GiB")
	flags.String("disk-rate", "", "Bytes per second each disk can be read and written at, e.g. 50MiB (0: unlimited)")
	flags.Int64("disk-iops", 0, "Operations per second each disk can be read and written at (0: unlimited)")
	flags.String("upload-rate", "", "Bytes per second the guest can send, e.g. 10MiB (0: unlimited)")
	flags.String("download-rate", "", "Bytes per second the guest can receive, e.g. 10MiB (0: unlimited)")
	flags.Duration("standby-after", 0, "Put the instance on standby once it has been idle this long, e.g. 15m (0: never)")
	flags.String("network", "", "Network to attach to (default: the default network)")
	flags.String("ip", "", "Static IP address (default: assigned from the subnet)")
	flags.StringArrayP("publish", "p", nil,
		"Publish a guest port on the host, as [hostIP:]hostPort:guestPort[/tcp|udp] (repeatable)")
	flags.StringArray("mount", nil,
		"Mount a volume, a file on this machine or a tmpfs, as [type=volume|file|tmpfs,][source=...,]target=/path[,readonly] (repeatable)")
	flags.StringArrayP("env", "e", nil,
		"Environment variable as KEY=VALUE, or KEY to pass this shell's value (repeatable)")
	flags.StringArray("env-file", nil, "Read environment variables from a file of KEY=VALUE lines (repeatable)")
	flags.StringArrayP("label", "l", nil, "Label as KEY=VALUE (repeatable)")
	flags.String("hostname", "", "Guest hostname (default: the instance name)")
	flags.String("restart", "",
		"Restart policy when the instance ends on its own: no, on-failure[:max-retries], unless-stopped or always (default no)")
	flags.Bool("rm", false, "Delete the instance once it stops, the daemon doing the deleting")

	_ = cmd.RegisterFlagCompletionFunc("kernel", complete(0, listKernels))
	_ = cmd.RegisterFlagCompletionFunc("network", complete(0, listNetworks))
	_ = cmd.RegisterFlagCompletionFunc("hypervisor-type", fixedCompletions("cloud-hypervisor", "firecracker"))
	addHealthCheckFlags(flags)
	flags.String("init-mode", "",
		"How the guest starts the command: auto, exec (as PID 1 of its own PID namespace) or systemd (default auto)")
	_ = cmd.RegisterFlagCompletionFunc("init-mode", fixedCompletions("auto", "exec", "systemd"))
	_ = cmd.RegisterFlagCompletionFunc("restart", fixedCompletions("no", "on-failure", "unless-stopped", "always"))
	_ = cmd.MarkFlagFilename("env-file")
}

// addPullFlag adds --pull, which says when the image is pulled, as docker
// run's does.
func addPullFlag(cmd *cobra.Command, usage string) {
	cmd.Flags().String("pull", "missing", usage+": missing, always or never")
	_ = cmd.RegisterFlagCompletionFunc("pull", fixedCompletions("missing", "always", "never"))
}

// pullPolicyFlag returns the pull policy --pull names.
func pullPolicyFlag(cmd *cobra.Command) (dicer.PullPolicy, error) {
	v, _ := cmd.Flags().GetString("pull")
	policy, err := parseChoice("--pull", v, pullPolicies)
	if err != nil {
		return "", usagef(cmd, "%s", err)
	}
	return policy, nil
}

// createArgs accepts at most one argument, the name, before a -- that
// introduces the command.
func createArgs(cmd *cobra.Command, args []string) error {
	return needs(nil, "an instance name")(cmd, positionalArgs(cmd, args))
}

// positionalArgs returns the arguments before a --, or all of them if there
// is none.
func positionalArgs(cmd *cobra.Command, args []string) []string {
	if dash := cmd.ArgsLenAtDash(); dash >= 0 {
		return args[:dash]
	}
	return args
}

// commandArgs returns the arguments after a --, or nil if there is none.
func commandArgs(cmd *cobra.Command, args []string) []string {
	if dash := cmd.ArgsLenAtDash(); dash >= 0 {
		return args[dash:]
	}
	return nil
}

// buildCreate assembles a create from the command's arguments and flags.
func buildCreate(cmd *cobra.Command, args []string) (instanceCreate, error) {
	var c instanceCreate

	if name := positionalArgs(cmd, args); len(name) > 0 {
		c.spec.Name = name[0]
	}
	if command := commandArgs(cmd, args); len(command) > 0 {
		c.spec.Cmd = command
	}
	if cmd.Flags().Changed("image") {
		c.spec.ImageRef, _ = cmd.Flags().GetString("image")
	}

	if err := applySpecFlags(cmd, &c.spec); err != nil {
		return c, usagef(cmd, "%s", err)
	}
	c.opts.Start, _ = cmd.Flags().GetBool("start")

	var err error
	if c.opts.PullPolicy, err = pullPolicyFlag(cmd); err != nil {
		return c, err
	}

	if c.spec.Name == "" {
		return c, usagef(cmd, "%s needs an instance name", cmd.CommandPath())
	}

	return c, nil
}

// buildRun assembles what 'dicer run IMAGE [COMMAND...]' asks for: a create
// that also starts the instance.
func buildRun(cmd *cobra.Command, args []string) (instanceCreate, error) {
	c := instanceCreate{
		spec: dicer.InstanceSpec{ImageRef: args[0], Cmd: trimDash(args[1:])},
		opts: dicer.CreateOptions{Start: true},
	}

	c.spec.Name, _ = cmd.Flags().GetString("name")
	if c.spec.Name == "" {
		c.spec.Name = generateName(c.spec.ImageRef)
	}

	if err := applySpecFlags(cmd, &c.spec); err != nil {
		return c, usagef(cmd, "%s", err)
	}

	var err error
	if c.opts.PullPolicy, err = pullPolicyFlag(cmd); err != nil {
		return c, err
	}

	return c, nil
}

// trimDash drops a -- that leads a command: with flags not interspersed,
// 'dicer run alpine -- ls' leaves it among the arguments.
func trimDash(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return nil
	}
	return args
}

// applySpecFlags sets the fields of spec for the flags given, and the sizes,
// whose defaults are the command line's.
func applySpecFlags(cmd *cobra.Command, spec *dicer.InstanceSpec) error {
	flags := cmd.Flags()
	setString := func(flag string, dst *string) {
		if flags.Changed(flag) {
			*dst, _ = flags.GetString(flag)
		}
	}
	setString("kernel", &spec.KernelName)
	setString("kernel-args", &spec.KernelArgs)
	setString("hypervisor-version", &spec.HypervisorVersion)
	setString("network", &spec.NetworkName)
	setString("ip", &spec.StaticIP)
	setString("hostname", &spec.Hostname)

	var err error
	if flags.Changed("hypervisor-type") {
		v, _ := flags.GetString("hypervisor-type")
		if spec.HypervisorType, err = parseChoice("hypervisor", v, hypervisorTypes); err != nil {
			return err
		}
	}
	if flags.Changed("init-mode") {
		v, _ := flags.GetString("init-mode")
		if spec.InitMode, err = parseChoice("init mode", v, initModes); err != nil {
			return err
		}
	}
	if flags.Changed("restart") {
		v, _ := flags.GetString("restart")
		if spec.RestartPolicy, err = parseRestartPolicy(v); err != nil {
			return err
		}
	}
	if flags.Changed("rm") {
		spec.RemoveOnExit, _ = flags.GetBool("rm")
	}
	switch hc, err := healthCheckFromFlags(cmd); {
	case err != nil:
		return err
	case hc != nil:
		spec.HealthCheck = hc
	}

	// Sizes are always given: the flags' defaults are the command line's.
	spec.VCPUs, _ = flags.GetInt("vcpus")
	memory, _ := flags.GetString("memory")
	if spec.MemoryBytes, err = parseMemoryBytes(memory); err != nil {
		return err
	}
	disk, _ := flags.GetString("disk")
	if spec.DiskBytes, err = parseDiskBytes(disk); err != nil {
		return err
	}
	spec.MaxVCPUs, _ = flags.GetInt("max-vcpus")
	if flags.Changed("max-memory") {
		v, _ := flags.GetString("max-memory")
		if spec.MaxMemoryBytes, err = parseMemoryBytes(v); err != nil {
			return err
		}
	}
	for flag, dst := range map[string]*int64{
		"disk-rate":     &spec.DiskBytesPerSecond,
		"upload-rate":   &spec.UploadBytesPerSecond,
		"download-rate": &spec.DownloadBytesPerSecond,
	} {
		if flags.Changed(flag) {
			v, _ := flags.GetString(flag)
			if *dst, err = parseBytesPerSecond(flag, v); err != nil {
				return err
			}
		}
	}
	spec.DiskIOPS, _ = flags.GetInt64("disk-iops")
	spec.StandbyAfter, _ = flags.GetDuration("standby-after")

	lists, err := parseListFlags(cmd)
	if err != nil {
		return err
	}
	if lists.ports != nil {
		spec.Ports = lists.ports
	}
	if lists.mounts != nil {
		spec.Mounts = lists.mounts
	}
	if lists.env != nil {
		spec.Env = lists.env
	}
	if lists.labels != nil {
		spec.Labels = lists.labels
	}

	return nil
}

// buildUpdate assembles an update of the instance args name that changes
// only what its flags were given for.
func buildUpdate(cmd *cobra.Command, args []string) (dicer.InstanceUpdate, error) {
	update := dicer.InstanceUpdate{Cmd: commandArgs(cmd, args)}

	flags := cmd.Flags()
	optionalString := func(flag string) *string {
		if !flags.Changed(flag) {
			return nil
		}
		v, _ := flags.GetString(flag)

		return &v
	}

	update.ImageRef = optionalString("image")
	update.KernelName = optionalString("kernel")
	update.KernelArgs = optionalString("kernel-args")
	update.HypervisorVersion = optionalString("hypervisor-version")
	update.NetworkName = optionalString("network")
	update.StaticIP = optionalString("ip")
	update.Hostname = optionalString("hostname")

	var err error
	if v := optionalString("hypervisor-type"); v != nil {
		if update.HypervisorType, err = parseChoice("hypervisor", *v, hypervisorTypes); err != nil {
			return update, usagef(cmd, "%s", err)
		}
	}
	if v := optionalString("init-mode"); v != nil {
		if update.InitMode, err = parseChoice("init mode", *v, initModes); err != nil {
			return update, usagef(cmd, "%s", err)
		}
	}
	if flags.Changed("vcpus") {
		v, _ := flags.GetInt("vcpus")
		update.VCPUs = &v
	}
	if v := optionalString("memory"); v != nil {
		bytes, err := parseMemoryBytes(*v)
		if err != nil {
			return update, usagef(cmd, "%s", err)
		}
		update.MemoryBytes = &bytes
	}
	if flags.Changed("max-vcpus") {
		v, _ := flags.GetInt("max-vcpus")
		update.MaxVCPUs = &v
	}
	if v := optionalString("max-memory"); v != nil {
		bytes, err := parseMemoryBytes(*v)
		if err != nil {
			return update, usagef(cmd, "%s", err)
		}
		update.MaxMemoryBytes = &bytes
	}
	if v := optionalString("disk"); v != nil {
		bytes, err := parseDiskBytes(*v)
		if err != nil {
			return update, usagef(cmd, "%s", err)
		}
		update.DiskBytes = &bytes
	}
	for flag, dst := range map[string]**int64{
		"disk-rate":     &update.DiskBytesPerSecond,
		"upload-rate":   &update.UploadBytesPerSecond,
		"download-rate": &update.DownloadBytesPerSecond,
	} {
		if v := optionalString(flag); v != nil {
			bytes, err := parseBytesPerSecond(flag, *v)
			if err != nil {
				return update, usagef(cmd, "%s", err)
			}
			*dst = &bytes
		}
	}
	if flags.Changed("disk-iops") {
		v, _ := flags.GetInt64("disk-iops")
		update.DiskIOPS = &v
	}
	if flags.Changed("standby-after") {
		d, _ := flags.GetDuration("standby-after")
		update.StandbyAfter = &d
	}
	if flags.Changed("restart") {
		v, _ := flags.GetString("restart")
		policy, err := parseRestartPolicy(v)
		if err != nil {
			return update, usagef(cmd, "%s", err)
		}
		update.RestartPolicy = &policy
	}
	if flags.Changed("rm") {
		v, _ := flags.GetBool("rm")
		update.RemoveOnExit = &v
	}
	if update.HealthCheck, err = healthCheckFromFlags(cmd); err != nil {
		return update, usagef(cmd, "%s", err)
	}

	lists, err := parseListFlags(cmd)
	if err != nil {
		return update, usagef(cmd, "%s", err)
	}
	update.Ports = lists.ports
	update.Mounts = lists.mounts
	update.Env = lists.env
	update.Labels = lists.labels

	if reflect.ValueOf(update).IsZero() {
		return update, usagef(cmd, "%s needs something to change: a flag, or a command after --", cmd.CommandPath())
	}

	return update, nil
}

// specLists are the list and map flags of an instance's definition, each
// nil unless its flag was given.
type specLists struct {
	ports  []dicer.PortMapping
	mounts []dicer.Mount
	env    map[string]string
	labels map[string]string
}

// parseListFlags parses the list and map flags that were given.
func parseListFlags(cmd *cobra.Command) (specLists, error) {
	var (
		lists specLists
		err   error
		flags = cmd.Flags()
	)

	if flags.Changed("publish") {
		specs, _ := flags.GetStringArray("publish")
		if lists.ports, err = parseEach(specs, parsePortMapping); err != nil {
			return lists, err
		}
	}
	if flags.Changed("mount") {
		specs, _ := flags.GetStringArray("mount")
		if lists.mounts, err = parseEach(specs, parseMount); err != nil {
			return lists, err
		}
	}
	if flags.Changed("env") || flags.Changed("env-file") {
		files, _ := flags.GetStringArray("env-file")
		specs, _ := flags.GetStringArray("env")
		if lists.env, err = parseEnv(specs, files); err != nil {
			return lists, err
		}
	}
	if flags.Changed("label") {
		specs, _ := flags.GetStringArray("label")
		if lists.labels, err = parseLabels(specs); err != nil {
			return lists, err
		}
	}

	return lists, nil
}

// parseEach parses every value of a repeated flag.
func parseEach[T any](specs []string, parse func(string) (T, error)) ([]T, error) {
	out := make([]T, 0, len(specs))
	for _, s := range specs {
		v, err := parse(s)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// parseMount parses a mount written as docker run --mount takes it: comma
// separated key=value pairs, e.g. type=volume,source=data,target=/data,readonly.
// src and dst or destination may stand for source and target, and ro for
// readonly. The type defaults to volume. A file mount's source is a file on
// this machine, which is read now, so that its contents are sent.
func parseMount(s string) (dicer.Mount, error) {
	m := dicer.Mount{Type: dicer.MountTypeVolume}

	for field := range strings.SplitSeq(s, ",") {
		key, value, hasValue := strings.Cut(field, "=")
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "type":
			t, err := parseChoice("mount type", value, mountTypes)
			if err != nil {
				return dicer.Mount{}, fmt.Errorf("invalid mount %q: %w", s, err)
			}
			m.Type = t
		case "source", "src":
			m.Source = value
		case "target", "dst", "destination":
			m.Target = value
		case "readonly", "ro":
			if !hasValue {
				m.ReadOnly = true
				continue
			}
			ro, err := strconv.ParseBool(value)
			if err != nil {
				return dicer.Mount{}, fmt.Errorf("invalid mount %q: %s=%q is not true or false", s, key, value)
			}
			m.ReadOnly = ro
		default:
			return dicer.Mount{}, fmt.Errorf("invalid mount %q: unknown key %q: want type, source, target or readonly", s, key)
		}
	}

	if m.Target == "" {
		return dicer.Mount{}, fmt.Errorf("invalid mount %q: it needs a target", s)
	}
	if m.Type != dicer.MountTypeFile {
		return m, nil
	}

	if m.Source == "" {
		return dicer.Mount{}, fmt.Errorf("invalid mount %q: a file mount needs the file on this machine as its source", s)
	}
	file, err := dicer.FileMount(m.Source, m.Target)
	if err != nil {
		return dicer.Mount{}, fmt.Errorf("invalid mount %q: %w", s, err)
	}
	file.ReadOnly = m.ReadOnly
	return file, nil
}

// parsePortMapping parses a mapping as a person writes it: "8080:80",
// "127.0.0.1:8080:80", "53:53/udp". The daemon checks it further.
func parsePortMapping(s string) (dicer.PortMapping, error) {
	spec, protoName, hasProto := strings.Cut(s, "/")
	parts := strings.Split(spec, ":")

	var hostIP, hostPort, guestPort string
	switch len(parts) {
	case 2:
		hostPort, guestPort = parts[0], parts[1]
	case 3:
		hostIP, hostPort, guestPort = parts[0], parts[1], parts[2]
	default:
		return dicer.PortMapping{}, fmt.Errorf("invalid port mapping %q: want [hostIP:]hostPort:guestPort[/tcp|udp], e.g. 8080:80", s)
	}

	host, err := parsePort(hostPort)
	if err != nil {
		return dicer.PortMapping{}, fmt.Errorf("invalid port mapping %q: host port: %w", s, err)
	}
	guest, err := parsePort(guestPort)
	if err != nil {
		return dicer.PortMapping{}, fmt.Errorf("invalid port mapping %q: guest port: %w", s, err)
	}

	p := dicer.PortMapping{HostIP: hostIP, HostPort: host, GuestPort: guest}
	if hasProto {
		if p.Protocol, err = parseChoice("protocol", protoName, protocols); err != nil {
			return dicer.PortMapping{}, fmt.Errorf("invalid port mapping %q: %w", s, err)
		}
	}
	return p, nil
}

// parsePort parses a port number.
func parsePort(s string) (int, error) {
	n, err := strconv.ParseUint(s, 10, 16)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("invalid port %q: want a number from 1 to 65535", s)
	}

	return int(n), nil
}

// parseMemoryBytes converts a human-readable memory size to bytes.
func parseMemoryBytes(size string) (int64, error) {
	bytes, err := units.RAMInBytes(size)
	if err != nil {
		return 0, fmt.Errorf("invalid memory size %q: want a size like 512MiB or 2GiB", size)
	}
	return bytes, nil
}

// parseDiskBytes converts a human-readable size to bytes.
func parseDiskBytes(size string) (int64, error) {
	bytes, err := units.RAMInBytes(size)
	if err != nil {
		return 0, fmt.Errorf("invalid disk size %q: want a size like 10GiB", size)
	}

	const minBytes = 1 << 20 // a disk smaller than 1MiB cannot hold a filesystem
	if bytes < minBytes {
		return 0, fmt.Errorf("disk size %q is below the 1MiB minimum", size)
	}

	return bytes, nil
}

// parseBytesPerSecond converts the value of the rate flag named flag, a
// human-readable size with or without a trailing /s, to bytes per second.
func parseBytesPerSecond(flag, rate string) (int64, error) {
	bytes, err := units.RAMInBytes(strings.TrimSuffix(rate, "/s"))
	if err != nil {
		return 0, fmt.Errorf("invalid --%s %q: want bytes per second, such as 50MiB, or 0 for unlimited", flag, rate)
	}
	return bytes, nil
}

// parseEnv builds an environment from --env-file files, then --env flags,
// which win. A flag or line that is only a KEY takes this shell's value of
// it, and is left out if the shell has none -- as with docker run.
func parseEnv(specs, files []string) (map[string]string, error) {
	env := make(map[string]string)

	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("cannot read env file %s: %w", path, osCause(err))
		}

		scanner := bufio.NewScanner(f)
		for line := 1; scanner.Scan(); line++ {
			text := strings.TrimSpace(scanner.Text())
			if text == "" || strings.HasPrefix(text, "#") {
				continue
			}
			if err := setEnv(env, text); err != nil {
				_ = f.Close()
				return nil, fmt.Errorf("%s:%d: %w", path, line, err)
			}
		}
		err = scanner.Err()
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("cannot read env file %s: %w", path, err)
		}
	}

	for _, s := range specs {
		if err := setEnv(env, s); err != nil {
			return nil, err
		}
	}

	return env, nil
}

// osCause strips the operation and path from a file error.
func osCause(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

// setEnv sets one KEY=VALUE, or KEY from this shell, in env. Only the first
// = splits, so a value may hold its own, and commas are the value's too.
func setEnv(env map[string]string, s string) error {
	key, value, ok := strings.Cut(s, "=")
	if key == "" || strings.ContainsAny(key, " \t") {
		return fmt.Errorf("invalid environment variable %q: want KEY=VALUE or KEY", s)
	}

	if !ok {
		value, ok = os.LookupEnv(key)
		if !ok {
			return nil
		}
	}

	env[key] = value
	return nil
}

// parseLabels parses KEY=VALUE labels. A bare KEY is a label with no value.
func parseLabels(specs []string) (map[string]string, error) {
	labels := make(map[string]string, len(specs))
	for _, s := range specs {
		key, value, _ := strings.Cut(s, "=")
		if key == "" {
			return nil, fmt.Errorf("invalid label %q: want KEY=VALUE", s)
		}
		labels[key] = value
	}
	return labels, nil
}

// notNameChars are what cannot be in a name, and are replaced with hyphens
// when one is made from an image's.
var notNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

// nameSuffixChars are what a generated name's suffix is drawn from.
const nameSuffixChars = "abcdefghijklmnopqrstuvwxyz0123456789"

// generateName makes an instance name from an image reference: its last
// path component and a random suffix, "nginx-k3x9" for
// docker.io/library/nginx:1.27.
func generateName(imageRef string) string {
	base := imageRef
	if i := strings.IndexByte(base, '@'); i >= 0 {
		base = base[:i]
	}
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.IndexByte(base, ':'); i >= 0 {
		base = base[:i]
	}

	base = strings.Trim(notNameChars.ReplaceAllString(strings.ToLower(base), "-"), "-")
	const maxBase = 40
	if len(base) > maxBase {
		base = strings.TrimRight(base[:maxBase], "-")
	}
	if base == "" {
		base = "instance"
	}

	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	for i, b := range suffix {
		suffix[i] = nameSuffixChars[int(b)%len(nameSuffixChars)]
	}

	name := base + "-" + string(suffix)
	if naming.Validate(name) != nil {
		return "instance-" + string(suffix)
	}
	return name
}
