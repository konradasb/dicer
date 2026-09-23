// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"gopkg.in/yaml.v3"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/grpcapi"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// fakeInstanceDaemon keeps instances in memory and moves them between states
// as the daemon would, recording the requests that change them.
type fakeInstanceDaemon struct {
	dicerdv1.UnimplementedDaemonServiceServer

	mu        sync.Mutex
	instances map[string]*dicerdv1.Instance
	calls     []string
	created   *dicerdv1.CreateInstanceRequest
	updated   *dicerdv1.UpdateInstanceRequest

	// cached are the images already on the host: pulling one of them
	// downloads nothing.
	cached map[string]bool
	// kernels and networks are what the host has, and host what
	// GetHostInfo says of it.
	kernels  []string
	networks []string
	host     *dicerdv1.GetHostInfoResponse
	// hostDelay holds up GetHostInfo, to trip --timeout.
	hostDelay time.Duration

	// events is what GetEvents streams once it has caught up. A test that
	// waits on an instance pushes what happens to it here.
	events chan *dicerdv1.Event
}

func newFakeInstanceDaemon(instances ...*dicerdv1.Instance) *fakeInstanceDaemon {
	d := &fakeInstanceDaemon{
		instances: make(map[string]*dicerdv1.Instance),
		cached:    make(map[string]bool),
		host:      &dicerdv1.GetHostInfoResponse{Version: "v9.9.9", Hostname: "compute-1"},
		events:    make(chan *dicerdv1.Event, 8),
	}
	for _, inst := range instances {
		d.instances[inst.GetName()] = inst
	}
	return d
}

func (d *fakeInstanceDaemon) record(call string) {
	d.calls = append(d.calls, call)
}

func (d *fakeInstanceDaemon) get(name string) (*dicerdv1.Instance, error) {
	inst, ok := d.instances[name]
	if !ok {
		return nil, errdefs.NotFound("no instance %q", name)
	}
	return inst, nil
}

// reply copies an instance for the wire. gRPC marshals what a handler
// returns after the handler has returned, so the lock is long released by
// then; handing out the stored message would let a test that moves an
// instance on -- stops, say -- write it while it is being marshalled.
func reply(inst *dicerdv1.Instance, err error) (*dicerdv1.Instance, error) {
	if err != nil {
		return nil, err
	}

	clone, ok := proto.Clone(inst).(*dicerdv1.Instance)
	if !ok {
		return nil, errors.New("clone instance")
	}

	return clone, nil
}

// setState moves an instance to state if it is in one of from.
func (d *fakeInstanceDaemon) setState(
	call, name string, state dicerdv1.InstanceState, from ...dicerdv1.InstanceState,
) (*dicerdv1.Instance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	inst, err := d.get(name)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(from, inst.GetState()) {
		return nil, errdefs.InvalidState("instance %q is %s", name, enumName(inst.GetState()))
	}

	d.record(call + " " + name)
	inst.State = state
	return reply(inst, nil)
}

func (d *fakeInstanceDaemon) ListInstances(
	context.Context, *dicerdv1.ListInstancesRequest,
) (*dicerdv1.ListInstancesResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	resp := &dicerdv1.ListInstancesResponse{}
	for _, name := range slices.Sorted(maps.Keys(d.instances)) {
		inst, err := reply(d.instances[name], nil)
		if err != nil {
			return nil, err
		}
		resp.Instances = append(resp.Instances, inst)
	}
	return resp, nil
}

func (d *fakeInstanceDaemon) GetInstance(_ context.Context, req *dicerdv1.GetInstanceRequest) (*dicerdv1.Instance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return reply(d.get(req.GetName()))
}

func (d *fakeInstanceDaemon) StartInstance(
	_ context.Context, req *dicerdv1.StartInstanceRequest,
) (*dicerdv1.Instance, error) {
	return d.setState("start", req.GetName(), stateRunning, stateStopped, stateFailed)
}

func (d *fakeInstanceDaemon) StopInstance(_ context.Context, req *dicerdv1.StopInstanceRequest) (*dicerdv1.Instance, error) {
	return d.setState("stop", req.GetName(), stateStopped, stateRunning, statePaused)
}

func (d *fakeInstanceDaemon) PauseInstance(
	_ context.Context, req *dicerdv1.PauseInstanceRequest,
) (*dicerdv1.Instance, error) {
	return d.setState("pause", req.GetName(), statePaused, stateRunning)
}

func (d *fakeInstanceDaemon) ResumeInstance(
	_ context.Context, req *dicerdv1.ResumeInstanceRequest,
) (*dicerdv1.Instance, error) {
	return d.setState("resume", req.GetName(), stateRunning, statePaused)
}

func (d *fakeInstanceDaemon) DeleteInstance(
	_ context.Context, req *dicerdv1.DeleteInstanceRequest,
) (*emptypb.Empty, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	inst, err := d.get(req.GetName())
	if err != nil {
		return nil, err
	}
	if inst.GetState() == stateRunning && !req.GetForce() {
		return nil, errdefs.InvalidState("instance %q is running", req.GetName())
	}
	d.record("delete " + req.GetName())
	delete(d.instances, req.GetName())
	return &emptypb.Empty{}, nil
}

func (d *fakeInstanceDaemon) CreateInstance(
	_ context.Context, req *dicerdv1.CreateInstanceRequest,
) (*dicerdv1.Instance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.created = req
	inst := &dicerdv1.Instance{Name: req.GetName(), ImageRef: req.GetImageRef(), State: stateStopped}
	if req.GetStart() {
		inst.State, inst.Ip = stateRunning, "10.0.0.9"
	}
	d.instances[inst.GetName()] = inst
	return reply(inst, nil)
}

func (d *fakeInstanceDaemon) UpdateInstance(
	_ context.Context, req *dicerdv1.UpdateInstanceRequest,
) (*dicerdv1.Instance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.updated = req
	return reply(d.get(req.GetName()))
}

// RenameInstance moves an instance to a new name, as the daemon does for a
// stopped one.
func (d *fakeInstanceDaemon) RenameInstance(
	_ context.Context, req *dicerdv1.RenameInstanceRequest,
) (*dicerdv1.Instance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	inst, err := d.get(req.GetName())
	if err != nil {
		return nil, err
	}
	if inst.GetState() != stateStopped {
		return nil, errdefs.InvalidState(
			"instance %q is %s", req.GetName(), enumName(inst.GetState()))
	}
	if _, taken := d.instances[req.GetNewName()]; taken {
		return nil, errdefs.Exists("instance %q already exists", req.GetNewName())
	}

	d.record("rename " + req.GetName() + " " + req.GetNewName())
	delete(d.instances, inst.GetName())
	inst.Name = req.GetNewName()
	d.instances[inst.GetName()] = inst

	return reply(inst, nil)
}

// GetEvents reports an empty history, says so, and then streams whatever the
// test pushes, which is the shape the daemon's own stream has.
func (d *fakeInstanceDaemon) GetEvents(
	req *dicerdv1.GetEventsRequest, stream grpc.ServerStreamingServer[dicerdv1.GetEventsResponse],
) error {
	if err := stream.Send(&dicerdv1.GetEventsResponse{CaughtUp: true}); err != nil {
		return err
	}
	if !req.GetFollow() {
		return nil
	}

	for {
		select {
		case e := <-d.events:
			if err := stream.Send(&dicerdv1.GetEventsResponse{Events: []*dicerdv1.Event{e}}); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}

// stops records an instance as stopped with the given status, and reports it
// as the daemon would.
func (d *fakeInstanceDaemon) stops(name string, exitCode int32) {
	d.mu.Lock()
	if inst, ok := d.instances[name]; ok {
		inst.State = stateStopped
		inst.ExitCode = &exitCode
	}
	d.mu.Unlock()

	d.events <- &dicerdv1.Event{
		Kind: dicerdv1.EventKind_EVENT_KIND_INSTANCE, Name: name, Action: dicerdv1.EventAction_EVENT_ACTION_EXITED,
		Attributes: map[string]string{"exit_code": strconv.Itoa(int(exitCode))},
	}
}

// stopsAndRemoves reports an instance as ended and deletes it, which is what
// the daemon does for one started with --rm.
func (d *fakeInstanceDaemon) stopsAndRemoves(name string, exitCode int32) {
	d.mu.Lock()
	delete(d.instances, name)
	d.mu.Unlock()

	d.events <- &dicerdv1.Event{
		Kind: dicerdv1.EventKind_EVENT_KIND_INSTANCE, Name: name, Action: dicerdv1.EventAction_EVENT_ACTION_EXITED,
		Attributes: map[string]string{"exit_code": strconv.Itoa(int(exitCode))},
	}
}

func (d *fakeInstanceDaemon) GetHostInfo(
	ctx context.Context, _ *dicerdv1.GetHostInfoRequest,
) (*dicerdv1.GetHostInfoResponse, error) {
	select {
	case <-time.After(d.hostDelay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return d.host, nil
}

// GetImage returns an image only if it is cached.
func (d *fakeInstanceDaemon) GetImage(_ context.Context, req *dicerdv1.GetImageRequest) (*dicerdv1.Image, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.cached[req.GetRef()] {
		return nil, errdefs.NotFound("no image %q", req.GetRef())
	}
	return &dicerdv1.Image{Name: req.GetRef(), Digest: "sha256:0123456789abcdef0123", SizeBytes: 64 << 20}, nil
}

// PullImage reports a download for an image not yet cached, and caches it.
func (d *fakeInstanceDaemon) PullImage(
	req *dicerdv1.PullImageRequest, stream grpc.ServerStreamingServer[dicerdv1.PullImageProgress],
) error {
	d.mu.Lock()
	d.record("pull " + req.GetRef())
	cached := d.cached[req.GetRef()]
	d.cached[req.GetRef()] = true
	d.mu.Unlock()

	progress := []*dicerdv1.PullImageProgress{{Stage: dicerdv1.PullStage_PULL_STAGE_RESOLVING}}
	if !cached {
		progress = append(progress,
			&dicerdv1.PullImageProgress{Stage: dicerdv1.PullStage_PULL_STAGE_DOWNLOADING, TotalBytes: 100},
			&dicerdv1.PullImageProgress{
				Stage: dicerdv1.PullStage_PULL_STAGE_DOWNLOADING, DownloadedBytes: 100, TotalBytes: 100,
			},
		)
	}
	progress = append(progress, &dicerdv1.PullImageProgress{
		Image: &dicerdv1.Image{Name: req.GetRef(), Digest: "sha256:0123456789abcdef0123", SizeBytes: 64 << 20},
	})

	for _, p := range progress {
		if err := stream.Send(p); err != nil {
			return err
		}
	}
	return nil
}

func (d *fakeInstanceDaemon) ListKernels(
	context.Context, *dicerdv1.ListKernelsRequest,
) (*dicerdv1.ListKernelsResponse, error) {
	resp := &dicerdv1.ListKernelsResponse{}
	for _, k := range d.kernels {
		resp.Kernels = append(resp.Kernels, &dicerdv1.Kernel{Name: k, Arch: dicerdv1.Architecture_ARCHITECTURE_X86_64})
	}
	return resp, nil
}

func (d *fakeInstanceDaemon) ListNetworks(
	context.Context, *dicerdv1.ListNetworksRequest,
) (*dicerdv1.ListNetworksResponse, error) {
	resp := &dicerdv1.ListNetworksResponse{}
	for _, n := range d.networks {
		resp.Networks = append(resp.Networks, &dicerdv1.Network{Name: n, Subnet: "172.20.0.0/16"})
	}
	return resp, nil
}

// GetResources answers with the same host testResources describes, written
// out as the daemon would send it. The fake is a daemon, so it speaks the
// wire format, which is what the CLI works with.
func (d *fakeInstanceDaemon) GetResources(
	context.Context, *dicerdv1.GetResourcesRequest,
) (*dicerdv1.GetResourcesResponse, error) {
	return testResources(), nil
}

// fakeInstances are what the tests start from: one of each state that
// matters, with labels and ports to filter and show.
func fakeInstances() []*dicerdv1.Instance {
	return []*dicerdv1.Instance{
		{
			Name: "web", ImageRef: "docker.io/library/nginx:1.27", State: stateRunning,
			NetworkName: "default", Ip: "10.0.0.5", Vcpus: 2, MemoryBytes: 1 << 30, DiskBytes: 10 << 30,
			Labels: map[string]string{"team": "web"},
			Env:    map[string]string{"B": "2", "A": "1"},
			Cmd:    []string{"nginx", "-g", "daemon off;"},
			Ports: []*dicerdv1.PortMapping{
				{HostPort: 8080, GuestPort: 80, Protocol: dicerdv1.Protocol_PROTOCOL_TCP},
				{HostIp: "10.1.0.1", HostPort: 5353, GuestPort: 53, Protocol: dicerdv1.Protocol_PROTOCOL_UDP},
			},
		},
		{
			Name: "db", ImageRef: "docker.io/library/postgres:17", State: stateStopped,
			NetworkName: "default", Labels: map[string]string{"team": "data"},
		},
		{Name: "cache", ImageRef: "docker.io/library/redis:7", State: statePaused, NetworkName: "backend"},
	}
}

// serveInstanceDaemon serves d on a socket and aims commands at it through
// $DICER_REMOTE, as a completion, which takes no --remote, needs.
func serveInstanceDaemon(t *testing.T, d *fakeInstanceDaemon) {
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
}

func TestPsIsInstanceList(t *testing.T) {
	serveInstanceDaemon(t, newFakeInstanceDaemon(fakeInstances()...))

	ps, err := run(t, "ps")
	if err != nil {
		t.Fatalf("ps: %v\n%s", err, ps)
	}
	ls, err := run(t, "instance", "ls")
	if err != nil {
		t.Fatalf("instance ls: %v\n%s", err, ls)
	}

	if ps != ls {
		t.Errorf("dicer ps and dicer instance ls differ:\n%s\n---\n%s", ps, ls)
	}
	for _, want := range []string{"NAME", "web", "db", "cache", "8080->80/tcp"} {
		if !strings.Contains(ps, want) {
			t.Errorf("ps output is missing %q:\n%s", want, ps)
		}
	}

	// -a is taken for Docker's sake, and changes nothing.
	if all, err := run(t, "ps", "-a"); err != nil || all != ps {
		t.Errorf("ps -a = %q, %v; want what ps shows", all, err)
	}
}

func TestPsQuietAndFilters(t *testing.T) {
	serveInstanceDaemon(t, newFakeInstanceDaemon(fakeInstances()...))

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"ps", "-q"}, "cache\ndb\nweb\n"},
		{[]string{"ps", "-q", "--filter", "state=running"}, "web\n"},
		{[]string{"ps", "-q", "-f", "state=Running", "-f", "state=paused"}, "cache\nweb\n"},
		{[]string{"ps", "-q", "--filter", "label=team"}, "db\nweb\n"},
		{[]string{"ps", "-q", "--filter", "label=team=data"}, "db\n"},
		{[]string{"ps", "-q", "--filter", "network=default", "--filter", "name=w"}, "web\n"},
		{[]string{"ps", "-q", "--filter", "image=redis"}, "cache\n"},
		{[]string{"ps", "--format", "{{.Name}}:{{.State}}", "--filter", "state=stopped"}, "db:Stopped\n"},
	} {
		out, err := run(t, tc.args...)
		if err != nil {
			t.Errorf("%v: %v\n%s", tc.args, err, out)
			continue
		}
		if out != tc.want {
			t.Errorf("%v = %q, want %q", tc.args, out, tc.want)
		}
	}

	if out, err := run(t, "ps", "--filter", "colour=red"); err == nil || !strings.Contains(err.Error(), "name, state") {
		t.Errorf("an unknown filter key should be refused, naming the known ones: %v\n%s", err, out)
	}
}

func TestPsYAMLAndColumns(t *testing.T) {
	serveInstanceDaemon(t, newFakeInstanceDaemon(fakeInstances()...))

	out, err := run(t, "ps", "--format", "yaml", "-c", "name,ip", "--filter", "name=web")
	if err != nil {
		t.Fatalf("ps: %v\n%s", err, out)
	}

	var rows []map[string]string
	if err := yaml.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("not YAML: %v\n%s", err, out)
	}
	if want := []map[string]string{{"Name": "web", "IP": "10.0.0.5"}}; len(rows) != 1 ||
		rows[0]["Name"] != want[0]["Name"] || rows[0]["IP"] != want[0]["IP"] || len(rows[0]) != 2 {
		t.Errorf("rows = %v, want %v", rows, want)
	}
}

func TestLifecycleShortcutsTakeManyNames(t *testing.T) {
	d := newFakeInstanceDaemon(fakeInstances()...)
	serveInstanceDaemon(t, d)

	if out, err := run(t, "stop", "web", "cache"); err != nil {
		t.Fatalf("stop: %v\n%s", err, out)
	}
	if out, err := run(t, "start", "web", "cache", "db"); err != nil {
		t.Fatalf("start: %v\n%s", err, out)
	}
	if out, err := run(t, "pause", "web"); err != nil {
		t.Fatalf("pause: %v\n%s", err, out)
	}
	if out, err := run(t, "unpause", "web"); err != nil {
		t.Fatalf("unpause: %v\n%s", err, out)
	}

	want := []string{
		"stop web", "stop cache",
		"start web", "start cache", "start db",
		"pause web", "resume web",
	}
	if !slices.Equal(d.calls, want) {
		t.Errorf("calls = %q, want %q", d.calls, want)
	}
}

// TestRmCarriesOnPastAFailure pins down docker rm's behaviour: one missing
// name is reported, the rest are still deleted, and the command fails.
func TestRmCarriesOnPastAFailure(t *testing.T) {
	d := newFakeInstanceDaemon(fakeInstances()...)
	serveInstanceDaemon(t, d)

	out, err := run(t, "rm", "-f", "web", "nope", "db")

	var exitErr *exitError
	if !errors.As(err, &exitErr) || exitErr.code != 1 {
		t.Fatalf("err = %v, want exit status 1", err)
	}
	if !strings.Contains(out, `no instance "nope"`) {
		t.Errorf("output should report the missing instance:\n%s", out)
	}
	if want := []string{"delete web", "delete db"}; !slices.Equal(d.calls, want) {
		t.Errorf("calls = %q, want %q", d.calls, want)
	}
}

func TestRestart(t *testing.T) {
	d := newFakeInstanceDaemon(fakeInstances()...)
	serveInstanceDaemon(t, d)

	if out, err := run(t, "restart", "web", "db"); err != nil {
		t.Fatalf("restart: %v\n%s", err, out)
	}

	// A running instance is stopped first; a stopped one only started.
	if want := []string{"stop web", "start web", "start db"}; !slices.Equal(d.calls, want) {
		t.Errorf("calls = %q, want %q", d.calls, want)
	}
}

func TestRun(t *testing.T) {
	d := newFakeInstanceDaemon()
	serveInstanceDaemon(t, d)

	out, err := run(t, "run", "--name", "web", "-p", "8080:80", "-e", "A=1,2", "-m", "1GiB",
		"nginx:1.27", "nginx", "-g", "daemon off;")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}

	req := d.created
	want := &dicerdv1.CreateInstanceRequest{
		Name:        "web",
		ImageRef:    "nginx:1.27",
		Cmd:         []string{"nginx", "-g", "daemon off;"},
		Start:       true,
		Vcpus:       1,
		MemoryBytes: 1 << 30,
		DiskBytes:   10 << 30,
		Env:         map[string]string{"A": "1,2"},
		Ports:       []*dicerdv1.PortMapping{{HostPort: 8080, GuestPort: 80}},
	}
	if !proto.Equal(req, want) {
		t.Errorf("request = %v\nwant      %v", req, want)
	}
	if !regexp.MustCompile(`Instance web started in [0-9.]+m?s \(10\.0\.0\.9\)`).MatchString(out) {
		t.Errorf("output = %q, want it to say how long the start took and the address", out)
	}

	// The image was pulled first, so its download could be shown, and then
	// the instance created.
	if want := []string{"pull nginx:1.27"}; !slices.Equal(d.calls, want) {
		t.Errorf("calls = %q, want %q", d.calls, want)
	}
	if !strings.Contains(out, "Image nginx:1.27 pulled in") {
		t.Errorf("a download should be reported: %q", out)
	}
}

func TestRunNamesAfterTheImage(t *testing.T) {
	d := newFakeInstanceDaemon()
	serveInstanceDaemon(t, d)

	if out, err := run(t, "run", "ghcr.io/acme/Web_App:2@sha256:abc", "--", "serve"); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}

	name := d.created.GetName()
	if !strings.HasPrefix(name, "web-app-") || len(name) != len("web-app-")+4 {
		t.Errorf("name = %q, want web-app- and a four-character suffix", name)
	}
	if !slices.Equal(d.created.GetCmd(), []string{"serve"}) {
		t.Errorf("cmd = %q, want the -- dropped", d.created.GetCmd())
	}
}

func TestUpdateSendsOnlyWhatChanged(t *testing.T) {
	d := newFakeInstanceDaemon(fakeInstances()...)
	serveInstanceDaemon(t, d)

	if out, err := run(t, "update", "db", "--memory", "2GiB", "--restart", "always", "--init-mode", "exec",
		"-l", "tier=gold"); err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}

	memory := int64(2 << 30)
	want := &dicerdv1.UpdateInstanceRequest{
		Name:          "db",
		MemoryBytes:   &memory,
		RestartPolicy: &dicerdv1.RestartPolicy{Mode: dicerdv1.RestartMode_RESTART_MODE_ALWAYS},
		InitMode:      dicerdv1.InitMode_INIT_MODE_EXEC,
		Labels:        map[string]string{"tier": "gold"},
	}
	if !proto.Equal(d.updated, want) {
		t.Errorf("request = %v\nwant      %v", d.updated, want)
	}

	if _, err := run(t, "update", "db"); err == nil || !strings.Contains(err.Error(), "needs something to change") {
		t.Errorf("an update with nothing to change should say so, got %v", err)
	}
}

func TestInspect(t *testing.T) {
	serveInstanceDaemon(t, newFakeInstanceDaemon(fakeInstances()...))

	out, err := run(t, "inspect", "web")
	if err != nil {
		t.Fatalf("inspect: %v\n%s", err, out)
	}
	for _, want := range []string{
		"● web — docker.io/library/nginx:1.27\n",
		"     Active: running\n",
		"    Command: nginx -g 'daemon off;'\n",
		"    Machine: 2 vCPUs, 1 GiB memory, 10 GiB disk\n",
		"      Ports: 8080->80/tcp\n             10.1.0.1:5353->53/udp\n",
		"        Env: A=1\n             B=2\n",
		"     Labels: team=web\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("inspect output is missing %q:\n%s", want, out)
		}
	}
	// What an instance does not have is left out, not shown as "-".
	if strings.Contains(out, "Volumes") || strings.Contains(out, "\x1b[") {
		t.Errorf("inspect output lists an empty field, or is coloured when piped:\n%s", out)
	}

	out, err = run(t, "inspect", "web", "db", "--format", "json")
	if err != nil {
		t.Fatalf("inspect json: %v\n%s", err, out)
	}
	var records []map[string]any
	if err := json.Unmarshal([]byte(out), &records); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	// The whole record, as the API has it: its field names, its enums'
	// names, and 64-bit numbers as strings, as protobuf's JSON writes them.
	if len(records) != 2 {
		t.Fatalf("records = %v, want one per instance", records)
	}
	if r := records[0]; r["image_ref"] != "docker.io/library/nginx:1.27" || r["memory_bytes"] != "1073741824" ||
		r["state"] != "INSTANCE_STATE_RUNNING" || r["ip"] != "10.0.0.5" {
		t.Errorf("record = %v", r)
	}
	if records[1]["name"] != "db" {
		t.Errorf("second record = %v", records[1])
	}

	if out, err := run(t, "inspect", "web", "--format", "{{.IP}}"); err != nil || out != "10.0.0.5\n" {
		t.Errorf("inspect --format '{{.IP}}' = %q, %v", out, err)
	}
}

func TestVersion(t *testing.T) {
	serveInstanceDaemon(t, newFakeInstanceDaemon())

	out, err := run(t, "version")
	if err != nil {
		t.Fatalf("version: %v\n%s", err, out)
	}
	for _, want := range []string{"Client:", "Server:", "v9.9.9", "compute-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("version output is missing %q:\n%s", want, out)
		}
	}
}

func TestVersionWithoutDaemon(t *testing.T) {
	isolateConfig(t)
	t.Setenv(remoteEnv, "unix:///nonexistent/dicer.sock")

	out, err := run(t, "version")
	if err == nil {
		t.Error("version should fail when the daemon cannot be reached")
	}
	if !strings.Contains(out, "Client:") || strings.Contains(out, "Server:") {
		t.Errorf("the client's version should still be shown:\n%s", out)
	}
}

func TestCompletionOffersInstancesInTheRightState(t *testing.T) {
	serveInstanceDaemon(t, newFakeInstanceDaemon(fakeInstances()...))

	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"start", ""}, []string{"db"}},
		{[]string{"stop", ""}, []string{"cache", "web"}},
		{[]string{"unpause", ""}, []string{"cache"}},
		// Names already given are not offered again.
		{[]string{"rm", "web", ""}, []string{"cache", "db"}},
		{[]string{"logs", "web", ""}, nil},
	} {
		out, err := run(t, append([]string{"__complete"}, tc.args...)...)
		if err != nil {
			t.Fatalf("complete %v: %v\n%s", tc.args, err, out)
		}
		if got := completedNames(out); !slices.Equal(got, tc.want) {
			t.Errorf("complete %v = %q, want %q", tc.args, got, tc.want)
		}
	}
}

// completedNames picks the values out of cobra's __complete output, which
// ends with a directive line and, here, a note on stderr.
func completedNames(out string) []string {
	var names []string
	for line := range strings.SplitSeq(out, "\n") {
		if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "Completion ended") {
			continue
		}
		names = append(names, completionValue(line))
	}
	return names
}

func TestShortcutsAreGrouped(t *testing.T) {
	root := NewCommand()
	for _, name := range []string{
		"run", "ps", "exec", "logs", "start", "stop", "restart", "pause", "resume", "rm",
		"create", "update", "inspect", "cp", "pull", "images", "rmi",
	} {
		cmd, _, err := root.Find([]string{name})
		if err != nil || cmd.Name() != name {
			t.Errorf("dicer %s is not a command: %v", name, err)
			continue
		}
		if cmd.GroupID != groupCommon {
			t.Errorf("dicer %s is in group %q, want %q", name, cmd.GroupID, groupCommon)
		}
	}
}

// newTestServer returns a gRPC server that sends errors as dicerd does.
func newTestServer() *grpc.Server {
	return grpc.NewServer(
		grpc.ChainUnaryInterceptor(grpcapi.UnaryStatusInterceptor),
		grpc.ChainStreamInterceptor(grpcapi.StreamStatusInterceptor),
	)
}
