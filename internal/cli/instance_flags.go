// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Instance flags: turning command-line flags into an InstanceSpec or an
// InstancePatch, and parsing the individual flag values.

package cli

import (
	"bufio"
	"crypto/rand"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/docker/go-units"
	"github.com/spf13/cobra"

	"github.com/dicer-sh/dicer"
)

// parseRestartPolicy parses a restart policy as --restart and a spec file's
// restart: take it, "on-failure:5". The daemon checks the mode.
func parseRestartPolicy(s string) (*dicer.RestartPolicy, error) {
	mode, retries, hasRetries := strings.Cut(s, ":")
	p := &dicer.RestartPolicy{Mode: dicer.RestartMode(mode)}

	if hasRetries {
		n, err := strconv.Atoi(retries)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid restart policy %q: want on-failure:N, with N a whole number", s)
		}
		p.MaxRetries = n
	}

	return p, nil
}

// addInstanceSpecFlags adds the flags that describe an instance, shared by
// create, run and update. Update changes only what it is given, so it shows
// no defaults: withDefaults is false for it.
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
	flags.String("disk", disk, "Overlay disk size, e.g. 10GiB")
	flags.String("network", "", "Network to attach to")
	flags.String("ip", "", "Static IP address (default: assigned from the subnet)")
	flags.StringArrayP("publish", "p", nil,
		"Publish a guest port on the host, as [hostIP:]hostPort:guestPort[/tcp|udp] (repeatable)")
	flags.StringSliceP("volume", "v", nil, "Volume mount as volume:path[:accessMode] (repeatable)")
	flags.StringArray("host-file", nil,
		"Expose a host file to the guest at /run/secrets/<name>, as name=/host/path (repeatable)")
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
	addHealthFlags(flags)
	flags.String("init-mode", "",
		"How the guest starts the command: auto, exec (as PID 1 of its own PID namespace) or systemd (default auto)")
	_ = cmd.RegisterFlagCompletionFunc("init-mode", fixedCompletions("auto", "exec", "systemd"))
	_ = cmd.RegisterFlagCompletionFunc("restart", fixedCompletions("no", "on-failure", "unless-stopped", "always"))
	_ = cmd.MarkFlagFilename("env-file")
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

// buildCreateRequest assembles a create request from a file, from flags, or
// from a file with flags overriding it.
func buildCreateRequest(cmd *cobra.Command, args []string) (dicer.InstanceSpec, bool, error) {
	var spec dicer.InstanceSpec

	if path, _ := cmd.Flags().GetString("file"); path != "" {
		file, err := readInstanceFile(cmd.InOrStdin(), path)
		if err != nil {
			return spec, false, err
		}
		if spec, err = file.spec(); err != nil {
			return spec, false, err
		}
	}

	if name := positionalArgs(cmd, args); len(name) > 0 {
		spec.Name = name[0]
	}
	if command := commandArgs(cmd, args); len(command) > 0 {
		spec.Cmd = command
	}
	if cmd.Flags().Changed("image") {
		spec.ImageRef, _ = cmd.Flags().GetString("image")
	}

	if err := applySpecFlags(cmd, &spec); err != nil {
		return spec, false, usagef(cmd, "%s", err)
	}
	start, _ := cmd.Flags().GetBool("start")

	if spec.Name == "" {
		return spec, false, usagef(cmd, "%s needs an instance name, as an argument or in the file", cmd.CommandPath())
	}

	return spec, start, nil
}

// buildRunRequest assembles the request 'dicer run IMAGE [COMMAND...]'
// makes: a create that also starts the instance.
func buildRunRequest(cmd *cobra.Command, args []string) (dicer.InstanceSpec, error) {
	spec := dicer.InstanceSpec{
		ImageRef: args[0],
		Cmd:      trimDash(args[1:]),
	}

	spec.Name, _ = cmd.Flags().GetString("name")
	if spec.Name == "" {
		spec.Name = generateName(spec.ImageRef)
	}

	if err := applySpecFlags(cmd, &spec); err != nil {
		return spec, usagef(cmd, "%s", err)
	}

	return spec, nil
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

// applySpecFlags sets the fields of req its flags were given for. Flags
// override whatever a file said, but only when explicitly set -- otherwise
// cobra's defaults would silently clobber the file.
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
	if flags.Changed("hypervisor-type") {
		v, _ := flags.GetString("hypervisor-type")
		spec.HypervisorType = dicer.HypervisorType(v)
	}
	if flags.Changed("init-mode") {
		v, _ := flags.GetString("init-mode")
		spec.InitMode = dicer.InitMode(v)
	}
	if flags.Changed("restart") {
		v, _ := flags.GetString("restart")
		p, err := parseRestartPolicy(v)
		if err != nil {
			return usagef(cmd, "%s", err)
		}
		spec.Restart = *p
	}
	if flags.Changed("rm") {
		spec.RemoveOnExit, _ = flags.GetBool("rm")
	}
	switch hc, err := healthCheckFromFlags(cmd); {
	case err != nil:
		return usagef(cmd, "%s", err)
	case hc != nil:
		spec.HealthCheck = hc
	}

	// Sizes the file leaves out take the flags' defaults.
	if flags.Changed("vcpus") || spec.VCPUs == 0 {
		v, _ := flags.GetInt32("vcpus")
		spec.VCPUs = int(v)
	}
	var err error
	if flags.Changed("memory") || spec.MemoryBytes == 0 {
		if spec.MemoryBytes, err = parseSizeFlag(cmd, "memory", parseMemoryBytes); err != nil {
			return err
		}
	}
	if flags.Changed("disk") || spec.DiskBytes == 0 {
		if spec.DiskBytes, err = parseSizeFlag(cmd, "disk", parseDiskBytes); err != nil {
			return err
		}
	}

	lists, err := parseListFlags(cmd)
	if err != nil {
		return err
	}
	if lists.ports != nil {
		spec.Ports = lists.ports
	}
	if lists.volumes != nil {
		spec.VolumeMounts = lists.volumes
	}
	if lists.files != nil {
		spec.Files = lists.files
	}
	if lists.env != nil {
		spec.Env = lists.env
	}
	if lists.labels != nil {
		spec.Labels = lists.labels
	}

	return nil
}

// parseSizeFlag parses the size flag holds.
func parseSizeFlag(cmd *cobra.Command, flag string, parse func(string) (int64, error)) (int64, error) {
	v, _ := cmd.Flags().GetString(flag)
	return parse(v)
}

// buildUpdateRequest assembles a patch that changes only what its flags were
// given for.
func buildUpdateRequest(cmd *cobra.Command, args []string) (dicer.InstancePatch, error) {
	patch := dicer.InstancePatch{
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

	patch.ImageRef = optionalString("image")
	patch.KernelName = optionalString("kernel")
	patch.KernelArgs = optionalString("kernel-args")
	patch.HypervisorVersion = optionalString("hypervisor-version")
	patch.NetworkName = optionalString("network")
	patch.StaticIP = optionalString("ip")
	patch.Hostname = optionalString("hostname")

	if v := optionalString("hypervisor-type"); v != nil {
		hv := dicer.HypervisorType(*v)
		patch.HypervisorType = &hv
	}
	if v := optionalString("init-mode"); v != nil {
		mode := dicer.InitMode(*v)
		patch.InitMode = &mode
	}
	if flags.Changed("vcpus") {
		v, _ := flags.GetInt32("vcpus")
		vcpus := int(v)
		patch.VCPUs = &vcpus
	}
	if flags.Changed("memory") {
		bytes, err := parseSizeFlag(cmd, "memory", parseMemoryBytes)
		if err != nil {
			return patch, usagef(cmd, "%s", err)
		}
		patch.MemoryBytes = &bytes
	}
	if flags.Changed("disk") {
		bytes, err := parseSizeFlag(cmd, "disk", parseDiskBytes)
		if err != nil {
			return patch, usagef(cmd, "%s", err)
		}
		patch.DiskBytes = &bytes
	}
	if flags.Changed("restart") {
		v, _ := flags.GetString("restart")
		p, err := parseRestartPolicy(v)
		if err != nil {
			return patch, usagef(cmd, "%s", err)
		}
		patch.Restart = p
	}
	if flags.Changed("rm") {
		v, _ := flags.GetBool("rm")
		patch.RemoveOnExit = &v
	}
	healthCheck, err := healthCheckFromFlags(cmd)
	if err != nil {
		return patch, usagef(cmd, "%s", err)
	}
	patch.HealthCheck = healthCheck

	lists, err := parseListFlags(cmd)
	if err != nil {
		return patch, usagef(cmd, "%s", err)
	}
	patch.Ports = lists.ports
	patch.VolumeMounts = lists.volumes
	patch.Files = lists.files
	patch.Env = lists.env
	patch.Labels = lists.labels

	if !hasChanges(patch) {
		return patch, usagef(cmd, "%s needs something to change: a flag, or a command after --", cmd.CommandPath())
	}

	return patch, nil
}

// hasChanges reports whether a patch would change anything.
func hasChanges(p dicer.InstancePatch) bool {
	return p.ImageRef != nil || p.KernelName != nil || p.KernelArgs != nil ||
		p.HypervisorType != nil || p.HypervisorVersion != nil || p.NetworkName != nil ||
		p.StaticIP != nil || p.Hostname != nil || p.VCPUs != nil || p.MemoryBytes != nil ||
		p.DiskBytes != nil || p.Restart != nil || p.HealthCheck != nil || p.InitMode != nil ||
		p.RemoveOnExit != nil ||
		len(p.Cmd) > 0 || len(p.Ports) > 0 || len(p.VolumeMounts) > 0 || len(p.Files) > 0 ||
		len(p.Env) > 0 || len(p.Labels) > 0
}

// specLists are the list and map flags of an instance's definition, each
// nil unless its flag was given.
type specLists struct {
	ports   []dicer.PortMapping
	volumes []dicer.VolumeMount
	files   []dicer.FileMount
	env     map[string]string
	labels  map[string]string
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
		if lists.ports, err = parsePortFlags(specs); err != nil {
			return lists, err
		}
	}
	if flags.Changed("volume") {
		specs, _ := flags.GetStringSlice("volume")
		if lists.volumes, err = parseVolumeFlags(specs); err != nil {
			return lists, err
		}
	}
	if flags.Changed("host-file") {
		specs, _ := flags.GetStringArray("host-file")
		if lists.files, err = parseFileFlags(specs); err != nil {
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

// parseFileFlags parses name=/host/path pairs.
func parseFileFlags(specs []string) ([]dicer.FileMount, error) {
	files := make([]dicer.FileMount, 0, len(specs))

	for _, s := range specs {
		name, hostPath, ok := strings.Cut(s, "=")
		if !ok || name == "" || hostPath == "" {
			return nil, fmt.Errorf("invalid file %q: want name=/host/path", s)
		}

		files = append(files, dicer.FileMount{Name: name, HostPath: hostPath})
	}

	return files, nil
}

// parsePortFlags parses [hostIP:]hostPort:guestPort[/protocol] mappings. The
// daemon validates them further.
func parsePortFlags(specs []string) ([]dicer.PortMapping, error) {
	if len(specs) == 0 {
		return nil, nil
	}

	ports := make([]dicer.PortMapping, 0, len(specs))
	for _, s := range specs {
		p, err := dicer.ParsePortMapping(s)
		if err != nil {
			return nil, err
		}
		ports = append(ports, p)
	}

	return ports, nil
}

// parseVolumeFlags parses volume:path[:accessMode] triples. An omitted access
// mode is left for the daemon to default.
func parseVolumeFlags(specs []string) ([]dicer.VolumeMount, error) {
	mounts := make([]dicer.VolumeMount, 0, len(specs))

	for _, s := range specs {
		parts := strings.Split(s, ":")
		if len(parts) < 2 || len(parts) > 3 {
			return nil, fmt.Errorf("invalid volume %q: want volume:path[:accessMode]", s)
		}

		var mode string
		if len(parts) == 3 {
			mode = parts[2]
		}

		mounts = append(mounts, dicer.VolumeMount{
			VolumeName: parts[0],
			MountPath:  parts[1],
			AccessMode: dicer.VolumeAccessMode(mode),
		})
	}

	return mounts, nil
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
	if dicer.ValidateName(name) != nil {
		return "instance-" + string(suffix)
	}
	return name
}
