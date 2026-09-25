// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package compose

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/konradasb/dicer/internal/naming"
)

// FileNames are the names a compose file is looked for under, in order.
var FileNames = []string{"dicer-compose.yaml", "dicer-compose.yml", "compose.yaml", "compose.yml"}

// Options say where a project's file is and how to read it.
type Options struct {
	// File is the compose file. Empty means the first of FileNames in
	// WorkDir or the nearest directory above it that has one.
	File string
	// WorkDir is where to look for the file. Empty means the current
	// directory.
	WorkDir string
	// ProjectDir is the directory relative paths are taken from, and the
	// .env file read from. Empty means the file's directory.
	ProjectDir string
	// ProjectName overrides the file's name and the directory's.
	ProjectName string
	// EnvFile holds variables for substitution. Empty means .env in the
	// project directory, if there is one.
	EnvFile string
	// Lookup finds the environment's variables, which win over the env
	// file's. Nil means the process's.
	Lookup Lookup
}

// Load reads and checks a project's compose file.
func Load(opts Options) (*Project, error) {
	file, err := findFile(opts)
	if err != nil {
		return nil, err
	}

	dir := opts.ProjectDir
	if dir == "" {
		dir = filepath.Dir(file)
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return nil, err
	}

	lookup, err := environment(opts, dir)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}

	p, err := parse(data, dir, opts.ProjectName, lookup)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	p.File = file
	return p, nil
}

// findFile returns the compose file Options name, or finds one.
func findFile(opts Options) (string, error) {
	if opts.File != "" {
		return filepath.Abs(opts.File)
	}

	dir := opts.WorkDir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = wd
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}

	for {
		for _, name := range FileNames {
			path := filepath.Join(dir, name)
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no compose file here or in any directory above: name one with -f, or add %s",
				FileNames[0])
		}
		dir = parent
	}
}

// environment returns what variables are looked up in: the process's, then
// the env file's.
func environment(opts Options, dir string) (Lookup, error) {
	lookup := opts.Lookup
	if lookup == nil {
		lookup = os.LookupEnv
	}

	path, required := opts.EnvFile, true
	if path == "" {
		path, required = filepath.Join(dir, ".env"), false
	}

	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) && !required {
		return lookup, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	vars, err := parseEnvFile(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	return func(name string) (string, bool) {
		if v, ok := lookup(name); ok {
			return v, true
		}
		if v, ok := vars[name]; ok && v != nil {
			return *v, true
		}
		return "", false
	}, nil
}

// parse reads a compose file's contents into a project.
func parse(data []byte, dir, name string, lookup Lookup) (*Project, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, errors.New("the file is empty")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("line %d: want a map of services, networks and volumes", root.Line)
	}

	expandAliases(root)
	applyMerges(root)
	dropExtensions(root)
	written, err := encodeYAML(deepCopy(root))
	if err != nil {
		return nil, err
	}
	if err := interpolateNode(root, lookup); err != nil {
		return nil, err
	}
	if err := refuseUnsupported(root); err != nil {
		return nil, err
	}

	var raw rawFile
	if err := decodeStrict(root, &raw); err != nil {
		return nil, err
	}

	resolved, err := encodeYAML(root)
	if err != nil {
		return nil, err
	}

	b := &builder{dir: dir, lookup: lookup}
	p, err := b.project(&raw, name)
	if err != nil {
		return nil, err
	}
	p.Written, p.Resolved = written, resolved
	return p, nil
}

// encodeYAML writes n out as YAML, indented as a person would write it.
func encodeYAML(n *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// expandAliases replaces every alias with a copy of what it refers to, so
// that the extensions an anchor is usually defined in can be dropped.
func expandAliases(n *yaml.Node) {
	for i, c := range n.Content {
		if c.Kind == yaml.AliasNode {
			c = deepCopy(c.Alias)
			n.Content[i] = c
		}
		expandAliases(c)
	}
}

func deepCopy(n *yaml.Node) *yaml.Node {
	for n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	c := *n
	c.Anchor = ""
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		c.Content[i] = deepCopy(child)
	}
	return &c
}

// applyMerges replaces every merge key, <<, with the keys it merges that
// its mapping does not set itself. Of several mappings merged, the first to
// set a key wins, as YAML has it.
func applyMerges(n *yaml.Node) {
	for _, c := range n.Content {
		applyMerges(c)
	}
	if n.Kind != yaml.MappingNode {
		return
	}

	own := make(map[string]bool)
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value != "<<" {
			own[n.Content[i].Value] = true
		}
	}

	var merged []*yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i], n.Content[i+1]
		if key.Tag != "!!merge" {
			merged = append(merged, key, value)
			continue
		}

		sources := []*yaml.Node{value}
		if value.Kind == yaml.SequenceNode {
			sources = value.Content
		}
		for _, src := range sources {
			if src.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(src.Content); j += 2 {
				if k := src.Content[j].Value; !own[k] {
					own[k] = true
					merged = append(merged, src.Content[j], src.Content[j+1])
				}
			}
		}
	}
	n.Content = merged
}

// dropExtensions removes the x- keys Docker Compose leaves for tools and
// anchors: at the top level, and in each service, network and volume.
func dropExtensions(root *yaml.Node) {
	dropXKeys(root)
	for i := 0; i+1 < len(root.Content); i += 2 {
		switch root.Content[i].Value {
		case "services", "networks", "volumes":
			section := root.Content[i+1]
			if section.Kind != yaml.MappingNode {
				continue
			}
			for j := 1; j < len(section.Content); j += 2 {
				dropXKeys(section.Content[j])
			}
		}
	}
}

func dropXKeys(n *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		return
	}
	kept := n.Content[:0]
	for i := 0; i+1 < len(n.Content); i += 2 {
		if !strings.HasPrefix(n.Content[i].Value, "x-") {
			kept = append(kept, n.Content[i], n.Content[i+1])
		}
	}
	n.Content = kept
}

// interpolateNode substitutes variables in every value under n. Keys are
// left as they are.
func interpolateNode(n *yaml.Node, lookup Lookup) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if !strings.Contains(n.Value, "$") {
			return nil
		}
		v, err := interpolate(n.Value, lookup)
		if err != nil {
			return fmt.Errorf("line %d: %w", n.Line, err)
		}
		n.Value = v
		// An unquoted, untagged value was taken for a string because of
		// its $: its type is worked out again from what replaced it, so
		// that cpus: ${CPUS:-2} is a number. A quoted or tagged value
		// stays what it says it is.
		if n.Style&(yaml.TaggedStyle|yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle|
			yaml.LiteralStyle|yaml.FoldedStyle) == 0 {
			n.Tag = ""
		}
	case yaml.MappingNode:
		for i := 1; i < len(n.Content); i += 2 {
			if err := interpolateNode(n.Content[i], lookup); err != nil {
				return err
			}
		}
	default:
		for _, c := range n.Content {
			if err := interpolateNode(c, lookup); err != nil {
				return err
			}
		}
	}
	return nil
}

// unsupported are service keys of Docker Compose's that describe something
// a virtual machine does not have, or dicer compose does not do, and why.
var unsupported = map[string]string{
	"build":        "dicer boots images from a registry: build and push the image, then name it with image",
	"deploy":       "set vcpus, memory and disk on the service instead",
	"scale":        "each service is one instance",
	"privileged":   "a guest is a whole machine, and its workload already has it to itself",
	"cap_add":      "a guest is a whole machine, and its workload already has it to itself",
	"cap_drop":     "a guest is a whole machine, and its workload already has it to itself",
	"devices":      "instances cannot be given host devices",
	"network_mode": "an instance joins one network, named with networks",
	"links":        "reach other services by address: see networks and ipv4_address",
	"extra_hosts":  "an instance's /etc/hosts cannot be added to",
	"dns":          "set nameservers on the network instead",
	"user":         "the image's user runs the command",
	"working_dir":  "the image's working directory is used",
	"stdin_open":   "attach with dicer compose exec instead",
	"tty":          "attach with dicer compose exec instead",
	"profiles":     "every service in the file is part of the project",
	"extends":      "use YAML anchors and x- extensions to share settings",
	"secrets":      "mount the secret's file with volumes instead",
	"configs":      "mount the config's file with volumes instead",
	"init":         "dicer-init is always the guest's first process; see init_mode",
	"platform":     "an instance runs on the host's architecture",
	"pull_policy":  "use dicer compose up --pull instead",
	"logging":      "the guest's console is kept with the instance: see dicer compose logs",
	"ulimits":      "set limits in the guest's own configuration",
	"sysctls":      "set kernel parameters in the guest, or with kernel_args",
	"shm_size":     "mount a tmpfs at /dev/shm instead",
	"expose":       "every port is open on the instance's network already",
	"stop_signal":  "a stop shuts the guest down",
}

// refuseUnsupported fails on the first Docker Compose key dicer compose
// knows of but cannot honour, saying why. Keys it does not know of at all
// are refused as unknown when the file is decoded.
func refuseUnsupported(root *yaml.Node) error {
	for i := 0; i+1 < len(root.Content); i += 2 {
		switch key := root.Content[i]; key.Value {
		case "secrets", "configs":
			return fmt.Errorf("line %d: %s are not supported: mount their files with a service's volumes", key.Line, key.Value)
		case "services":
			services := root.Content[i+1]
			if services.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(services.Content); j += 2 {
				name, service := services.Content[j], services.Content[j+1]
				if service.Kind != yaml.MappingNode {
					continue
				}
				for k := 0; k+1 < len(service.Content); k += 2 {
					key := service.Content[k]
					if why, ok := unsupported[key.Value]; ok {
						return fmt.Errorf("line %d: service %s: %s is not supported: %s",
							key.Line, name.Value, key.Value, why)
					}
				}
			}
		}
	}
	return nil
}

// notProjectNameChars are what a directory's name loses to become a
// project's.
var notProjectNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

// projectNameFromDir makes a project name from a directory's name, as Docker
// Compose does: lower case, with what a name cannot hold replaced.
func projectNameFromDir(dir string) string {
	name := notProjectNameChars.ReplaceAllString(strings.ToLower(filepath.Base(dir)), "-")
	return strings.Trim(name, "-")
}

// builder turns a decoded file into a project.
type builder struct {
	dir    string
	lookup Lookup
	p      *Project
}

func (b *builder) project(raw *rawFile, name string) (*Project, error) {
	if name == "" {
		name = raw.Name
	}
	if name == "" {
		name = projectNameFromDir(b.dir)
	}
	if err := naming.Validate(name); err != nil {
		return nil, fmt.Errorf("project name %q: use letters, digits and hyphens, starting and ending "+
			"with a letter or digit; set another with -p or name", name)
	}

	b.p = &Project{
		Name:     name,
		Dir:      b.dir,
		Services: make(map[string]*Service),
		Networks: make(map[string]*Network),
		Volumes:  make(map[string]*Volume),
	}

	if len(raw.Services) == 0 {
		return nil, errors.New("the file has no services")
	}

	for key, n := range raw.Networks {
		network, err := b.network(key, n)
		if err != nil {
			return nil, fmt.Errorf("network %s: %w", key, err)
		}
		b.p.Networks[key] = network
	}
	for key, v := range raw.Volumes {
		volume, err := b.volume(key, v)
		if err != nil {
			return nil, fmt.Errorf("volume %s: %w", key, err)
		}
		b.p.Volumes[key] = volume
	}

	for _, key := range slices.Sorted(maps.Keys(raw.Services)) {
		s := raw.Services[key]
		if s == nil {
			return nil, fmt.Errorf("service %s: it needs an image", key)
		}
		service, err := b.service(key, s)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", key, err)
		}
		b.p.Services[key] = service
	}

	if err := b.checkDependencies(); err != nil {
		return nil, err
	}
	if err := b.checkInstanceNames(); err != nil {
		return nil, err
	}
	return b.p, nil
}

// resourceName is the daemon's name for a project's own network or volume:
// the project's name, then the file's.
func (b *builder) resourceName(key, name string, external bool) (string, error) {
	switch {
	case name != "":
	case external:
		name = key
	default:
		name = b.p.Name + "-" + key
	}

	if err := naming.Validate(name); err != nil {
		return "", fmt.Errorf("%q cannot be a name: use letters, digits and hyphens", name)
	}
	return name, nil
}

// checkDependencies refuses a dependency on a service that is not there, or
// a cycle of them.
func (b *builder) checkDependencies() error {
	for _, name := range b.p.ServiceNames() {
		for _, dep := range b.p.Services[name].DependsOn {
			if dep.Service == name {
				return fmt.Errorf("service %s depends on itself", name)
			}
			if _, ok := b.p.Services[dep.Service]; !ok {
				return fmt.Errorf("service %s depends on %s, which is not a service", name, dep.Service)
			}
		}
	}

	const (
		unvisited = iota
		visiting
		done
	)
	state := make(map[string]int)
	var path []string
	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case visiting:
			start := slices.Index(path, name)
			cycle := append(slices.Clone(path[start:]), name)
			return fmt.Errorf("services depend on each other in a cycle: %s", strings.Join(cycle, " -> "))
		case done:
			return nil
		}

		state[name] = visiting
		path = append(path, name)
		for _, dep := range b.p.Services[name].DependsOn {
			if err := visit(dep.Service); err != nil {
				return err
			}
		}
		path = path[:len(path)-1]
		state[name] = done
		return nil
	}

	for _, name := range b.p.ServiceNames() {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

// checkInstanceNames refuses two services with one instance name, which
// container_name can give them.
func (b *builder) checkInstanceNames() error {
	seen := make(map[string]string)
	for _, name := range b.p.ServiceNames() {
		instance := b.p.Services[name].Instance.GetName()
		if other, ok := seen[instance]; ok {
			return fmt.Errorf("services %s and %s would both be instance %s", other, name, instance)
		}
		seen[instance] = name
	}
	return nil
}

// resolvePath makes a path from the file absolute: from the project
// directory, or from the home directory for one starting ~.
func (b *builder) resolvePath(path string) (string, error) {
	if rest, ok := strings.CutPrefix(path, "~"); ok && (rest == "" || strings.HasPrefix(rest, "/")) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, rest), nil
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	return filepath.Join(b.dir, path), nil
}

// readEnvFile reads an env_file, relative to the project directory.
func (b *builder) readEnvFile(path string) (map[string]*string, error) {
	abs, err := b.resolvePath(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("env_file: %w", err)
	}
	vars, err := parseEnvFile(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("env_file %s: %w", path, err)
	}
	return vars, nil
}
