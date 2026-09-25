// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// The compose file as it is written: what YAML decodes into before it is
// checked and turned into a Project. Where Docker's format lets a value be
// written in more than one way -- a list or a map, a string or a list -- the
// types here take each way and keep what it says.

package compose

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/docker/go-units"
	"gopkg.in/yaml.v3"
)

// rawFile is a whole compose file.
type rawFile struct {
	// Version is Docker's obsolete version key, accepted and ignored.
	Version  string                 `yaml:"version"`
	Name     string                 `yaml:"name"`
	Services map[string]*rawService `yaml:"services"`
	Networks map[string]*rawNetwork `yaml:"networks"`
	Volumes  map[string]*rawVolume  `yaml:"volumes"`
}

// rawService is one entry under services.
type rawService struct {
	Image         string          `yaml:"image"`
	ContainerName string          `yaml:"container_name"`
	Hostname      string          `yaml:"hostname"`
	Command       *shellCommand   `yaml:"command"`
	Entrypoint    *shellCommand   `yaml:"entrypoint"`
	Environment   keyValues       `yaml:"environment"`
	EnvFile       stringList      `yaml:"env_file"`
	Labels        keyValues       `yaml:"labels"`
	Ports         []rawPort       `yaml:"ports"`
	Volumes       []rawMount      `yaml:"volumes"`
	Tmpfs         stringList      `yaml:"tmpfs"`
	Networks      serviceNetworks `yaml:"networks"`
	DependsOn     dependsOn       `yaml:"depends_on"`
	Restart       string          `yaml:"restart"`
	Healthcheck   *rawHealthcheck `yaml:"healthcheck"`
	CPUs          *float64        `yaml:"cpus"`
	VCPUs         *int32          `yaml:"vcpus"`
	MemLimit      *byteSize       `yaml:"mem_limit"`
	Memory        *byteSize       `yaml:"memory"`
	Disk          *byteSize       `yaml:"disk"`
	Kernel        string          `yaml:"kernel"`
	KernelArgs    string          `yaml:"kernel_args"`
	Hypervisor    string          `yaml:"hypervisor"`
	HypervisorVer string          `yaml:"hypervisor_version"`
	InitMode      string          `yaml:"init_mode"`
}

// rawNetwork is one entry under networks. Its subnet may be given as Docker
// gives it, under ipam.
type rawNetwork struct {
	Name        string     `yaml:"name"`
	External    bool       `yaml:"external"`
	Subnet      string     `yaml:"subnet"`
	Gateway     string     `yaml:"gateway"`
	MTU         int32      `yaml:"mtu"`
	Nameservers stringList `yaml:"nameservers"`
	Isolated    bool       `yaml:"isolated"`
	IPAM        *rawIPAM   `yaml:"ipam"`
}

// rawIPAM is Docker's way of giving a network's addresses.
type rawIPAM struct {
	Config []struct {
		Subnet  string `yaml:"subnet"`
		Gateway string `yaml:"gateway"`
	} `yaml:"config"`
}

// rawVolume is one entry under volumes.
type rawVolume struct {
	Name     string    `yaml:"name"`
	External bool      `yaml:"external"`
	Size     *byteSize `yaml:"size"`
}

// rawHealthcheck is a service's healthcheck: Docker's, with http and tcp for
// the probes the guest agent runs itself.
type rawHealthcheck struct {
	Test        *healthTest   `yaml:"test"`
	HTTP        string        `yaml:"http"`
	TCP         uint32        `yaml:"tcp"`
	Interval    time.Duration `yaml:"interval"`
	Timeout     time.Duration `yaml:"timeout"`
	StartPeriod time.Duration `yaml:"start_period"`
	Retries     int32         `yaml:"retries"`
	Disable     bool          `yaml:"disable"`
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

// rawPort is a published port: "[HOST_IP:]HOST_PORT:GUEST_PORT[/PROTOCOL]",
// or Docker's long form.
type rawPort struct {
	Short string `yaml:"-"`

	Target    uint32 `yaml:"target"`
	Published string `yaml:"published"`
	HostIP    string `yaml:"host_ip"`
	Protocol  string `yaml:"protocol"`
	Mode      string `yaml:"mode"`
	line      int
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
	Short string `yaml:"-"`

	Type     string `yaml:"type"`
	Source   string `yaml:"source"`
	Target   string `yaml:"target"`
	ReadOnly bool   `yaml:"read_only"`
	line     int
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

// healthTest is Docker's healthcheck test: ["CMD", arg...],
// ["CMD-SHELL", command], ["NONE"], or a string run by the shell.
type healthTest struct {
	args []string
	line int
}

func (t *healthTest) UnmarshalYAML(n *yaml.Node) error {
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
