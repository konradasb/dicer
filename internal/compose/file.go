// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// The compose file as it is written: what YAML decodes into before it is
// checked and turned into a Project. Where Docker's format lets a value be
// written in more than one way -- a list or a map, a string or a list -- the
// types here take each way and keep what it says.

package compose

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/docker/go-units"
	"gopkg.in/yaml.v3"
)

// rawFile is a whole compose file. Every field's doc comment is its entry in
// the compose file reference, which make docs-gen generates from them: write
// them for someone writing a compose file.
type rawFile struct {
	// Name is the project's name. `-p` or `$DICER_COMPOSE_PROJECT_NAME`
	// overrides it. Unset is the file's directory's name, in lower case.
	Name string `yaml:"name"`

	// Version is accepted and ignored: it is Docker Compose's, and obsolete.
	Version string `yaml:"version"`

	// Services are the project's services, by name. Each is one instance.
	// Required.
	Services map[string]*rawService `yaml:"services"`

	// Networks are the networks the services join, by name.
	Networks map[string]*rawNetwork `yaml:"networks"`

	// Volumes are the volumes the services mount, by name. A volume may be
	// declared with nothing:
	//
	//	volumes:
	//	  data:
	Volumes map[string]*rawVolume `yaml:"volumes"`
}

// rawService is one entry under services.
type rawService struct {
	// Image is the image to boot. Required.
	Image string `yaml:"image"`

	// ContainerName is the instance's name, instead of `PROJECT-SERVICE`.
	// Changing it replaces the instance at the next `dicer compose up`.
	ContainerName string `yaml:"container_name"`

	// Hostname is the guest's hostname, which other instances on its
	// network can look it up by. Unset is the service's name.
	Hostname string `yaml:"hostname"`

	// Command is the command to run: a list, or a string split as a shell
	// would split it. It replaces the image's ENTRYPOINT and CMD, as the
	// command given to dicer run does.
	Command *shellCommand `yaml:"command"`

	// Entrypoint is put before `command`, and together they replace the
	// image's ENTRYPOINT and CMD.
	Entrypoint *shellCommand `yaml:"entrypoint"`

	// Environment is the variables the command runs with. A `KEY` with no
	// value takes this shell's, and is left out if it has none.
	Environment keyValues `yaml:"environment"`

	// EnvFile is files of `KEY=VALUE` lines, read in order, relative to the
	// project directory. `environment` wins over them.
	EnvFile stringList `yaml:"env_file"`

	// Labels are the instance's labels. Those starting `dicer.compose.` are
	// `dicer compose`'s own.
	Labels keyValues `yaml:"labels"`

	// Ports are the ports to publish on the host, each as
	// `[HOST_IP:]HOST_PORT:GUEST_PORT[/tcp|udp]`, as `dicer run -p` takes it,
	// or as a mapping. An IPv6 host address goes in brackets:
	// `[::1]:8080:80`. A port must be published on a port of the host: `"80"`
	// alone is refused, as are ranges.
	Ports []rawPort `yaml:"ports"`

	// Volumes are the volumes and files to mount, each as
	// `SOURCE:TARGET[:ro]` or as a mapping. A `SOURCE` that is a name is a
	// volume, which must be declared under the file's `volumes`. One starting
	// `/`, `.` or `~` is a file on the machine running `dicer compose`,
	// relative to the project directory. It must be a file, not a directory.
	// Its contents are read when the project is loaded and sent to the
	// daemon, which puts them in the guest each time the instance starts, as
	// with `dicer run --mount type=file`. A changed file changes the
	// service's definition, so `dicer compose up` recreates its instance. A
	// `TARGET` alone, an anonymous volume, is refused: name the volume.
	Volumes []rawMount `yaml:"volumes"`

	// Tmpfs is the paths to mount an empty tmpfs at. A tmpfs takes no
	// options.
	Tmpfs stringList `yaml:"tmpfs"`

	// Networks is the network the instance joins: a list of one name, or a
	// mapping of one name to its settings, which can give the instance a
	// fixed address. Unset is the file's network called `default`, which
	// the project has whether the file declares it or not: `PROJECT-default`.
	// On its network, a service is found by its hostname, which is its name
	// unless it says otherwise, and by its instance's name.
	//
	//	networks:
	//	  backend:
	//	    ipv4_address: 172.30.0.10
	Networks serviceNetworks `yaml:"networks"`

	// DependsOn is the services to start first: a list of them, or a mapping
	// of them to the condition to wait for. `service_started`, the default,
	// waits until the service is running; `service_healthy` until it is
	// healthy, for which it must have a health check, of its own or its
	// image's; and `service_completed_successfully` until it has ended with
	// exit code 0. A service is not started if one it depends on fails to
	// come up, is unhealthy or ends with another code, and services that
	// depend on each other in a cycle are refused.
	//
	//	depends_on:
	//	  db:
	//	    condition: service_healthy
	DependsOn dependsOn `yaml:"depends_on"`

	// Restart is `no`, `always`, `unless-stopped`, `on-failure` or `on-failure:N`:
	// see [Restarts]({{< relref "/docs/guides/restarts" >}}). Unset is `no`.
	Restart string `yaml:"restart"`

	// HealthCheck is how the workload's health is checked: see [Health
	// checks]({{< relref "/docs/guides/health-checks" >}}).
	HealthCheck *rawHealthCheck `yaml:"healthcheck"`

	// CPUs is taken for `vcpus`, if it is a whole number.
	CPUs *float64 `yaml:"cpus"`

	// VCPUs is the machine's vCPUs, as `dicer run --vcpus`. Unset is 1.
	VCPUs *int32 `yaml:"vcpus"`

	// MemLimit is taken for `memory`.
	MemLimit *byteSize `yaml:"mem_limit"`

	// Memory is the machine's memory, as `dicer run --memory`. Unset is
	// 512MiB.
	Memory *byteSize `yaml:"memory"`

	// Disk is the size of the instance's disk, as `dicer run --disk`. Unset is
	// 10GiB.
	Disk *byteSize `yaml:"disk"`

	// DiskRate is the bytes per second each of the instance's disks can be
	// read and written at, as `dicer run --disk-rate`. Unset is unlimited.
	DiskRate *byteRate `yaml:"disk_rate"`

	// DiskIOPS is the operations per second each of the instance's disks can
	// be read and written at, as `dicer run --disk-iops`. Unset is unlimited.
	DiskIOPS *int64 `yaml:"disk_iops"`

	// StandbyAfter is how long the instance may be idle before it is put on
	// standby, as `dicer run --standby-after`, e.g. `15m`. Unset is never.
	StandbyAfter time.Duration `yaml:"standby_after"`

	// UploadRate is the bytes per second the instance can send, as
	// `dicer run --upload-rate`. Unset is unlimited.
	UploadRate *byteRate `yaml:"upload_rate"`

	// DownloadRate is the bytes per second the instance can receive, as
	// `dicer run --download-rate`. Unset is unlimited.
	DownloadRate *byteRate `yaml:"download_rate"`

	// Kernel is the kernel to boot, as `dicer run --kernel`. Unset is the
	// daemon's default.
	Kernel string `yaml:"kernel"`

	// KernelArgs is kernel command line arguments, as
	// `dicer run --kernel-args`.
	KernelArgs string `yaml:"kernel_args"`

	// Hypervisor is `cloud-hypervisor` or `firecracker`, as
	// `dicer run --hypervisor-type`. Unset is `cloud-hypervisor`.
	Hypervisor string `yaml:"hypervisor"`

	// HypervisorVersion is a version of the hypervisor the daemon ships, as
	// `dicer run --hypervisor-version`. Unset is the hypervisor's default
	// version, which `dicer info` shows.
	HypervisorVersion string `yaml:"hypervisor_version"`

	// InitMode is `auto`, `exec` or `systemd`, as `dicer run --init-mode`: see
	// [Init modes]({{< relref "/docs/concepts/init-modes" >}}). Unset is `auto`.
	InitMode string `yaml:"init_mode"`
}

// rawNetwork is one entry under networks. Its subnet may be given as Docker
// gives it, under ipam.
type rawNetwork struct {
	// Name is the daemon's name for the network, instead of `PROJECT-NAME`.
	Name string `yaml:"name"`

	// External uses a network that already exists, named by the key or `name`,
	// and never creates or deletes it. It takes no other settings.
	External bool `yaml:"external"`

	// Subnet is the network's subnet, such as `172.30.0.0/24`. Unset is a
	// free /24 of `10.213.0.0/16`, picked by `dicer compose up`.
	Subnet string `yaml:"subnet"`

	// Gateway is the network's gateway. Unset is the subnet's first address.
	Gateway string `yaml:"gateway"`

	// MTU is the network's MTU. Unset is 1500.
	MTU int32 `yaml:"mtu"`

	// Nameservers are the nameservers the network's instances use. Unset is
	// `8.8.8.8`.
	Nameservers stringList `yaml:"nameservers"`

	// Isolated stops the network's instances reaching each other.
	Isolated bool `yaml:"isolated"`

	// Internal stops the network's instances reaching anything beyond it:
	// the outside, other networks, the host and upstream nameservers. It
	// takes no `nameservers`.
	Internal bool `yaml:"internal"`

	// IPAM is another way of giving `subnet` and `gateway`, as Docker Compose
	// does, with one entry.
	//
	//	ipam:
	//	  config:
	//	    - subnet: 172.30.0.0/24
	IPAM *rawIPAM `yaml:"ipam"`
}

// rawIPAM is Docker's way of giving a network's addresses.
type rawIPAM struct {
	// Config is the network's addresses: one entry.
	Config []rawIPAMConfig `yaml:"config"`
}

// rawIPAMConfig is an entry of an ipam's config.
type rawIPAMConfig struct {
	// Subnet is the network's subnet.
	Subnet string `yaml:"subnet"`

	// Gateway is the network's gateway.
	Gateway string `yaml:"gateway"`
}

// rawVolume is one entry under volumes.
type rawVolume struct {
	// Name is the daemon's name for the volume, instead of `PROJECT-NAME`.
	Name string `yaml:"name"`

	// External uses a volume that already exists, named by the key or `name`,
	// and never creates or deletes it.
	External bool `yaml:"external"`

	// Size is the volume's size. Unset is 10GiB.
	Size *byteSize `yaml:"size"`
}

// rawHealthCheck is a service's healthcheck: Docker's, with http and tcp for
// the probes the guest agent runs itself. Exactly one of test, http and tcp
// is given.
type rawHealthCheck struct {
	// Test is a command to check health with: `["CMD", ARG...]` runs a
	// command; `["CMD-SHELL", LINE]`, or a string, runs a line with
	// `/bin/sh`; `["NONE"]` checks nothing, not even as the image says to.
	Test *healthCheckTest `yaml:"test"`

	// HTTP is `PORT[/path]`: healthy when a GET in the guest answers 2xx or
	// 3xx. It suits an image with no shell.
	HTTP string `yaml:"http"`

	// TCP is a port: healthy when a connection to it in the guest is
	// accepted. It suits an image with no shell.
	TCP uint32 `yaml:"tcp"`

	// Interval is the time between checks. Unset is 10s.
	Interval time.Duration `yaml:"interval"`

	// Timeout is the time a check may take. Unset is 5s.
	Timeout time.Duration `yaml:"timeout"`

	// StartPeriod is the time after a start in which failed checks do not
	// count.
	StartPeriod time.Duration `yaml:"start_period"`

	// Retries is how many failed checks in a row make the instance
	// unhealthy. Unset is 3.
	Retries int32 `yaml:"retries"`

	// Disable checks nothing, as `test: ["NONE"]` does.
	Disable bool `yaml:"disable"`
}

// stringList is a string or a list of them.
type stringList []string

func (l *stringList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*l = stringList{n.Value}
		return nil
	}

	var list []string
	if err := n.Decode(&list); err != nil {
		return err
	}
	*l = list
	return nil
}

// shellCommand is a command: a list of arguments, or a string split into
// them as a shell would.
type shellCommand []string

func (c *shellCommand) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		args, err := splitShellWords(n.Value)
		if err != nil {
			return fmt.Errorf("line %d: %w", n.Line, err)
		}
		*c = args
		return nil
	}

	var list []string
	if err := n.Decode(&list); err != nil {
		return err
	}
	*c = list
	return nil
}

// splitShellWords splits a command line into arguments as a shell would,
// honouring single and double quotes and backslash escapes. It expands
// nothing.
func splitShellWords(s string) ([]string, error) {
	var (
		args    []string
		word    strings.Builder
		inWord  bool
		quote   byte
		escaped bool
	)

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			word.WriteByte(c)
			escaped = false
		case quote == '\'':
			if c == '\'' {
				quote = 0
			} else {
				word.WriteByte(c)
			}
		case quote == '"':
			switch {
			case c == '"':
				quote = 0
			case c == '\\' && i+1 < len(s) && strings.IndexByte(`"\$`+"`", s[i+1]) >= 0:
				word.WriteByte(s[i+1])
				i++
			default:
				word.WriteByte(c)
			}
		case c == '\\':
			escaped, inWord = true, true
		case c == '\'' || c == '"':
			quote, inWord = c, true
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				args = append(args, word.String())
				word.Reset()
				inWord = false
			}
		default:
			word.WriteByte(c)
			inWord = true
		}
	}

	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote in %q", quote, s)
	}
	if escaped {
		return nil, errors.New("a command cannot end in a backslash")
	}
	if inWord {
		args = append(args, word.String())
	}
	return args, nil
}

// keyValues is a map of strings, written as a map or as a list of KEY=VALUE.
// A value that is null, or a list item with no =, is recorded as absent: for
// environment it is taken from the shell.
type keyValues map[string]*string

func (kv *keyValues) UnmarshalYAML(n *yaml.Node) error {
	out := make(keyValues)

	switch n.Kind {
	case yaml.SequenceNode:
		for _, item := range n.Content {
			if item.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: want KEY=VALUE", item.Line)
			}
			key, value, ok := strings.Cut(item.Value, "=")
			if key == "" {
				return fmt.Errorf("line %d: want KEY=VALUE, not %q", item.Line, item.Value)
			}
			if ok {
				out[key] = &value
			} else {
				out[key] = nil
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			if value.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: the value of %s must be a string, number or boolean", value.Line, key.Value)
			}
			if value.Tag == "!!null" {
				out[key.Value] = nil
				continue
			}
			v := value.Value
			out[key.Value] = &v
		}
	default:
		return fmt.Errorf("line %d: want a map or a list of KEY=VALUE", n.Line)
	}

	*kv = out
	return nil
}

// byteSize is a size: bytes as a number, or a string such as 512MiB or 2g.
type byteSize int64

func (b *byteSize) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: want a size, such as 512MiB", n.Line)
	}
	if v, err := strconv.ParseInt(n.Value, 10, 64); err == nil {
		*b = byteSize(v)
		return nil
	}

	v, err := units.RAMInBytes(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: invalid size %q: want one such as 512MiB or 2GiB", n.Line, n.Value)
	}
	*b = byteSize(v)
	return nil
}

// byteRate is bytes per second: a size, as byteSize takes it, with or
// without a trailing /s.
type byteRate int64

func (r *byteRate) UnmarshalYAML(n *yaml.Node) error {
	trimmed := *n
	trimmed.Value = strings.TrimSuffix(n.Value, "/s")
	var size byteSize
	if err := size.UnmarshalYAML(&trimmed); err != nil {
		return err
	}
	*r = byteRate(size)
	return nil
}

// rawPort is a published port: "[HOST_IP:]HOST_PORT:GUEST_PORT[/PROTOCOL]",
// or Docker's long form.
type rawPort struct {
	// Short is the entry as written, if it was written as a string.
	Short string `yaml:"-"`

	// Target is the guest's port. Required.
	Target uint32 `yaml:"target"`

	// Published is the host's port. Required.
	Published string `yaml:"published"`

	// HostIP is the host's address to publish on. Unset is every address.
	HostIP string `yaml:"host_ip"`

	// Protocol is `tcp` or `udp`. Unset is `tcp`.
	Protocol string `yaml:"protocol"`

	// Mode is accepted and ignored: it is Docker Swarm's.
	Mode string `yaml:"mode"`

	line int
}

func (p *rawPort) UnmarshalYAML(n *yaml.Node) error {
	p.line = n.Line
	if n.Kind == yaml.ScalarNode {
		p.Short = n.Value
		return nil
	}

	type long rawPort
	var l long
	if err := decodeStrict(n, &l); err != nil {
		return err
	}
	l.line = n.Line
	*p = rawPort(l)
	return nil
}

// rawMount is an entry under a service's volumes:
// "SOURCE:TARGET[:ro|rw]", or Docker's long form.
type rawMount struct {
	// Short is the entry as written, if it was written as a string.
	Short string `yaml:"-"`

	// Type is `volume`, `bind`, for a file, or `tmpfs`. Required.
	Type string `yaml:"type"`

	// Source is the volume's name, or the file's path on the machine
	// running `dicer compose`. A tmpfs has none.
	Source string `yaml:"source"`

	// Target is the path in the guest. Required.
	Target string `yaml:"target"`

	// ReadOnly mounts it read-only.
	ReadOnly bool `yaml:"read_only"`

	line int
}

func (m *rawMount) UnmarshalYAML(n *yaml.Node) error {
	m.line = n.Line
	if n.Kind == yaml.ScalarNode {
		m.Short = n.Value
		return nil
	}

	type long rawMount
	var l long
	if err := decodeStrict(n, &l); err != nil {
		return err
	}
	l.line = n.Line
	*m = rawMount(l)
	return nil
}

// serviceNetworks is the networks a service joins: a list of names, or a
// map of names to settings.
type serviceNetworks struct {
	names []string
	// settings are the settings given per network, by name.
	settings map[string]rawServiceNetwork
	line     int
}

// rawServiceNetwork is how a service joins one network.
type rawServiceNetwork struct {
	IPv4Address string `yaml:"ipv4_address"`
}

func (s *serviceNetworks) UnmarshalYAML(n *yaml.Node) error {
	s.line = n.Line
	s.settings = make(map[string]rawServiceNetwork)

	switch n.Kind {
	case yaml.SequenceNode:
		var names []string
		if err := n.Decode(&names); err != nil {
			return err
		}
		s.names = names
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			name, value := n.Content[i].Value, n.Content[i+1]
			s.names = append(s.names, name)

			var settings rawServiceNetwork
			if value.Tag != "!!null" {
				if err := decodeStrict(value, &settings); err != nil {
					return err
				}
			}
			s.settings[name] = settings
		}
	default:
		return fmt.Errorf("line %d: want a list of networks, or a map of them", n.Line)
	}
	return nil
}

// dependsOn is the services a service depends on, and on what condition: a
// list of names, or a map of names to conditions.
type dependsOn struct {
	names      []string
	conditions map[string]string
}

func (d *dependsOn) UnmarshalYAML(n *yaml.Node) error {
	d.conditions = make(map[string]string)

	switch n.Kind {
	case yaml.SequenceNode:
		var names []string
		if err := n.Decode(&names); err != nil {
			return err
		}
		for _, name := range names {
			d.names = append(d.names, name)
			d.conditions[name] = ""
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			name, value := n.Content[i].Value, n.Content[i+1]

			var dep struct {
				Condition string `yaml:"condition"`
			}
			if value.Tag != "!!null" {
				if err := decodeStrict(value, &dep); err != nil {
					return err
				}
			}
			d.names = append(d.names, name)
			d.conditions[name] = dep.Condition
		}
	default:
		return fmt.Errorf("line %d: want a list of services, or a map of them", n.Line)
	}
	return nil
}

// healthCheckTest is Docker's healthcheck test: ["CMD", arg...],
// ["CMD-SHELL", command], ["NONE"], or a string run by the shell.
type healthCheckTest struct {
	args []string
	line int
}

func (t *healthCheckTest) UnmarshalYAML(n *yaml.Node) error {
	t.line = n.Line
	if n.Kind == yaml.ScalarNode {
		t.args = []string{"CMD-SHELL", n.Value}
		return nil
	}
	return n.Decode(&t.args)
}

// decodeStrict decodes n into v, refusing keys v has no field for, with
// the line each is on.
func decodeStrict(n *yaml.Node, v any) error {
	if err := checkKeys(n, reflect.TypeOf(v)); err != nil {
		return err
	}
	return n.Decode(v)
}

var unmarshalerType = reflect.TypeFor[yaml.Unmarshaler]()

// checkKeys refuses a key in n that t, a struct or a map, list or pointer
// of them, has no field for. A type that decodes itself checks its own.
func checkKeys(n *yaml.Node, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if reflect.PointerTo(t).Implements(unmarshalerType) {
		return nil
	}

	switch t.Kind() {
	case reflect.Struct:
		if n.Kind != yaml.MappingNode {
			return nil // n.Decode says what is wrong with it
		}
		fields := yamlFields(t)
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			if key.Value == "<<" {
				// A merge key: what it merges must fit t as well.
				if err := checkMerged(value, t); err != nil {
					return err
				}
				continue
			}
			field, ok := fields[key.Value]
			if !ok {
				return fmt.Errorf("line %d: unknown key %q", key.Line, key.Value)
			}
			if err := checkKeys(value, field); err != nil {
				return err
			}
		}
	case reflect.Map:
		if n.Kind != yaml.MappingNode {
			return nil
		}
		for i := 1; i < len(n.Content); i += 2 {
			if err := checkKeys(n.Content[i], t.Elem()); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			return nil
		}
		for _, item := range n.Content {
			if err := checkKeys(item, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkMerged checks what a merge key merges: one mapping, or a list of them.
func checkMerged(n *yaml.Node, t reflect.Type) error {
	if n.Kind == yaml.SequenceNode {
		for _, item := range n.Content {
			if err := checkKeys(item, t); err != nil {
				return err
			}
		}
		return nil
	}
	return checkKeys(n, t)
}

// yamlFields maps the keys a struct decodes to the types of their fields.
func yamlFields(t reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type)
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = strings.ToLower(f.Name)
		}
		fields[name] = f.Type
	}
	return fields
}
