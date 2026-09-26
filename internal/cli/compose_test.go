// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/konradasb/dicer/internal/compose"
	"github.com/konradasb/dicer/internal/errdefs"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// composeDaemon is fakeInstanceDaemon with networks and volumes, keeping
// the whole of each instance's definition, as dicer compose needs.
type composeDaemon struct {
	*fakeInstanceDaemon

	networkNames map[string]bool
	volumeNames  map[string]bool

	// healthyAfter is how many looks at a checked instance pass before it
	// is healthy, and looks counts them.
	healthyAfter int
	looks        map[string]int
	// exits says what a started instance's workload exits with, at once,
	// by name.
	exits map[string]int32
}

func newComposeDaemon(instances ...*dicerdv1.Instance) *composeDaemon {
	d := &composeDaemon{
		fakeInstanceDaemon: newFakeInstanceDaemon(instances...),
		networkNames:       make(map[string]bool),
		volumeNames:        make(map[string]bool),
		looks:              make(map[string]int),
		exits:              make(map[string]int32),
	}
	d.cached["nginx:1.27"] = true
	d.cached["postgres:17"] = true
	return d
}

// CreateInstance keeps the definition, and starts the instance if asked:
// one with a health check starts out with no verdict, and one given an exit
// code ends at once, with its console saying so.
func (d *composeDaemon) CreateInstance(
	_ context.Context, req *dicerdv1.CreateInstanceRequest,
) (*dicerdv1.Instance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.instances[req.GetName()]; ok {
		return nil, errdefs.Exists("instance %q already exists", req.GetName())
	}
	d.record("create " + req.GetName())
	d.created = req

	inst := &dicerdv1.Instance{
		Id: "id-" + req.GetName(), Name: req.GetName(), ImageRef: req.GetImageRef(), State: stateStopped,
		Labels: req.GetLabels(), NetworkName: req.GetNetworkName(), Mounts: req.GetMounts(),
		Ports: req.GetPorts(), HealthCheck: req.GetHealthCheck(), Env: req.GetEnv(),
	}
	d.instances[inst.GetName()] = inst
	if req.GetStart() {
		d.start(inst)
	}
	return reply(inst, nil)
}

// start runs an instance, as the daemon would.
func (d *composeDaemon) start(inst *dicerdv1.Instance) {
	name := inst.GetName()
	inst.State, inst.Ip, inst.ExitCode = stateRunning, "10.0.0.9", nil
	d.ran[name] = true
	d.console[name] = "booted " + name + "\r\n"
	if inst.GetHealthCheck() != nil {
		inst.Health = &dicerdv1.Health{Status: healthStarting}
	}
	if code, ok := d.exits[name]; ok {
		inst.State, inst.ExitCode = stateStopped, &code
		d.console[name] += "done\n"
	}
}

func (d *composeDaemon) StartInstance(
	_ context.Context, req *dicerdv1.StartInstanceRequest,
) (*dicerdv1.Instance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	inst, err := d.get(req.GetName())
	if err != nil {
		return nil, err
	}
	d.record("start " + req.GetName())
	d.start(inst)
	return reply(inst, nil)
}

// GetInstance makes a checked instance healthy once it has been looked at
// healthyAfter times.
func (d *composeDaemon) GetInstance(_ context.Context, req *dicerdv1.GetInstanceRequest) (*dicerdv1.Instance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	inst, err := d.get(req.GetName())
	if err != nil {
		return nil, err
	}
	if inst.GetHealth().GetStatus() == healthStarting {
		d.looks[inst.GetName()]++
		if d.looks[inst.GetName()] > d.healthyAfter {
			inst.Health.Status = healthHealthy
			d.record("healthy " + inst.GetName())
		}
	}
	return reply(inst, nil)
}

func (d *composeDaemon) GetNetwork(_ context.Context, req *dicerdv1.GetNetworkRequest) (*dicerdv1.Network, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.networkNames[req.GetName()] {
		return nil, errdefs.NotFound("no network %q", req.GetName())
	}
	return &dicerdv1.Network{Name: req.GetName()}, nil
}

func (d *composeDaemon) CreateNetwork(_ context.Context, req *dicerdv1.CreateNetworkRequest) (*dicerdv1.Network, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.record("create network " + req.GetName())
	d.networkNames[req.GetName()] = true
	return &dicerdv1.Network{Name: req.GetName(), Subnet: req.GetSubnet()}, nil
}

func (d *composeDaemon) DeleteNetwork(_ context.Context, req *dicerdv1.DeleteNetworkRequest) (*emptypb.Empty, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.networkNames[req.GetName()] {
		return nil, errdefs.NotFound("no network %q", req.GetName())
	}
	d.record("delete network " + req.GetName())
	delete(d.networkNames, req.GetName())
	return &emptypb.Empty{}, nil
}

func (d *composeDaemon) GetVolume(_ context.Context, req *dicerdv1.GetVolumeRequest) (*dicerdv1.Volume, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.volumeNames[req.GetName()] {
		return nil, errdefs.NotFound("no volume %q", req.GetName())
	}
	return &dicerdv1.Volume{Name: req.GetName()}, nil
}

func (d *composeDaemon) CreateVolume(_ context.Context, req *dicerdv1.CreateVolumeRequest) (*dicerdv1.Volume, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.record("create volume " + req.GetName())
	d.volumeNames[req.GetName()] = true
	return &dicerdv1.Volume{Name: req.GetName(), SizeBytes: req.GetSizeBytes()}, nil
}

func (d *composeDaemon) DeleteVolume(_ context.Context, req *dicerdv1.DeleteVolumeRequest) (*emptypb.Empty, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.volumeNames[req.GetName()] {
		return nil, errdefs.NotFound("no volume %q", req.GetName())
	}
	d.record("delete volume " + req.GetName())
	delete(d.volumeNames, req.GetName())
	return &emptypb.Empty{}, nil
}

// calledWith returns the calls d has recorded, in order.
func (d *composeDaemon) calledWith() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.calls)
}

// serveComposeDaemon serves d on a socket and aims dicer compose at it.
func serveComposeDaemon(t *testing.T, d *composeDaemon) {
	t.Helper()
	isolateConfig(t)

	dir, err := os.MkdirTemp("", "dicer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	socket := filepath.Join(dir, "d.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}

	server := newTestServer()
	dicerdv1.RegisterDaemonServiceServer(server, d)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	t.Setenv(remoteEnv, "unix://"+socket)

	poll := composePollInterval
	composePollInterval = time.Millisecond
	t.Cleanup(func() { composePollInterval = poll })
}

// composeProject writes a compose file into a directory named shop, and
// returns the file's path.
func composeProject(t *testing.T, content string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "shop")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// runCompose runs dicer compose on file, returning what it wrote.
func runCompose(t *testing.T, file string, args ...string) (string, error) {
	t.Helper()
	t.Setenv(composeFileEnv, "")
	t.Setenv(composeProjectEnv, "")

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"compose", "-f", file}, args...))

	err := cmd.ExecuteContext(t.Context())
	return out.String(), err
}

// shopFile is a small project: a database others wait on to be healthy, an
// API, and a web front end, with a network and a volume of their own.
const shopFile = `
services:
  web:
    image: nginx:1.27
    ports: ["8080:80"]
    depends_on: [api]
  api:
    image: nginx:1.27
    environment: {DB: shop-db}
    networks: [backend]
    depends_on:
      db: {condition: service_healthy}
  db:
    image: postgres:17
    networks: [backend]
    volumes: [data:/var/lib/postgresql/data]
    healthcheck: {test: pg_isready}
networks:
  backend: {subnet: 172.30.0.0/24}
volumes:
  data: {size: 1GiB}
`

func TestComposeUpCreatesInDependencyOrder(t *testing.T) {
	d := newComposeDaemon()
	d.healthyAfter = 3
	serveComposeDaemon(t, d)
	// A second service waits on the database, as api does.
	file := composeProject(t, strings.Replace(shopFile, "\nnetworks:\n", `
  worker:
    image: nginx:1.27
    networks: [backend]
    depends_on: {db: {condition: service_healthy}}
networks:
`, 1))

	out, err := runCompose(t, file, "up", "-d")
	if err != nil {
		t.Fatalf("up: %v\n%s", err, out)
	}

	calls := d.calledWith()
	if want := []string{"create network shop-backend", "create volume shop-data", "create shop-db"}; !slices.Equal(calls[:3], want) {
		t.Errorf("first calls = %q, want %q", calls[:3], want)
	}
	before := func(a, b string) bool {
		i, j := slices.Index(calls, a), slices.Index(calls, b)
		return i >= 0 && j >= 0 && i < j
	}
	// api waits for db to be healthy, not only started; web for api.
	if !before("healthy shop-db", "create shop-api") || !before("create shop-api", "create shop-web") {
		t.Errorf("calls = %q: want db healthy, then api, then web", calls)
	}

	// However many wait on it, a wait is announced once.
	if !before("healthy shop-db", "create shop-worker") {
		t.Errorf("calls = %q: want worker started once db was healthy", calls)
	}
	if n := strings.Count(out, "Waiting for shop-db to be healthy"); n != 1 {
		t.Errorf("the wait for db was announced %d times, want once:\n%s", n, out)
	}
	for _, want := range []string{
		"Network shop-backend created (172.30.0.0/24)",
		"Volume shop-data created (1 GiB)",
		"Waiting for shop-db to be healthy",
		"Instance shop-db is healthy",
		"Instance shop-web started in",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	d.mu.Lock()
	api := d.instances["shop-api"]
	d.mu.Unlock()
	if api.GetNetworkName() != "shop-backend" || api.GetEnv()["DB"] != "shop-db" {
		t.Errorf("api = %v, want it on the project's network, with its environment", api)
	}
	if api.GetLabels()[compose.LabelProject] != "shop" || api.GetLabels()[compose.LabelService] != "api" ||
		api.GetLabels()[compose.LabelConfigHash] == "" {
		t.Errorf("api's labels = %v, want the project's", api.GetLabels())
	}
}

func TestComposeUpOnlyChangesWhatChanged(t *testing.T) {
	d := newComposeDaemon()
	serveComposeDaemon(t, d)
	file := composeProject(t, shopFile)

	if out, err := runCompose(t, file, "up", "-d"); err != nil {
		t.Fatalf("up: %v\n%s", err, out)
	}

	// Again, with nothing changed: nothing is created.
	d.calls = nil
	out, err := runCompose(t, file, "up", "-d")
	if err != nil {
		t.Fatalf("second up: %v\n%s", err, out)
	}
	for _, call := range d.calledWith() {
		if strings.HasPrefix(call, "create") || strings.HasPrefix(call, "delete") {
			t.Errorf("an unchanged project made call %q", call)
		}
	}
	if strings.Count(out, "is up to date") != 3 {
		t.Errorf("output = %q, want each instance up to date", out)
	}

	// A stopped instance is started again, not recreated.
	if _, err := d.StopInstance(t.Context(), &dicerdv1.StopInstanceRequest{Name: "shop-web"}); err != nil {
		t.Fatal(err)
	}
	d.calls = nil
	if out, err := runCompose(t, file, "up", "-d"); err != nil {
		t.Fatalf("up after a stop: %v\n%s", err, out)
	}
	if calls := d.calledWith(); !slices.Equal(calls, []string{"start shop-web"}) {
		t.Errorf("calls = %q, want web started", calls)
	}

	// A changed service is recreated, and only it.
	if err := os.WriteFile(file, []byte(strings.Replace(shopFile, "DB: shop-db", "DB: other", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	d.calls = nil
	out, err = runCompose(t, file, "up", "-d")
	if err != nil {
		t.Fatalf("up after a change: %v\n%s", err, out)
	}
	if calls := d.calledWith(); !slices.Equal(calls, []string{"delete shop-api", "create shop-api"}) {
		t.Errorf("calls = %q, want api recreated and nothing else", calls)
	}
	if !strings.Contains(out, "Instance shop-api recreated in") {
		t.Errorf("output = %q, want api's recreation reported", out)
	}

	// --no-recreate leaves even a changed service alone; --force-recreate
	// recreates even unchanged ones.
	if err := os.WriteFile(file, []byte(strings.Replace(shopFile, "DB: shop-db", "DB: third", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	d.calls = nil
	if out, err := runCompose(t, file, "up", "-d", "--no-recreate"); err != nil {
		t.Fatalf("up --no-recreate: %v\n%s", err, out)
	}
	if calls := d.calledWith(); slices.Contains(calls, "create shop-api") {
		t.Errorf("--no-recreate recreated api: %q", calls)
	}
	d.calls = nil
	if out, err := runCompose(t, file, "up", "-d", "--force-recreate", "web"); err != nil {
		t.Fatalf("up --force-recreate: %v\n%s", err, out)
	}
	for _, want := range []string{"create shop-db", "create shop-api", "create shop-web"} {
		if !slices.Contains(d.calledWith(), want) {
			t.Errorf("--force-recreate web did not %s, which web depends on: %q", want, d.calledWith())
		}
	}
}

func TestComposeRenamedService(t *testing.T) {
	d := newComposeDaemon()
	serveComposeDaemon(t, d)
	before := `
services:
  web: {image: nginx:1.27, ports: ["8080:80"], networks: [lan]}
networks:
  lan: {subnet: 172.30.0.0/24}
`
	after := strings.Replace(before, "image: nginx:1.27,", "image: nginx:1.27, container_name: api,", 1)
	file := composeProject(t, before)
	if out, err := runCompose(t, file, "up", "-d"); err != nil {
		t.Fatalf("up: %v\n%s", err, out)
	}

	// Renamed, the service's old instance is still found by the commands
	// until up replaces it.
	if err := os.WriteFile(file, []byte(after), 0o600); err != nil {
		t.Fatal(err)
	}
	d.calls = nil
	if out, err := runCompose(t, file, "stop"); err != nil {
		t.Fatalf("stop: %v\n%s", err, out)
	}
	if calls := d.calledWith(); !slices.Equal(calls, []string{"stop shop-web"}) {
		t.Errorf("stop calls = %q, want the old instance stopped", calls)
	}

	// --no-recreate keeps the old instance, and starts it.
	d.calls = nil
	if out, err := runCompose(t, file, "up", "-d", "--no-recreate"); err != nil {
		t.Fatalf("up --no-recreate: %v\n%s", err, out)
	}
	if calls := d.calledWith(); !slices.Equal(calls, []string{"start shop-web"}) {
		t.Errorf("up --no-recreate calls = %q, want the old instance started, and nothing created", calls)
	}

	// up replaces it, deleting the old one before creating the new, which
	// wants its port.
	d.calls = nil
	out, err := runCompose(t, file, "up", "-d")
	if err != nil {
		t.Fatalf("up: %v\n%s", err, out)
	}
	if calls := d.calledWith(); !slices.Equal(calls, []string{"delete shop-web", "create api"}) {
		t.Errorf("up calls = %q, want the old instance replaced by the new", calls)
	}
	if !strings.Contains(out, "Instance shop-web deleted: service web is now instance api") {
		t.Errorf("output = %q, want the replacement explained", out)
	}

	// Renamed back without an up, down deletes the instance under its old
	// name too, and so can delete the network it was on.
	if err := os.WriteFile(file, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	d.calls = nil
	out, err = runCompose(t, file, "down")
	if err != nil {
		t.Fatalf("down: %v\n%s", err, out)
	}
	if calls := d.calledWith(); !slices.Equal(calls, []string{"delete api", "delete network shop-lan"}) {
		t.Errorf("down calls = %q, want the renamed instance and the network deleted\n%s", calls, out)
	}
}

func TestComposeUpRefusesAnInstanceNotItsOwn(t *testing.T) {
	d := newComposeDaemon(&dicerdv1.Instance{Name: "shop-web", State: stateRunning})
	serveComposeDaemon(t, d)
	file := composeProject(t, "services: {web: {image: nginx:1.27}}")

	_, err := runCompose(t, file, "up", "-d")
	if err == nil || !strings.Contains(err.Error(), "instance shop-web already exists and is not this project's") {
		t.Errorf("up = %v, want it to refuse to take over another instance", err)
	}
	if slices.Contains(d.calledWith(), "delete shop-web") {
		t.Error("up deleted an instance that was not the project's")
	}
}

func TestComposeUpWaitsForCompletion(t *testing.T) {
	file := composeProject(t, `
services:
  migrate:
    image: postgres:17
    command: ./migrate up
  app:
    image: nginx:1.27
    depends_on:
      migrate: {condition: service_completed_successfully}
`)

	d := newComposeDaemon()
	d.exits["shop-migrate"] = 0
	serveComposeDaemon(t, d)

	out, err := runCompose(t, file, "up", "-d")
	if err != nil {
		t.Fatalf("up: %v\n%s", err, out)
	}
	if calls := d.calledWith(); !slices.Equal(calls, []string{"create shop-migrate", "create shop-app"}) {
		t.Errorf("calls = %q, want app started after migrate finished\n%s", calls, out)
	}

	failing := newComposeDaemon()
	failing.exits["shop-migrate"] = 3
	serveComposeDaemon(t, failing)

	_, err = runCompose(t, file, "up", "-d")
	if err == nil || !strings.Contains(err.Error(), "service app was not started: instance shop-migrate exited with code 3") {
		t.Errorf("up = %v, want it to say migrate failed and app was not started", err)
	}
	if slices.Contains(failing.calledWith(), "create shop-app") {
		t.Error("app was started although migrate failed")
	}
}

func TestComposeUpHealthyNeedsAHealthCheck(t *testing.T) {
	d := newComposeDaemon()
	serveComposeDaemon(t, d)
	file := composeProject(t, `
services:
  db: {image: postgres:17}
  app:
    image: nginx:1.27
    depends_on: {db: {condition: service_healthy}}
`)

	_, err := runCompose(t, file, "up", "-d")
	if err == nil || !strings.Contains(err.Error(), "service db has no health check to wait for") {
		t.Errorf("up = %v, want it to say db has no health check", err)
	}
}

func TestComposeUpExternalResourcesMustExist(t *testing.T) {
	d := newComposeDaemon()
	serveComposeDaemon(t, d)
	file := composeProject(t, `
services: {web: {image: nginx:1.27, networks: [lan]}}
networks: {lan: {external: true}}
`)

	_, err := runCompose(t, file, "up", "-d")
	if err == nil || !strings.Contains(err.Error(), "external network lan does not exist") {
		t.Errorf("up = %v, want it to say the external network is missing", err)
	}
	if slices.Contains(d.calledWith(), "create network lan") {
		t.Error("up created an external network")
	}
}

func TestComposeUpPulls(t *testing.T) {
	d := newComposeDaemon()
	serveComposeDaemon(t, d)
	file := composeProject(t, "services: {web: {image: 'redis:7'}, cache: {image: 'redis:7'}}")

	if out, err := runCompose(t, file, "up", "-d", "--pull", "never"); err != nil {
		t.Fatalf("up --pull never: %v\n%s", err, out)
	}
	if slices.ContainsFunc(d.calledWith(), func(c string) bool { return strings.HasPrefix(c, "pull") }) {
		t.Errorf("--pull never pulled: %q", d.calledWith())
	}

	d.calls = nil
	if out, err := runCompose(t, file, "up", "-d", "--pull", "always"); err != nil {
		t.Fatalf("up --pull always: %v\n%s", err, out)
	}
	pulls := slices.DeleteFunc(d.calledWith(), func(c string) bool { return !strings.HasPrefix(c, "pull") })
	if !slices.Equal(pulls, []string{"pull redis:7"}) {
		t.Errorf("pulls = %q, want the one image pulled once", pulls)
	}

	if _, err := runCompose(t, file, "up", "--pull", "sometimes"); err == nil {
		t.Error("an invalid --pull was accepted")
	}
}

func TestComposeUpAttached(t *testing.T) {
	d := newComposeDaemon()
	d.exits["shop-job"] = 0
	d.exits["shop-other"] = 2
	serveComposeDaemon(t, d)
	file := composeProject(t, "services: {job: {image: nginx:1.27}, other: {image: nginx:1.27}}")

	out, err := runCompose(t, file, "up")
	if err != nil {
		t.Fatalf("up: %v\n%s", err, out)
	}

	for _, want := range []string{
		"shop-job   | booted shop-job\n",
		"shop-job   | done\n",
		"shop-other | booted shop-other\n",
		"Instance shop-job exited with code 0",
		"Instance shop-other exited with code 2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestComposeUpOrphans(t *testing.T) {
	orphan := &dicerdv1.Instance{
		Name: "shop-old", State: stateRunning,
		Labels: map[string]string{compose.LabelProject: "shop", compose.LabelService: "old"},
	}
	d := newComposeDaemon(orphan)
	serveComposeDaemon(t, d)
	file := composeProject(t, "services: {web: {image: nginx:1.27}}")

	out, err := runCompose(t, file, "up", "-d")
	if err != nil {
		t.Fatalf("up: %v\n%s", err, out)
	}
	if !strings.Contains(out, "services no longer in the file: shop-old") {
		t.Errorf("output = %q, want the orphan pointed out", out)
	}
	if slices.Contains(d.calledWith(), "delete shop-old") {
		t.Error("an orphan was deleted without --remove-orphans")
	}

	if out, err := runCompose(t, file, "up", "-d", "--remove-orphans"); err != nil {
		t.Fatalf("up --remove-orphans: %v\n%s", err, out)
	}
	if !slices.Contains(d.calledWith(), "delete shop-old") {
		t.Errorf("calls = %q, want the orphan deleted", d.calledWith())
	}
}

func TestComposeDown(t *testing.T) {
	d := newComposeDaemon()
	serveComposeDaemon(t, d)
	file := composeProject(t, shopFile)

	if out, err := runCompose(t, file, "up", "-d"); err != nil {
		t.Fatalf("up: %v\n%s", err, out)
	}
	// Another project's instance is left alone.
	d.instances["elsewhere"] = &dicerdv1.Instance{
		Name: "elsewhere", State: stateRunning, Labels: map[string]string{compose.LabelProject: "other"},
	}

	d.calls = nil
	out, err := runCompose(t, file, "down")
	if err != nil {
		t.Fatalf("down: %v\n%s", err, out)
	}
	want := []string{"delete shop-web", "delete shop-api", "delete shop-db", "delete network shop-backend"}
	if calls := d.calledWith(); !slices.Equal(calls, want) {
		t.Errorf("calls = %q, want %q: instances in reverse order, then the network, and the volume kept", calls, want)
	}
	if _, ok := d.instances["elsewhere"]; !ok {
		t.Error("down deleted another project's instance")
	}

	// Down again finds nothing to do; -v deletes the volume.
	d.calls = nil
	if out, err := runCompose(t, file, "down", "-v"); err != nil {
		t.Fatalf("down -v: %v\n%s", err, out)
	}
	if calls := d.calledWith(); !slices.Equal(calls, []string{"delete volume shop-data"}) {
		t.Errorf("calls = %q, want only the volume deleted", calls)
	}
}

func TestComposePsAndLogs(t *testing.T) {
	d := newComposeDaemon(&dicerdv1.Instance{Name: "unrelated", State: stateRunning})
	serveComposeDaemon(t, d)
	file := composeProject(t, shopFile)

	if out, err := runCompose(t, file, "up", "-d"); err != nil {
		t.Fatalf("up: %v\n%s", err, out)
	}

	out, err := runCompose(t, file, "ps", "--format", "{{.Name}} {{.Service}}")
	if err != nil {
		t.Fatalf("ps: %v\n%s", err, out)
	}
	if want := "shop-api api\nshop-db db\nshop-web web\n"; out != want {
		t.Errorf("ps = %q, want %q: the project's instances, and nothing else", out, want)
	}

	out, err = runCompose(t, file, "ps", "-q", "db")
	if err != nil || out != "shop-db\n" {
		t.Errorf("ps -q db = %q, %v; want shop-db", out, err)
	}
	if _, err := runCompose(t, file, "ps", "nope"); err == nil {
		t.Error("ps of an unknown service succeeded")
	}

	out, err = runCompose(t, file, "logs", "db", "web")
	if err != nil {
		t.Fatalf("logs: %v\n%s", err, out)
	}
	if want := "shop-db  | booted shop-db\n"; !strings.Contains(out, want) || !strings.Contains(out, "shop-web | booted shop-web\n") {
		t.Errorf("logs = %q, want each line marked with its instance", out)
	}
	if strings.Contains(out, "shop-api") {
		t.Errorf("logs = %q, want only the services named", out)
	}

	out, err = runCompose(t, file, "logs", "--no-prefix", "db")
	if err != nil || out != "booted shop-db\n" {
		t.Errorf("logs --no-prefix = %q, %v", out, err)
	}
}

func TestComposeStopStartRestart(t *testing.T) {
	d := newComposeDaemon()
	serveComposeDaemon(t, d)
	file := composeProject(t, shopFile)

	if out, err := runCompose(t, file, "up", "-d"); err != nil {
		t.Fatalf("up: %v\n%s", err, out)
	}

	d.calls = nil
	if out, err := runCompose(t, file, "stop"); err != nil {
		t.Fatalf("stop: %v\n%s", err, out)
	}
	if want := []string{"stop shop-web", "stop shop-api", "stop shop-db"}; !slices.Equal(d.calledWith(), want) {
		t.Errorf("stop calls = %q, want %q", d.calledWith(), want)
	}

	d.calls = nil
	if out, err := runCompose(t, file, "start"); err != nil {
		t.Fatalf("start: %v\n%s", err, out)
	}
	if want := []string{"start shop-db", "start shop-api", "start shop-web"}; !slices.Equal(d.calledWith(), want) {
		t.Errorf("start calls = %q, want %q", d.calledWith(), want)
	}

	d.calls = nil
	if out, err := runCompose(t, file, "restart", "api"); err != nil {
		t.Fatalf("restart: %v\n%s", err, out)
	}
	if want := []string{"stop shop-api", "start shop-api"}; !slices.Equal(d.calledWith(), want) {
		t.Errorf("restart api calls = %q, want %q", d.calledWith(), want)
	}

	if out, err := runCompose(t, file, "down"); err != nil {
		t.Fatalf("down: %v\n%s", err, out)
	}
	if _, err := runCompose(t, file, "start", "web"); err == nil ||
		!strings.Contains(err.Error(), "service web has no instance: create it with dicer compose up") {
		t.Errorf("start with no instance = %v, want it to point at up", err)
	}
}

func TestComposeConfig(t *testing.T) {
	isolateConfig(t)
	file := composeProject(t, `
x-base: &base {image: "nginx:${TAG:-1.27}"}
services:
  web: {<<: *base}
  db: {image: postgres:17}
volumes: {data: {}}
`)

	out, err := runCompose(t, file, "config", "--services")
	if err != nil || out != "db\nweb\n" {
		t.Errorf("config --services = %q, %v", out, err)
	}
	out, err = runCompose(t, file, "config", "--volumes")
	if err != nil || out != "data\n" {
		t.Errorf("config --volumes = %q, %v", out, err)
	}

	out, err = runCompose(t, file, "config", "--interpolate")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "nginx:1.27") || strings.Contains(out, "${") ||
		strings.Contains(out, "x-base") || strings.Contains(out, "<<") {
		t.Errorf("config --interpolate = %q, want it resolved: substituted, merged, without extensions", out)
	}

	bad := composeProject(t, "services: {web: {image: nginx, build: .}}")
	if _, err := runCompose(t, bad, "config", "-q"); err == nil || !strings.Contains(err.Error(), "build is not supported") {
		t.Errorf("config -q of a bad file = %v, want the problem", err)
	}
}

func TestComposeConfigKeepsValuesOut(t *testing.T) {
	isolateConfig(t)
	file := composeProject(t, `
x-db: &db {image: postgres:17}
services:
  db:
    <<: *db
    environment: {POSTGRES_PASSWORD: "${DB_PASSWORD:?set it in .env}"}
`)
	env := filepath.Join(filepath.Dir(file), ".env")
	if err := os.WriteFile(env, []byte("DB_PASSWORD=hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// By default the variable is shown, not the password, though the file is
	// still resolved otherwise.
	out, err := runCompose(t, file, "config")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "hunter2") || !strings.Contains(out, "${DB_PASSWORD:?set it in .env}") {
		t.Errorf("config = %q, want the variable as written and not its value", out)
	}
	if !strings.Contains(out, "image: postgres:17") || strings.Contains(out, "x-db") {
		t.Errorf("config = %q, want its anchor merged and its extension left out", out)
	}

	out, err = runCompose(t, file, "config", "--interpolate")
	if err != nil || !strings.Contains(out, "hunter2") {
		t.Errorf("config --interpolate = %q, %v; want the value", out, err)
	}

	// The variable is still needed for the file to be valid.
	if err := os.Remove(env); err != nil {
		t.Fatal(err)
	}
	if _, err := runCompose(t, file, "config"); err == nil || !strings.Contains(err.Error(), "set it in .env") {
		t.Errorf("config without the variable = %v, want the file refused", err)
	}
}

func TestComposeProjectName(t *testing.T) {
	d := newComposeDaemon()
	serveComposeDaemon(t, d)
	file := composeProject(t, "services: {web: {image: nginx:1.27}}")

	if out, err := runCompose(t, file, "-p", "staging", "up", "-d"); err != nil {
		t.Fatalf("up -p: %v\n%s", err, out)
	}
	if !slices.Contains(d.calledWith(), "create staging-web") {
		t.Errorf("calls = %q, want the instance named for -p", d.calledWith())
	}

	// A request made from the file is not changed by being sent.
	d.mu.Lock()
	created, ok := proto.Clone(d.created).(*dicerdv1.CreateInstanceRequest)
	d.mu.Unlock()
	if !ok {
		t.Fatal("clone of a CreateInstanceRequest is not one")
	}
	if !created.GetStart() || created.GetLabels()[compose.LabelProject] != "staging" {
		t.Errorf("created = %v, want it started and labelled for staging", created)
	}
}
