// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Turning a file's services, networks and volumes into the daemon's
// requests.

package compose

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/konradasb/dicer/internal/naming"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// An instance's size unless its service gives one: the same as dicer run's.
const (
	defaultVCPUs       = 1
	defaultMemoryBytes = 512 << 20
	defaultDiskBytes   = 10 << 30

	// defaultVolumeBytes is a volume's size unless the file gives one.
	defaultVolumeBytes = 10 << 30
)

func (b *builder) network(key string, raw *rawNetwork) (*Network, error) {
	if raw == nil {
		raw = &rawNetwork{}
	}

	name, err := b.resourceName(key, raw.Name, raw.External)
	if err != nil {
		return nil, err
	}
	n := &Network{Key: key, Name: name, External: raw.External}

	subnet, gateway := raw.Subnet, raw.Gateway
	if raw.IPAM != nil {
		if len(raw.IPAM.Config) > 1 {
			return nil, errors.New("ipam can give one subnet, not several")
		}
		if len(raw.IPAM.Config) == 1 {
			if subnet != "" {
				return nil, errors.New("give subnet or ipam, not both")
			}
			subnet, gateway = raw.IPAM.Config[0].Subnet, raw.IPAM.Config[0].Gateway
		}
	}

	if raw.External {
		if subnet != "" || gateway != "" || raw.MTU != 0 || len(raw.Nameservers) > 0 || raw.Isolated {
			return nil, errors.New("an external network is used as it is: give it no settings but name")
		}
		return n, nil
	}
	if subnet == "" {
		return nil, errors.New("it needs a subnet, such as subnet: 172.30.0.0/24")
	}

	n.Request = &dicerdv1.CreateNetworkRequest{
		Name:        name,
		Subnet:      subnet,
		Gateway:     gateway,
		Mtu:         raw.MTU,
		Nameservers: raw.Nameservers,
		Isolated:    raw.Isolated,
	}
	return n, nil
}

func (b *builder) volume(key string, raw *rawVolume) (*Volume, error) {
	if raw == nil {
		raw = &rawVolume{}
	}

	name, err := b.resourceName(key, raw.Name, raw.External)
	if err != nil {
		return nil, err
	}
	v := &Volume{Key: key, Name: name, External: raw.External}

	if raw.External {
		if raw.Size != nil {
			return nil, errors.New("an external volume is used as it is: give it no size")
		}
		return v, nil
	}

	size := int64(defaultVolumeBytes)
	if raw.Size != nil {
		size = int64(*raw.Size)
	}
	if size <= 0 {
		return nil, errors.New("its size must be more than 0")
	}
	v.Request = &dicerdv1.CreateVolumeRequest{Name: name, SizeBytes: size}
	return v, nil
}

func (b *builder) service(key string, raw *rawService) (*Service, error) {
	if raw.Image == "" {
		return nil, errors.New("it needs an image")
	}

	name := raw.ContainerName
	if name == "" {
		name = b.p.Name + "-" + key
	}
	if err := naming.Validate(name); err != nil {
		if raw.ContainerName != "" {
			return nil, fmt.Errorf("container_name %q: use letters, digits and hyphens", name)
		}
		return nil, fmt.Errorf("its instance would be named %q, which cannot be a name: "+
			"use letters, digits and hyphens in the service's name, or set container_name", name)
	}

	req := &dicerdv1.CreateInstanceRequest{
		Name:              name,
		ImageRef:          raw.Image,
		Hostname:          raw.Hostname,
		KernelName:        raw.Kernel,
		KernelArgs:        raw.KernelArgs,
		HypervisorVersion: raw.HypervisorVer,
		Vcpus:             defaultVCPUs,
		MemoryBytes:       defaultMemoryBytes,
		DiskBytes:         defaultDiskBytes,
	}

	if raw.Entrypoint != nil || raw.Command != nil {
		var cmd []string
		if raw.Entrypoint != nil {
			cmd = append(cmd, *raw.Entrypoint...)
		}
		if raw.Command != nil {
			cmd = append(cmd, *raw.Command...)
		}
		req.Cmd = cmd
	}

	steps := []func(*rawService, *dicerdv1.CreateInstanceRequest) error{
		b.sizes, b.hypervisor, b.environment, b.labels, b.ports, b.mounts, b.networks, b.restart, b.healthcheck,
	}
	for _, step := range steps {
		if err := step(raw, req); err != nil {
			return nil, err
		}
	}

	req.Labels[LabelProject] = b.p.Name
	req.Labels[LabelService] = key
	req.Labels[LabelConfigHash] = ConfigHash(req)

	s := &Service{Name: key, Instance: req}
	for _, dep := range raw.DependsOn.names {
		condition, err := parseCondition(raw.DependsOn.conditions[dep])
		if err != nil {
			return nil, fmt.Errorf("depends_on %s: %w", dep, err)
		}
		s.DependsOn = append(s.DependsOn, Dependency{Service: dep, Condition: condition})
	}
	return s, nil
}

func parseCondition(s string) (Condition, error) {
	switch c := Condition(s); c {
	case "":
		return ConditionStarted, nil
	case ConditionStarted, ConditionHealthy, ConditionCompletedSuccessfully:
		return c, nil
	default:
		return "", fmt.Errorf("invalid condition %q: want %s, %s or %s",
			s, ConditionStarted, ConditionHealthy, ConditionCompletedSuccessfully)
	}
}

func (b *builder) sizes(raw *rawService, req *dicerdv1.CreateInstanceRequest) error {
	switch {
	case raw.VCPUs != nil && raw.CPUs != nil:
		return errors.New("give vcpus or cpus, not both")
	case raw.VCPUs != nil:
		req.Vcpus = *raw.VCPUs
	case raw.CPUs != nil:
		cpus := *raw.CPUs
		if cpus != float64(int32(cpus)) {
			return fmt.Errorf("cpus: %v is not a whole number: a guest has whole vCPUs", cpus)
		}
		req.Vcpus = int32(cpus)
	}
	if req.GetVcpus() < 1 {
		return errors.New("it needs at least 1 vCPU")
	}

	switch {
	case raw.Memory != nil && raw.MemLimit != nil:
		return errors.New("give memory or mem_limit, not both")
	case raw.Memory != nil:
		req.MemoryBytes = int64(*raw.Memory)
	case raw.MemLimit != nil:
		req.MemoryBytes = int64(*raw.MemLimit)
	}
	if raw.Disk != nil {
		req.DiskBytes = int64(*raw.Disk)
	}
	return nil
}

func (b *builder) hypervisor(raw *rawService, req *dicerdv1.CreateInstanceRequest) error {
	switch raw.Hypervisor {
	case "":
	case "cloud-hypervisor":
		req.HypervisorType = dicerdv1.HypervisorType_HYPERVISOR_TYPE_CLOUD_HYPERVISOR
	case "firecracker":
		req.HypervisorType = dicerdv1.HypervisorType_HYPERVISOR_TYPE_FIRECRACKER
	default:
		return fmt.Errorf("invalid hypervisor %q: want cloud-hypervisor or firecracker", raw.Hypervisor)
	}

	switch raw.InitMode {
	case "":
	case "auto":
		req.InitMode = dicerdv1.InitMode_INIT_MODE_AUTO
	case "exec":
		req.InitMode = dicerdv1.InitMode_INIT_MODE_EXEC
	case "systemd":
		req.InitMode = dicerdv1.InitMode_INIT_MODE_SYSTEMD
	default:
		return fmt.Errorf("invalid init_mode %q: want auto, exec or systemd", raw.InitMode)
	}
	return nil
}

// environment is env_file's variables, in order, then environment's, which
// win. A variable given no value takes the environment's, and is left out
// if it has none, as with Docker Compose.
func (b *builder) environment(raw *rawService, req *dicerdv1.CreateInstanceRequest) error {
	env := make(map[string]string)
	set := func(vars map[string]*string) {
		for k, v := range vars {
			if v != nil {
				env[k] = *v
			} else if value, ok := b.lookup(k); ok {
				env[k] = value
			}
		}
	}

	for _, path := range raw.EnvFile {
		vars, err := b.readEnvFile(path)
		if err != nil {
			return err
		}
		set(vars)
	}
	set(raw.Environment)

	if len(env) > 0 {
		req.Env = env
	}
	return nil
}

func (b *builder) labels(raw *rawService, req *dicerdv1.CreateInstanceRequest) error {
	req.Labels = make(map[string]string, len(raw.Labels)+3)
	for k, v := range raw.Labels {
		if strings.HasPrefix(k, LabelPrefix) {
			return fmt.Errorf("label %s: labels starting %s are reserved for dicer compose", k, LabelPrefix)
		}
		if v != nil {
			req.Labels[k] = *v
		} else {
			req.Labels[k] = ""
		}
	}
	return nil
}

func (b *builder) ports(raw *rawService, req *dicerdv1.CreateInstanceRequest) error {
	for _, p := range raw.Ports {
		var (
			m   *dicerdv1.PortMapping
			err error
		)
		if p.Short != "" {
			m, err = parseShortPort(p.Short)
		} else {
			m, err = longPort(p)
		}
		if err != nil {
			return fmt.Errorf("line %d: %w", p.line, err)
		}
		req.Ports = append(req.Ports, m)
	}
	return nil
}

// parseShortPort parses "[HOST_IP:]HOST_PORT:GUEST_PORT[/PROTOCOL]".
func parseShortPort(s string) (*dicerdv1.PortMapping, error) {
	spec, protocol, _ := strings.Cut(s, "/")

	var hostIP string
	if strings.HasPrefix(spec, "[") {
		// An IPv6 address, in brackets.
		end := strings.Index(spec, "]:")
		if end < 0 {
			return nil, fmt.Errorf("invalid port %q", s)
		}
		hostIP, spec = spec[1:end], spec[end+2:]
	}

	parts := strings.Split(spec, ":")
	var hostPort, guestPort string
	switch {
	case len(parts) == 1:
		return nil, fmt.Errorf("port %q: publish it on a port of the host, as HOST_PORT:%s", s, parts[0])
	case len(parts) == 2:
		hostPort, guestPort = parts[0], parts[1]
	case len(parts) == 3 && hostIP == "":
		hostIP, hostPort, guestPort = parts[0], parts[1], parts[2]
	default:
		return nil, fmt.Errorf("invalid port %q: want [HOST_IP:]HOST_PORT:GUEST_PORT[/tcp|udp]", s)
	}

	m := &dicerdv1.PortMapping{HostIp: hostIP}
	var err error
	if m.HostPort, err = parsePortNumber(hostPort); err != nil {
		return nil, fmt.Errorf("port %q: host port: %w", s, err)
	}
	if m.GuestPort, err = parsePortNumber(guestPort); err != nil {
		return nil, fmt.Errorf("port %q: guest port: %w", s, err)
	}
	if m.Protocol, err = parseProtocol(protocol); err != nil {
		return nil, fmt.Errorf("port %q: %w", s, err)
	}
	return m, nil
}

func longPort(p rawPort) (*dicerdv1.PortMapping, error) {
	if p.Target == 0 || p.Target > 65535 {
		return nil, errors.New("a port needs a target from 1 to 65535")
	}
	if p.Published == "" {
		return nil, fmt.Errorf("port %d: publish it on a port of the host with published", p.Target)
	}

	m := &dicerdv1.PortMapping{HostIp: p.HostIP, GuestPort: p.Target}
	var err error
	if m.HostPort, err = parsePortNumber(p.Published); err != nil {
		return nil, fmt.Errorf("port %d: published: %w", p.Target, err)
	}
	if m.Protocol, err = parseProtocol(p.Protocol); err != nil {
		return nil, fmt.Errorf("port %d: %w", p.Target, err)
	}
	return m, nil
}

func parsePortNumber(s string) (uint32, error) {
	if strings.Contains(s, "-") {
		return 0, fmt.Errorf("%q is a range: publish each port on its own", s)
	}
	n, err := strconv.ParseUint(s, 10, 16)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("invalid port %q: want a number from 1 to 65535", s)
	}
	return uint32(n), nil
}

func parseProtocol(s string) (dicerdv1.Protocol, error) {
	switch s {
	case "":
		return dicerdv1.Protocol_PROTOCOL_UNSPECIFIED, nil
	case "tcp":
		return dicerdv1.Protocol_PROTOCOL_TCP, nil
	case "udp":
		return dicerdv1.Protocol_PROTOCOL_UDP, nil
	default:
		return 0, fmt.Errorf("invalid protocol %q: want tcp or udp", s)
	}
}

// mounts are the service's volumes and tmpfs. A named volume must be one of
// the file's, and a host path is a file, copied into the guest.
func (b *builder) mounts(raw *rawService, req *dicerdv1.CreateInstanceRequest) error {
	for _, m := range raw.Volumes {
		mount, err := b.mount(m)
		if err != nil {
			return fmt.Errorf("line %d: %w", m.line, err)
		}
		req.Mounts = append(req.Mounts, mount)
	}

	for _, t := range raw.Tmpfs {
		target, options, _ := strings.Cut(t, ":")
		if options != "" {
			return fmt.Errorf("tmpfs %s: a tmpfs takes no options", t)
		}
		req.Mounts = append(req.Mounts, &dicerdv1.Mount{Type: dicerdv1.MountType_MOUNT_TYPE_TMPFS, Target: target})
	}
	return nil
}

func (b *builder) mount(m rawMount) (*dicerdv1.Mount, error) {
	kind, source, target, readOnly := m.Type, m.Source, m.Target, m.ReadOnly

	if m.Short != "" {
		parts := strings.Split(m.Short, ":")
		switch len(parts) {
		case 1:
			return nil, fmt.Errorf("volume %q: anonymous volumes are not supported: "+
				"name one, as NAME:%s, and declare it under volumes", m.Short, m.Short)
		case 2, 3:
			source, target = parts[0], parts[1]
		default:
			return nil, fmt.Errorf("invalid volume %q: want SOURCE:TARGET[:ro]", m.Short)
		}
		if len(parts) == 3 {
			switch parts[2] {
			case "ro":
				readOnly = true
			case "rw":
			default:
				return nil, fmt.Errorf("volume %q: invalid mode %q: want ro or rw", m.Short, parts[2])
			}
		}

		kind = "volume"
		if isHostPath(source) {
			kind = "bind"
		}
	}

	if !strings.HasPrefix(target, "/") {
		return nil, fmt.Errorf("volume target %q: want an absolute path in the guest", target)
	}

	switch kind {
	case "volume":
		v, ok := b.p.Volumes[source]
		if !ok {
			return nil, fmt.Errorf("volume %s is not declared under volumes", source)
		}
		return &dicerdv1.Mount{
			Type: dicerdv1.MountType_MOUNT_TYPE_VOLUME, Source: v.Name, Target: target, ReadOnly: readOnly,
		}, nil
	case "bind":
		path, err := b.resolvePath(source)
		if err != nil {
			return nil, err
		}
		return &dicerdv1.Mount{
			Type: dicerdv1.MountType_MOUNT_TYPE_FILE, Source: path, Target: target, ReadOnly: readOnly,
		}, nil
	case "tmpfs":
		if source != "" {
			return nil, fmt.Errorf("tmpfs %s: a tmpfs has no source", target)
		}
		return &dicerdv1.Mount{Type: dicerdv1.MountType_MOUNT_TYPE_TMPFS, Target: target}, nil
	default:
		return nil, fmt.Errorf("invalid volume type %q: want volume, bind or tmpfs", kind)
	}
}

// isHostPath reports whether a volume's source is a path on the host rather
// than a volume's name.
func isHostPath(source string) bool {
	return strings.HasPrefix(source, "/") || strings.HasPrefix(source, ".") || strings.HasPrefix(source, "~")
}

// networks sets the one network a service joins: the one it names, the
// file's network called default if it names none, or else the daemon's
// default.
func (b *builder) networks(raw *rawService, req *dicerdv1.CreateInstanceRequest) error {
	names := raw.Networks.names
	switch len(names) {
	case 0:
		if n, ok := b.p.Networks["default"]; ok {
			req.NetworkName = n.Name
		}
		return nil
	case 1:
	default:
		return fmt.Errorf("line %d: an instance joins one network, not %d", raw.Networks.line, len(names))
	}

	key := names[0]
	n, ok := b.p.Networks[key]
	if !ok {
		return fmt.Errorf("network %s is not declared under networks", key)
	}
	req.NetworkName = n.Name
	req.StaticIp = raw.Networks.settings[key].IPv4Address
	return nil
}

func (b *builder) restart(raw *rawService, req *dicerdv1.CreateInstanceRequest) error {
	if raw.Restart == "" {
		return nil
	}

	mode, retries, hasRetries := strings.Cut(raw.Restart, ":")
	p := &dicerdv1.RestartPolicy{}
	switch mode {
	case "no":
		p.Mode = dicerdv1.RestartMode_RESTART_MODE_NO
	case "always":
		p.Mode = dicerdv1.RestartMode_RESTART_MODE_ALWAYS
	case "unless-stopped":
		p.Mode = dicerdv1.RestartMode_RESTART_MODE_UNLESS_STOPPED
	case "on-failure":
		p.Mode = dicerdv1.RestartMode_RESTART_MODE_ON_FAILURE
	default:
		return fmt.Errorf("invalid restart %q: want no, always, unless-stopped or on-failure[:N]", raw.Restart)
	}

	if hasRetries {
		n, err := strconv.ParseInt(retries, 10, 32)
		if p.GetMode() != dicerdv1.RestartMode_RESTART_MODE_ON_FAILURE || err != nil || n < 0 {
			return fmt.Errorf("invalid restart %q: only on-failure takes a count, as on-failure:5", raw.Restart)
		}
		p.MaxRetries = int32(n)
	}
	req.RestartPolicy = p
	return nil
}

func (b *builder) healthcheck(raw *rawService, req *dicerdv1.CreateInstanceRequest) error {
	h := raw.Healthcheck
	if h == nil {
		return nil
	}

	var test []string
	if h.Test != nil {
		test = h.Test.args
	}
	testIsNone := len(test) == 1 && test[0] == "NONE"
	disabled := h.Disable || testIsNone

	probes := 0
	for _, given := range []bool{len(test) > 0 && !testIsNone, h.HTTP != "", h.TCP != 0} {
		if given {
			probes++
		}
	}

	if disabled {
		if probes > 0 || h.Interval != 0 || h.Timeout != 0 || h.StartPeriod != 0 || h.Retries != 0 {
			return errors.New("healthcheck: a disabled check takes no other settings")
		}
		req.HealthCheck = &dicerdv1.HealthCheck{Disabled: true}
		return nil
	}
	if probes != 1 {
		return errors.New("healthcheck: give exactly one of test, http and tcp")
	}

	c := &dicerdv1.HealthCheck{
		Interval:    duration(h.Interval),
		Timeout:     duration(h.Timeout),
		StartPeriod: duration(h.StartPeriod),
		Retries:     h.Retries,
	}

	switch {
	case len(test) > 0:
		command, err := healthCommand(test)
		if err != nil {
			return fmt.Errorf("line %d: healthcheck: %w", h.Test.line, err)
		}
		c.Probe = &dicerdv1.HealthCheck_Exec{Exec: &dicerdv1.HealthCheckExec{Command: command}}
	case h.HTTP != "":
		portText, path, _ := strings.Cut(h.HTTP, "/")
		port, err := parsePortNumber(portText)
		if err != nil {
			return fmt.Errorf("healthcheck http %q: want PORT[/path]", h.HTTP)
		}
		if path != "" {
			path = "/" + path
		}
		c.Probe = &dicerdv1.HealthCheck_Http{Http: &dicerdv1.HealthCheckHTTP{Port: port, Path: path}}
	default:
		c.Probe = &dicerdv1.HealthCheck_Tcp{Tcp: &dicerdv1.HealthCheckTCP{Port: h.TCP}}
	}

	req.HealthCheck = c
	return nil
}

// healthCommand turns Docker's test into the command the guest runs.
func healthCommand(test []string) ([]string, error) {
	switch test[0] {
	case "CMD":
		if len(test) < 2 {
			return nil, errors.New("CMD needs a command after it")
		}
		return test[1:], nil
	case "CMD-SHELL":
		if len(test) != 2 {
			return nil, errors.New("CMD-SHELL takes one command line after it")
		}
		return []string{"/bin/sh", "-c", test[1]}, nil
	default:
		return nil, fmt.Errorf("test must start with CMD, CMD-SHELL or NONE, not %q", test[0])
	}
}

func duration(d time.Duration) *durationpb.Duration {
	if d == 0 {
		return nil
	}
	return durationpb.New(d)
}
