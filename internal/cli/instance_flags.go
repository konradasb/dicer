// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Instance flags: turning command-line flags into a create or update
// request, and parsing the individual flag values.

package cli

import (
	"bufio"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/docker/go-units"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/konradasb/dicer/internal/naming"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// parseRestartPolicy parses a restart policy as --restart takes it,
// "on-failure:5".
func parseRestartPolicy(s string) (*dicerdv1.RestartPolicy, error) {
	modeName, retries, hasRetries := strings.Cut(s, ":")
	mode, err := parseEnum[dicerdv1.RestartMode]("restart policy", modeName)
	if err != nil {
		return nil, err
	}
	p := &dicerdv1.RestartPolicy{Mode: mode}

	if hasRetries {
		n, err := strconv.ParseInt(retries, 10, 32)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid restart policy %q: want on-failure:N, with N a whole number", s)
		}
		p.MaxRetries = int32(n)
	}

	return p, nil
}

// addInstanceSpecFlags adds the flags that describe an instance, shared by
// create, run and update (which passes withDefaults false).
func addInstanceSpecFlags(cmd *cobra.Command, withDefaults bool) {
	vcpus, memory, disk := int32(1), "512MiB", "10GiB"
	if !withDefaults {
		vcpus, memory, disk = 0, "", ""
	}

	flags := cmd.Flags()
	flags.String("kernel", "", "Kernel to boot with")
	flags.String("kernel-args", "", "Kernel command line arguments")
	flags.String("hypervisor-type", "", "Hypervisor: cloud-hypervisor or firecracker (default: cloud-hypervisor)")
	flags.String("hypervisor-version", "", "Hypervisor version (default: the newest available)")
	flags.Int32("vcpus", vcpus, "Number of virtual CPUs")
	flags.StringP("memory", "m", memory, "Memory, e.g. 512MiB or 2GiB")
	flags.Int32("max-vcpus", 0, "Most vCPUs 'dicer resize' can give the running instance, on Cloud Hypervisor (0: none)")
	flags.String("max-memory", "", "Most memory 'dicer resize' can give the running instance, e.g. 4GiB (0: none)")
	flags.String("disk", disk, "Overlay disk size, e.g. 10GiB")
	flags.String("disk-rate", "", "Bytes per second each disk can be read and written at, e.g. 50MiB (0: unlimited)")
	flags.Int64("disk-iops", 0, "Operations per second each disk can be read and written at (0: unlimited)")
	flags.String("upload-rate", "", "Bytes per second the guest can send, e.g. 10MiB (0: unlimited)")
	flags.String("download-rate", "", "Bytes per second the guest can receive, e.g. 10MiB (0: unlimited)")
	flags.Duration("standby-after", 0, "Put the instance on standby once it has been idle this long, e.g. 15m (0: never)")
	flags.String("network", "", "Network to attach to")
	flags.String("ip", "", "Static IP address (default: assigned from the subnet)")
	flags.StringArrayP("publish", "p", nil,
		"Publish a guest port on the host, as [hostIP:]hostPort:guestPort[/tcp|udp] (repeatable)")
	flags.StringArray("mount", nil,
		"Mount a volume, host file or tmpfs, as [type=volume|file|tmpfs,][source=...,]target=/path[,readonly] (repeatable)")
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
func pullPolicyFlag(cmd *cobra.Command) (dicerdv1.PullPolicy, error) {
	v, _ := cmd.Flags().GetString("pull")
	policy, err := parseEnum[dicerdv1.PullPolicy]("--pull", v)
	if err != nil {
		return 0, usagef(cmd, "%s", err)
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

// buildCreateRequest assembles a create request from the command's
// arguments and flags.
func buildCreateRequest(cmd *cobra.Command, args []string) (*dicerdv1.CreateInstanceRequest, error) {
	req := &dicerdv1.CreateInstanceRequest{}

	if name := positionalArgs(cmd, args); len(name) > 0 {
		req.Name = name[0]
	}
	if command := commandArgs(cmd, args); len(command) > 0 {
		req.Cmd = command
	}
	if cmd.Flags().Changed("image") {
		req.ImageRef, _ = cmd.Flags().GetString("image")
	}

	if err := applySpecFlags(cmd, req); err != nil {
		return nil, usagef(cmd, "%s", err)
	}
	req.Start, _ = cmd.Flags().GetBool("start")

	var err error
	if req.PullPolicy, err = pullPolicyFlag(cmd); err != nil {
		return nil, err
	}

	if req.GetName() == "" {
		return nil, usagef(cmd, "%s needs an instance name", cmd.CommandPath())
	}

	return req, nil
}

// buildRunRequest assembles the request 'dicer run IMAGE [COMMAND...]'
// makes: a create that also starts the instance.
func buildRunRequest(cmd *cobra.Command, args []string) (*dicerdv1.CreateInstanceRequest, error) {
	req := &dicerdv1.CreateInstanceRequest{
		ImageRef: args[0],
		Cmd:      trimDash(args[1:]),
		Start:    true,
	}

	req.Name, _ = cmd.Flags().GetString("name")
	if req.GetName() == "" {
		req.Name = generateName(req.GetImageRef())
	}

	if err := applySpecFlags(cmd, req); err != nil {
		return nil, usagef(cmd, "%s", err)
	}

	var err error
	if req.PullPolicy, err = pullPolicyFlag(cmd); err != nil {
		return nil, err
	}

	return req, nil
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

// applySpecFlags sets the fields of req for the flags given, and the sizes,
// whose defaults are the command line's.
func applySpecFlags(cmd *cobra.Command, req *dicerdv1.CreateInstanceRequest) error {
	flags := cmd.Flags()
	setString := func(flag string, dst *string) {
		if flags.Changed(flag) {
			*dst, _ = flags.GetString(flag)
		}
	}
	setString("kernel", &req.KernelName)
	setString("kernel-args", &req.KernelArgs)
	setString("hypervisor-version", &req.HypervisorVersion)
	setString("network", &req.NetworkName)
	setString("ip", &req.StaticIp)
	setString("hostname", &req.Hostname)

	var err error
	if flags.Changed("hypervisor-type") {
		v, _ := flags.GetString("hypervisor-type")
		if req.HypervisorType, err = parseEnum[dicerdv1.HypervisorType]("hypervisor", v); err != nil {
			return err
		}
	}
	if flags.Changed("init-mode") {
		v, _ := flags.GetString("init-mode")
		if req.InitMode, err = parseEnum[dicerdv1.InitMode]("init mode", v); err != nil {
			return err
		}
	}
	if flags.Changed("restart") {
		v, _ := flags.GetString("restart")
		if req.RestartPolicy, err = parseRestartPolicy(v); err != nil {
			return err
		}
	}
	if flags.Changed("rm") {
		req.RemoveOnExit, _ = flags.GetBool("rm")
	}
	switch hc, err := healthCheckFromFlags(cmd); {
	case err != nil:
		return err
	case hc != nil:
		req.HealthCheck = hc
	}

	// Sizes are always given: the flags' defaults are the command line's.
	req.Vcpus, _ = flags.GetInt32("vcpus")
	memory, _ := flags.GetString("memory")
	if req.MemoryBytes, err = parseMemoryBytes(memory); err != nil {
		return err
	}
	disk, _ := flags.GetString("disk")
	if req.DiskBytes, err = parseDiskBytes(disk); err != nil {
		return err
	}
	req.MaxVcpus, _ = flags.GetInt32("max-vcpus")
	if flags.Changed("max-memory") {
		v, _ := flags.GetString("max-memory")
		if req.MaxMemoryBytes, err = parseMemoryBytes(v); err != nil {
			return err
		}
	}
	for flag, dst := range map[string]*int64{
		"disk-rate":     &req.DiskBytesPerSecond,
		"upload-rate":   &req.UploadBytesPerSecond,
		"download-rate": &req.DownloadBytesPerSecond,
	} {
		if flags.Changed(flag) {
			v, _ := flags.GetString(flag)
			if *dst, err = parseBytesPerSecond(flag, v); err != nil {
				return err
			}
		}
	}
	req.DiskIops, _ = flags.GetInt64("disk-iops")
	if d, _ := flags.GetDuration("standby-after"); d != 0 {
		req.StandbyAfter = durationpb.New(d)
	}

	lists, err := parseListFlags(cmd)
	if err != nil {
		return err
	}
	if lists.ports != nil {
		req.Ports = lists.ports
	}
	if lists.mounts != nil {
		req.Mounts = lists.mounts
	}
	if lists.env != nil {
		req.Env = lists.env
	}
	if lists.labels != nil {
		req.Labels = lists.labels
	}

	return nil
}

// buildUpdateRequest assembles an update that changes only what its flags
// were given for.
func buildUpdateRequest(cmd *cobra.Command, args []string) (*dicerdv1.UpdateInstanceRequest, error) {
	req := &dicerdv1.UpdateInstanceRequest{
		Name: positionalArgs(cmd, args)[0],
		Cmd:  commandArgs(cmd, args),
	}

	flags := cmd.Flags()
	optionalString := func(flag string) *string {
		if !flags.Changed(flag) {
			return nil
		}
		v, _ := flags.GetString(flag)

		return &v
	}

	req.ImageRef = optionalString("image")
	req.KernelName = optionalString("kernel")
	req.KernelArgs = optionalString("kernel-args")
	req.HypervisorVersion = optionalString("hypervisor-version")
	req.NetworkName = optionalString("network")
	req.StaticIp = optionalString("ip")
	req.Hostname = optionalString("hostname")

	var err error
	if v := optionalString("hypervisor-type"); v != nil {
		if req.HypervisorType, err = parseEnum[dicerdv1.HypervisorType]("hypervisor", *v); err != nil {
			return nil, usagef(cmd, "%s", err)
		}
	}
	if v := optionalString("init-mode"); v != nil {
		if req.InitMode, err = parseEnum[dicerdv1.InitMode]("init mode", *v); err != nil {
			return nil, usagef(cmd, "%s", err)
		}
	}
	if flags.Changed("vcpus") {
		v, _ := flags.GetInt32("vcpus")
		req.Vcpus = &v
	}
	if v := optionalString("memory"); v != nil {
		bytes, err := parseMemoryBytes(*v)
		if err != nil {
			return nil, usagef(cmd, "%s", err)
		}
		req.MemoryBytes = &bytes
	}
	if flags.Changed("max-vcpus") {
		v, _ := flags.GetInt32("max-vcpus")
		req.MaxVcpus = &v
	}
	if v := optionalString("max-memory"); v != nil {
		bytes, err := parseMemoryBytes(*v)
		if err != nil {
			return nil, usagef(cmd, "%s", err)
		}
		req.MaxMemoryBytes = &bytes
	}
	if v := optionalString("disk"); v != nil {
		bytes, err := parseDiskBytes(*v)
		if err != nil {
			return nil, usagef(cmd, "%s", err)
		}
		req.DiskBytes = &bytes
	}
	for flag, dst := range map[string]**int64{
		"disk-rate":     &req.DiskBytesPerSecond,
		"upload-rate":   &req.UploadBytesPerSecond,
		"download-rate": &req.DownloadBytesPerSecond,
	} {
		if v := optionalString(flag); v != nil {
			bytes, err := parseBytesPerSecond(flag, *v)
			if err != nil {
				return nil, usagef(cmd, "%s", err)
			}
			*dst = &bytes
		}
	}
	if flags.Changed("disk-iops") {
		v, _ := flags.GetInt64("disk-iops")
		req.DiskIops = &v
	}
	if flags.Changed("standby-after") {
		d, _ := flags.GetDuration("standby-after")
		req.StandbyAfter = durationpb.New(d)
	}
	if flags.Changed("restart") {
		v, _ := flags.GetString("restart")
		if req.RestartPolicy, err = parseRestartPolicy(v); err != nil {
			return nil, usagef(cmd, "%s", err)
		}
	}
	if flags.Changed("rm") {
		v, _ := flags.GetBool("rm")
		req.RemoveOnExit = &v
	}
	if req.HealthCheck, err = healthCheckFromFlags(cmd); err != nil {
		return nil, usagef(cmd, "%s", err)
	}

	lists, err := parseListFlags(cmd)
	if err != nil {
		return nil, usagef(cmd, "%s", err)
	}
	req.Ports = lists.ports
	req.Mounts = lists.mounts
	req.Env = lists.env
	req.Labels = lists.labels

	if proto.Equal(req, &dicerdv1.UpdateInstanceRequest{Name: req.GetName()}) {
		return nil, usagef(cmd, "%s needs something to change: a flag, or a command after --", cmd.CommandPath())
	}

	return req, nil
}

// specLists are the list and map flags of an instance's definition, each
// nil unless its flag was given.
type specLists struct {
	ports  []*dicerdv1.PortMapping
	mounts []*dicerdv1.Mount
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
// readonly. The type defaults to volume.
func parseMount(s string) (*dicerdv1.Mount, error) {
	m := &dicerdv1.Mount{Type: dicerdv1.MountType_MOUNT_TYPE_VOLUME}

	for field := range strings.SplitSeq(s, ",") {
		key, value, hasValue := strings.Cut(field, "=")
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "type":
			t, err := parseEnum[dicerdv1.MountType]("mount type", value)
			if err != nil {
				return nil, fmt.Errorf("invalid mount %q: %w", s, err)
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
				return nil, fmt.Errorf("invalid mount %q: %s=%q is not true or false", s, key, value)
			}
			m.ReadOnly = ro
		default:
			return nil, fmt.Errorf("invalid mount %q: unknown key %q: want type, source, target or readonly", s, key)
		}
	}

	if m.GetTarget() == "" {
		return nil, fmt.Errorf("invalid mount %q: it needs a target", s)
	}
	return m, nil
}

// parsePortMapping parses a mapping as a person writes it: "8080:80",
// "127.0.0.1:8080:80", "53:53/udp". The daemon checks it further.
func parsePortMapping(s string) (*dicerdv1.PortMapping, error) {
	spec, protoName, hasProto := strings.Cut(s, "/")
	parts := strings.Split(spec, ":")

	var hostIP, hostPort, guestPort string
	switch len(parts) {
	case 2:
		hostPort, guestPort = parts[0], parts[1]
	case 3:
		hostIP, hostPort, guestPort = parts[0], parts[1], parts[2]
	default:
		return nil, fmt.Errorf("invalid port mapping %q: want [hostIP:]hostPort:guestPort[/tcp|udp], e.g. 8080:80", s)
	}

	host, err := parsePort(hostPort)
	if err != nil {
		return nil, fmt.Errorf("invalid port mapping %q: host port: %w", s, err)
	}
	guest, err := parsePort(guestPort)
	if err != nil {
		return nil, fmt.Errorf("invalid port mapping %q: guest port: %w", s, err)
	}

	p := &dicerdv1.PortMapping{HostIp: hostIP, HostPort: host, GuestPort: guest}
	if hasProto {
		if p.Protocol, err = parseEnum[dicerdv1.Protocol]("protocol", protoName); err != nil {
			return nil, fmt.Errorf("invalid port mapping %q: %w", s, err)
		}
	}
	return p, nil
}

// parsePort parses a port number.
func parsePort(s string) (uint32, error) {
	n, err := strconv.ParseUint(s, 10, 16)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("invalid port %q: want a number from 1 to 65535", s)
	}

	return uint32(n), nil
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
