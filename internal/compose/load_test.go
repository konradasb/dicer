// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package compose

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// load loads the compose file given, in a directory named dir, with an
// environment of vars.
func load(t *testing.T, dir, compose string, vars map[string]string) (*Project, error) {
	t.Helper()
	root := writeProjectFiles(t, dir, map[string]string{"compose.yaml": compose})
	return Load(Options{WorkDir: root, Lookup: lookupIn(vars)})
}

// writeProjectFiles writes files into a directory of its own named dir.
func writeProjectFiles(t *testing.T, dir string, files map[string]string) string {
	t.Helper()

	root := filepath.Join(t.TempDir(), dir)
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func mustLoad(t *testing.T, compose string, vars map[string]string) *Project {
	t.Helper()
	p, err := load(t, "shop", compose, vars)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return p
}

func TestLoadServiceBecomesAnInstance(t *testing.T) {
	root := writeProjectFiles(t, "shop", map[string]string{
		"compose.yaml": `
services:
  web:
    image: nginx:${TAG:-1.27}
    hostname: www
    entrypoint: /docker-entrypoint.sh
    command: nginx -g 'daemon off;'
    environment:
      MODE: production
      FROM_SHELL:
      NOT_IN_SHELL:
    env_file: web.env
    labels: [team=web, bare]
    ports:
      - 8080:80
      - 127.0.0.1:5353:53/udp
      - target: 443
        published: "8443"
        protocol: tcp
    volumes:
      - data:/srv/data
      - ./nginx.conf:/etc/nginx/nginx.conf:ro
      - type: tmpfs
        target: /cache
    tmpfs: /run
    networks:
      backend:
        ipv4_address: 172.30.0.10
    restart: on-failure:5
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost/"]
      interval: 30s
      timeout: 2s
      start_period: 1m
      retries: 4
    cpus: 2
    mem_limit: 1g
    disk: 20GiB
    kernel: linux-6.18
    kernel_args: quiet
    hypervisor: firecracker
    hypervisor_version: v1.17.0
    init_mode: exec
networks:
  backend:
    subnet: 172.30.0.0/24
volumes:
  data:
    size: 5GiB
`,
		"web.env": "MODE=development\nFROM_FILE=1\n",
	})

	p, err := Load(Options{WorkDir: root, Lookup: lookupIn(map[string]string{"FROM_SHELL": "shell"})})
	if err != nil {
		t.Fatal(err)
	}

	if p.Name != "shop" {
		t.Errorf("project name = %q, want the directory's, shop", p.Name)
	}

	got := p.Services["web"].Instance
	want := &dicerdv1.CreateInstanceRequest{
		Name:              "shop-web",
		ImageRef:          "nginx:1.27",
		Hostname:          "www",
		Cmd:               []string{"/docker-entrypoint.sh", "nginx", "-g", "daemon off;"},
		Env:               map[string]string{"MODE": "production", "FROM_SHELL": "shell", "FROM_FILE": "1"},
		HypervisorType:    dicerdv1.HypervisorType_HYPERVISOR_TYPE_FIRECRACKER,
		HypervisorVersion: "v1.17.0",
		KernelName:        "linux-6.18",
		KernelArgs:        "quiet",
		InitMode:          dicerdv1.InitMode_INIT_MODE_EXEC,
		Vcpus:             2,
		MemoryBytes:       1 << 30,
		DiskBytes:         20 << 30,
		NetworkName:       "shop-backend",
		StaticIp:          "172.30.0.10",
		Ports: []*dicerdv1.PortMapping{
			{HostPort: 8080, GuestPort: 80},
			{HostIp: "127.0.0.1", HostPort: 5353, GuestPort: 53, Protocol: dicerdv1.Protocol_PROTOCOL_UDP},
			{HostPort: 8443, GuestPort: 443, Protocol: dicerdv1.Protocol_PROTOCOL_TCP},
		},
		Mounts: []*dicerdv1.Mount{
			{Type: dicerdv1.MountType_MOUNT_TYPE_VOLUME, Source: "shop-data", Target: "/srv/data"},
			{
				Type: dicerdv1.MountType_MOUNT_TYPE_FILE, Source: filepath.Join(root, "nginx.conf"),
				Target: "/etc/nginx/nginx.conf", ReadOnly: true,
			},
			{Type: dicerdv1.MountType_MOUNT_TYPE_TMPFS, Target: "/cache"},
			{Type: dicerdv1.MountType_MOUNT_TYPE_TMPFS, Target: "/run"},
		},
		RestartPolicy: &dicerdv1.RestartPolicy{Mode: dicerdv1.RestartMode_RESTART_MODE_ON_FAILURE, MaxRetries: 5},
		HealthCheck: &dicerdv1.HealthCheck{
			Probe: &dicerdv1.HealthCheck_Exec{Exec: &dicerdv1.HealthCheckExec{
				Command: []string{"curl", "-f", "http://localhost/"},
			}},
			Interval:    durationpb.New(30 * time.Second),
			Timeout:     durationpb.New(2 * time.Second),
			StartPeriod: durationpb.New(time.Minute),
			Retries:     4,
		},
		Labels: map[string]string{
			"team": "web", "bare": "",
			LabelProject: "shop", LabelService: "web",
		},
	}
	want.Labels[LabelConfigHash] = ConfigHash(want)

	if !proto.Equal(got, want) {
		t.Errorf("instance =\n%v\nwant\n%v", got, want)
	}

	if n := p.Networks["backend"]; n.Name != "shop-backend" || !proto.Equal(n.Request, &dicerdv1.CreateNetworkRequest{
		Name: "shop-backend", Subnet: "172.30.0.0/24",
	}) {
		t.Errorf("network = %+v", n)
	}
	if v := p.Volumes["data"]; !proto.Equal(v.Request, &dicerdv1.CreateVolumeRequest{
		Name: "shop-data", SizeBytes: 5 << 30,
	}) {
		t.Errorf("volume = %+v", v)
	}
}

func TestLoadDefaults(t *testing.T) {
	p := mustLoad(t, `
services:
  cache:
    image: redis:7
volumes:
  unsized:
`, nil)

	got := p.Services["cache"].Instance
	if got.GetVcpus() != 1 || got.GetMemoryBytes() != 512<<20 || got.GetDiskBytes() != 10<<30 {
		t.Errorf("sizes = %d vCPU, %d, %d; want dicer run's defaults", got.GetVcpus(), got.GetMemoryBytes(), got.GetDiskBytes())
	}
	if got.GetNetworkName() != "" {
		t.Errorf("network = %q, want the daemon's default", got.GetNetworkName())
	}
	if got.Cmd != nil {
		t.Errorf("cmd = %q, want the image's", got.GetCmd())
	}
	if p.Volumes["unsized"].Request.GetSizeBytes() != 10<<30 {
		t.Errorf("volume size = %d, want 10GiB", p.Volumes["unsized"].Request.GetSizeBytes())
	}
}

func TestLoadNamesTheProject(t *testing.T) {
	p, err := load(t, "My_App.v2", "services: {web: {image: nginx}}", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "my-app-v2" {
		t.Errorf("name from directory = %q, want my-app-v2", p.Name)
	}

	p = mustLoad(t, "name: store\nservices: {web: {image: nginx}}", nil)
	if p.Name != "store" || p.Services["web"].Instance.GetName() != "store-web" {
		t.Errorf("name from file = %q, instance %q", p.Name, p.Services["web"].Instance.GetName())
	}

	root := writeProjectFiles(t, "shop", map[string]string{"compose.yaml": "name: store\nservices: {web: {image: nginx}}"})
	p, err = Load(Options{WorkDir: root, ProjectName: "override", Lookup: lookupIn(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "override" {
		t.Errorf("name from options = %q, want override", p.Name)
	}

	if _, err := Load(Options{WorkDir: root, ProjectName: "not_valid", Lookup: lookupIn(nil)}); err == nil {
		t.Error("an invalid project name was accepted")
	}
}

func TestLoadExtensionsAndAnchors(t *testing.T) {
	p := mustLoad(t, `
x-common: &common
  image: alpine:3.21
  restart: always
  environment:
    SHARED: "1"
services:
  a:
    <<: *common
    x-note: tools may keep what they like here
  b:
    <<: *common
    image: busybox
`, nil)

	a, b := p.Services["a"].Instance, p.Services["b"].Instance
	if a.GetImageRef() != "alpine:3.21" || a.GetEnv()["SHARED"] != "1" ||
		a.GetRestartPolicy().GetMode() != dicerdv1.RestartMode_RESTART_MODE_ALWAYS {
		t.Errorf("a = %v, want what the anchor gives", a)
	}
	if b.GetImageRef() != "busybox" {
		t.Errorf("b's image = %q, want its own to win over the anchor's", b.GetImageRef())
	}
	if strings.Contains(string(p.Resolved), "x-") {
		t.Errorf("resolved file keeps extensions:\n%s", p.Resolved)
	}
}

func TestLoadEnvFile(t *testing.T) {
	root := writeProjectFiles(t, "shop", map[string]string{
		"compose.yaml": "services: {web: {image: 'nginx:${TAG}', environment: [PORT]}}",
		".env":         "TAG=from-dotenv\nPORT=80\n",
	})

	p, err := Load(Options{WorkDir: root, Lookup: lookupIn(nil)})
	if err != nil {
		t.Fatal(err)
	}
	web := p.Services["web"].Instance
	if web.GetImageRef() != "nginx:from-dotenv" {
		t.Errorf("image = %q, want .env's tag", web.GetImageRef())
	}
	if web.GetEnv()["PORT"] != "80" {
		t.Errorf("PORT = %q, want it taken from .env", web.GetEnv()["PORT"])
	}

	// The environment wins over .env.
	p, err = Load(Options{WorkDir: root, Lookup: lookupIn(map[string]string{"TAG": "from-shell"})})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Services["web"].Instance.GetImageRef(); got != "nginx:from-shell" {
		t.Errorf("image = %q, want the environment's tag", got)
	}

	// An env file named with --env-file must be there.
	if _, err := Load(Options{WorkDir: root, EnvFile: filepath.Join(root, "missing.env")}); err == nil {
		t.Error("a missing --env-file was accepted")
	}
}

func TestLoadFindsTheFile(t *testing.T) {
	root := writeProjectFiles(t, "shop", map[string]string{
		"dicer-compose.yaml": "services: {web: {image: nginx}}",
		"compose.yaml":       "services: {other: {image: nginx}}",
		"sub/dir/.keep":      "",
	})

	// From a directory below, the nearest file is found, and
	// dicer-compose.yaml is preferred to compose.yaml.
	p, err := Load(Options{WorkDir: filepath.Join(root, "sub", "dir"), Lookup: lookupIn(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if p.File != filepath.Join(root, "dicer-compose.yaml") {
		t.Errorf("file = %s, want dicer-compose.yaml", p.File)
	}
	if p.Dir != root || p.Name != "shop" {
		t.Errorf("dir = %s, name = %s; want the file's directory", p.Dir, p.Name)
	}

	if _, err := Load(Options{WorkDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "no compose file") {
		t.Errorf("Load with no file = %v, want one saying there is none", err)
	}
}

func TestLoadDefaultNetwork(t *testing.T) {
	p := mustLoad(t, `
services:
  web: {image: nginx}
  db: {image: postgres, networks: [private]}
networks:
  default: {ipam: {config: [{subnet: 172.31.0.0/24, gateway: 172.31.0.254}]}}
  private: {name: shared-private, external: true}
`, nil)

	if got := p.Services["web"].Instance.GetNetworkName(); got != "shop-default" {
		t.Errorf("web's network = %q, want the file's default network", got)
	}
	if got := p.Services["db"].Instance.GetNetworkName(); got != "shared-private" {
		t.Errorf("db's network = %q, want the external network's own name", got)
	}
	if req := p.Networks["default"].Request; req.GetGateway() != "172.31.0.254" {
		t.Errorf("default network = %v, want ipam's subnet and gateway", req)
	}
	if p.Networks["private"].Request != nil {
		t.Error("an external network has a create request")
	}
}

func TestLoadVariablesInNumbersAndBooleans(t *testing.T) {
	p := mustLoad(t, `
services:
  web:
    image: nginx
    cpus: ${CPUS:-2}
    ports:
      - target: ${PORT:-80}
        published: ${HOST_PORT:-8080}
    volumes:
      - type: volume
        source: data
        target: /data
        read_only: ${RO:-true}
    networks: [lan]
    healthcheck:
      tcp: ${PORT:-80}
      retries: ${RETRIES}
  job:
    image: busybox
    vcpus: ${VCPUS:-3}
    healthcheck:
      disable: ${OFF:-true}
networks:
  lan:
    subnet: 10.0.0.0/24
    mtu: ${MTU:-1400}
    isolated: ${ISOLATED:-false}
  shared:
    external: ${EXTERNAL:-true}
volumes:
  data:
`, map[string]string{"RETRIES": "5"})

	web, job := p.Services["web"].Instance, p.Services["job"].Instance
	if web.GetVcpus() != 2 || job.GetVcpus() != 3 {
		t.Errorf("vcpus = %d and %d, want 2 and 3", web.GetVcpus(), job.GetVcpus())
	}
	if port := web.GetPorts()[0]; port.GetGuestPort() != 80 || port.GetHostPort() != 8080 {
		t.Errorf("port = %v, want 8080:80", port)
	}
	if !web.GetMounts()[0].GetReadOnly() {
		t.Error("read_only from a variable was not taken")
	}
	if hc := web.GetHealthCheck(); hc.GetTcp().GetPort() != 80 || hc.GetRetries() != 5 {
		t.Errorf("healthcheck = %v, want tcp 80 and 5 retries", hc)
	}
	if !job.GetHealthCheck().GetDisabled() {
		t.Error("disable from a variable was not taken")
	}
	if req := p.Networks["lan"].Request; req.GetMtu() != 1400 || req.GetIsolated() {
		t.Errorf("lan = %v, want mtu 1400, not isolated", req)
	}
	if !p.Networks["shared"].External {
		t.Error("external from a variable was not taken")
	}

	// A quoted value is a string, whatever it holds.
	p = mustLoad(t, `services: {web: {image: nginx, environment: {N: "${N:-2}", B: '${B:-true}'}}}`, nil)
	if env := p.Services["web"].Instance.GetEnv(); env["N"] != "2" || env["B"] != "true" {
		t.Errorf("env = %v, want the quoted values as strings", env)
	}
}

func TestLoadHealthchecks(t *testing.T) {
	tests := []struct {
		check string
		want  *dicerdv1.HealthCheck
	}{
		{
			"{test: pg_isready -U postgres}",
			&dicerdv1.HealthCheck{Probe: &dicerdv1.HealthCheck_Exec{Exec: &dicerdv1.HealthCheckExec{
				Command: []string{"/bin/sh", "-c", "pg_isready -U postgres"},
			}}},
		},
		{
			`{test: ["CMD-SHELL", "exit 0"]}`,
			&dicerdv1.HealthCheck{Probe: &dicerdv1.HealthCheck_Exec{Exec: &dicerdv1.HealthCheckExec{
				Command: []string{"/bin/sh", "-c", "exit 0"},
			}}},
		},
		{
			"{http: 3000/healthz}",
			&dicerdv1.HealthCheck{Probe: &dicerdv1.HealthCheck_Http{Http: &dicerdv1.HealthCheckHTTP{
				Port: 3000, Path: "/healthz",
			}}},
		},
		{
			"{tcp: 6379}",
			&dicerdv1.HealthCheck{Probe: &dicerdv1.HealthCheck_Tcp{Tcp: &dicerdv1.HealthCheckTCP{Port: 6379}}},
		},
		{`{test: ["NONE"]}`, &dicerdv1.HealthCheck{Disabled: true}},
		{"{disable: true}", &dicerdv1.HealthCheck{Disabled: true}},
	}

	for _, tt := range tests {
		p, err := load(t, "shop", "services: {db: {image: postgres, healthcheck: "+tt.check+"}}", nil)
		if err != nil {
			t.Errorf("healthcheck %s: %v", tt.check, err)
			continue
		}
		if got := p.Services["db"].Instance.GetHealthCheck(); !proto.Equal(got, tt.want) {
			t.Errorf("healthcheck %s = %v, want %v", tt.check, got, tt.want)
		}
	}
}

func TestLoadRefuses(t *testing.T) {
	tests := []struct {
		name, compose, want string
	}{
		{"empty", "", "empty"},
		{"no services", "name: x", "no services"},
		{"no image", "services: {web: {hostname: x}}", "service web: it needs an image"},
		{"null service", "services: {web: }", "service web: it needs an image"},
		{"unknown key", "services: {web: {image: nginx, imagee: x}}", `line 1: unknown key "imagee"`},
		{"unknown nested key", "services:\n  web:\n    image: nginx\n    ports:\n      - {target: 80, publishd: 80}",
			`line 5: unknown key "publishd"`},
		{"build", "services:\n  web:\n    build: .", "line 3: service web: build is not supported: dicer boots images"},
		{"deploy", "services: {web: {image: nginx, deploy: {}}}", "set vcpus, memory and disk"},
		{"secrets", "secrets: {}\nservices: {web: {image: nginx}}", "secrets are not supported"},
		{"bad service name", "services: {db_primary: {image: postgres}}", `would be named "shop-db_primary"`},
		{"same instance name", "services: {a: {image: x, container_name: one}, b: {image: x, container_name: one}}",
			"services a and b would both be instance one"},
		{"unknown dependency", "services: {web: {image: x, depends_on: [db]}}", "depends on db, which is not a service"},
		{"self dependency", "services: {web: {image: x, depends_on: [web]}}", "depends on itself"},
		{"cycle", "services: {a: {image: x, depends_on: [b]}, b: {image: x, depends_on: [c]}, c: {image: x, depends_on: [a]}}",
			"cycle: a -> b -> c -> a"},
		{"bad condition", "services: {a: {image: x, depends_on: {b: {condition: done}}}, b: {image: x}}",
			`invalid condition "done"`},
		{"two networks", "services: {web: {image: x, networks: [a, b]}}\nnetworks: {a: {subnet: 10.0.0.0/24}, b: {subnet: 10.1.0.0/24}}",
			"joins one network, not 2"},
		{"undeclared network", "services: {web: {image: x, networks: [a]}}", "network a is not declared"},
		{"network without subnet", "services: {web: {image: x}}\nnetworks: {a: {}}", "network a: it needs a subnet"},
		{"external network with settings", "services: {web: {image: x}}\nnetworks: {a: {external: true, subnet: 10.0.0.0/24}}",
			"external network is used as it is"},
		{"undeclared volume", "services: {web: {image: x, volumes: [data:/data]}}", "volume data is not declared"},
		{"anonymous volume", "services: {web: {image: x, volumes: [/data]}}", "anonymous volumes are not supported"},
		{"relative target", "services: {web: {image: x, volumes: ['./a:b']}}", "want an absolute path"},
		{"bad mode", "services: {web: {image: x, volumes: ['./a:/b:z']}}", `invalid mode "z"`},
		{"guest port only", "services: {web: {image: x, ports: ['80']}}", "HOST_PORT:80"},
		{"port range", "services: {web: {image: x, ports: ['8000-8001:80']}}", "is a range"},
		{"bad protocol", "services: {web: {image: x, ports: ['80:80/sctp']}}", `invalid protocol "sctp"`},
		{"fractional cpus", "services: {web: {image: x, cpus: 0.5}}", "not a whole number"},
		{"cpus and vcpus", "services: {web: {image: x, cpus: 1, vcpus: 2}}", "vcpus or cpus, not both"},
		{"bad size", "services: {web: {image: x, memory: lots}}", `invalid size "lots"`},
		{"bad restart", "services: {web: {image: x, restart: sometimes}}", `invalid restart "sometimes"`},
		{"count on always", "services: {web: {image: x, restart: 'always:3'}}", "only on-failure takes a count"},
		{"bad hypervisor", "services: {web: {image: x, hypervisor: qemu}}", `invalid hypervisor "qemu"`},
		{"two probes", "services: {web: {image: x, healthcheck: {http: '80', tcp: 80}}}", "exactly one of test, http and tcp"},
		{"disabled with settings", "services: {web: {image: x, healthcheck: {disable: true, retries: 3}}}",
			"a disabled check takes no other settings"},
		{"disabled with a test", "services: {web: {image: x, healthcheck: {disable: true, test: exit 0}}}",
			"a disabled check takes no other settings"},
		{"bad test", `services: {web: {image: x, healthcheck: {test: ["RUN", "x"]}}}`, "must start with CMD"},
		{"reserved label", "services: {web: {image: x, labels: {dicer.compose.project: other}}}", "reserved for dicer compose"},
		{"missing variable", "services: {web: {image: '${IMAGE:?name an image}'}}", "required variable IMAGE: name an image"},
		{"unclosed quote", `services: {web: {image: x, command: "echo 'hi"}}`, "unterminated"},
		{"tmpfs options", "services: {web: {image: x, tmpfs: ['/run:size=64m']}}", "takes no options"},
	}

	for _, tt := range tests {
		_, err := load(t, "shop", tt.compose, nil)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: Load = %v, want an error saying %q", tt.name, err, tt.want)
		}
	}
}

func TestOrder(t *testing.T) {
	p := mustLoad(t, `
services:
  web: {image: x, depends_on: [api, cache]}
  api: {image: x, depends_on: {db: {condition: service_healthy}}}
  db: {image: x}
  cache: {image: x}
  worker: {image: x, depends_on: [db]}
`, nil)

	names := func(services []*Service) []string {
		out := make([]string, 0, len(services))
		for _, s := range services {
			out = append(out, s.Name)
		}
		return out
	}

	all, err := p.Order()
	if err != nil {
		t.Fatal(err)
	}
	got := names(all)
	if len(got) != 5 {
		t.Fatalf("Order() = %q, want every service", got)
	}
	before := func(a, b string) bool { return slices.Index(got, a) < slices.Index(got, b) }
	for _, pair := range [][2]string{{"db", "api"}, {"api", "web"}, {"cache", "web"}, {"db", "worker"}} {
		if !before(pair[0], pair[1]) {
			t.Errorf("Order() = %q: %s should come before %s", got, pair[0], pair[1])
		}
	}

	// Naming a service brings what it depends on with it.
	web, err := p.Order("web")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(web); !slices.Equal(got, []string{"db", "api", "cache", "web"}) {
		t.Errorf("Order(web) = %q, want web and everything it depends on", got)
	}

	// Selecting does not.
	selected, err := p.Select("web", "db")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(selected); !slices.Equal(got, []string{"db", "web"}) {
		t.Errorf("Select(web, db) = %q, want just those, in order", got)
	}

	if _, err := p.Order("nope"); err == nil {
		t.Error("Order of an unknown service succeeded")
	}

	if dep := p.Services["api"].DependsOn; len(dep) != 1 || dep[0].Condition != ConditionHealthy {
		t.Errorf("api depends on %+v, want db, healthy", dep)
	}
	if dep := p.Services["web"].DependsOn; dep[0].Condition != ConditionStarted {
		t.Errorf("a listed dependency's condition = %q, want started", dep[0].Condition)
	}
}

func TestConfigHash(t *testing.T) {
	compose := "services: {web: {image: 'nginx:${TAG}', environment: {A: '1', B: '2'}}}"
	hash := func(tag string) string {
		p := mustLoad(t, compose, map[string]string{"TAG": tag})
		return p.Services["web"].Instance.GetLabels()[LabelConfigHash]
	}

	if first, again := hash("1"), hash("1"); first != again {
		t.Error("the same definition hashes differently")
	}
	if hash("1") == hash("2") {
		t.Error("a changed image hashes the same")
	}

	p := mustLoad(t, compose, map[string]string{"TAG": "1"})
	req := p.Services["web"].Instance
	started, ok := proto.Clone(req).(*dicerdv1.CreateInstanceRequest)
	if !ok {
		t.Fatal("clone of a CreateInstanceRequest is not one")
	}
	started.Start = true
	if ConfigHash(started) != req.GetLabels()[LabelConfigHash] {
		t.Error("starting the instance changes its hash")
	}
}

func TestServiceFor(t *testing.T) {
	p := mustLoad(t, "services: {web: {image: nginx}}", nil)

	ours := &dicerdv1.Instance{Labels: map[string]string{LabelProject: "shop", LabelService: "web"}}
	if s, ok := p.ServiceFor(ours); !ok || s.Name != "web" {
		t.Errorf("ServiceFor(ours) = %v, %v", s, ok)
	}

	for _, labels := range []map[string]string{
		{LabelProject: "other", LabelService: "web"},
		{LabelProject: "shop", LabelService: "gone"},
		nil,
	} {
		if _, ok := p.ServiceFor(&dicerdv1.Instance{Labels: labels}); ok {
			t.Errorf("ServiceFor(%v) found a service", labels)
		}
	}
}
