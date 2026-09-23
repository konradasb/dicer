// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package filestore

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/access"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()

	dir := t.TempDir()
	s, err := NewManager(Config{DataDir: filepath.Join(dir, "data")})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	return s
}

func testNetwork(name string) dicer.Network {
	return dicer.Network{
		ID:      "net-" + name,
		Name:    name,
		Subnet:  "10.0.0.0/24",
		Gateway: "10.0.0.1",
		Bridge:  "br-" + name,
	}
}

func testInstance(name string) dicer.InstanceSpec {
	return dicer.InstanceSpec{
		ID:          "id-" + name,
		Name:        name,
		ImageRef:    "docker.io/library/alpine:latest",
		KernelName:  "default",
		NetworkName: "default",
		VCPUs:       1,
	}
}

func TestCreateGetList(t *testing.T) {
	s := newTestManager(t)

	if err := s.CreateInstance(testInstance("web")); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	// Lookup by name and by ID must both work.
	for _, key := range []string{"web", "id-web"} {
		got, err := s.GetInstance(key)
		if err != nil {
			t.Fatalf("GetInstance(%q): %v", key, err)
		}
		if got.Name != "web" {
			t.Errorf("GetInstance(%q) = %q, want %q", key, got.Name, "web")
		}
	}

	if err := s.CreateInstance(testInstance("db")); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	list, err := s.ListInstances()
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	// Sorted by name for stable output.
	if len(list) != 2 || list[0].Name != "db" || list[1].Name != "web" {
		t.Errorf("ListInstances = %v, want [db web]", names(list))
	}
}

func names(in []dicer.InstanceSpec) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = v.Name
	}
	return out
}

func TestCreateDuplicateFails(t *testing.T) {
	s := newTestManager(t)

	if err := s.CreateInstance(testInstance("web")); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	err := s.CreateInstance(testInstance("web"))
	if !errors.Is(err, dicer.ErrExists) {
		t.Errorf("duplicate create error = %v, want ErrExists", err)
	}
}

func TestGetMissingReturnsNotFound(t *testing.T) {
	s := newTestManager(t)

	_, err := s.GetInstance("nope")
	if !errors.Is(err, dicer.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{DataDir: filepath.Join(dir, "data")}

	s, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := s.CreateInstance(testInstance("web")); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	if err := s.CreateNetwork(testNetwork("default")); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	reopened, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}

	got, err := reopened.GetInstance("web")
	if err != nil {
		t.Fatalf("GetInstance after reopen: %v", err)
	}
	if got.ImageRef != "docker.io/library/alpine:latest" {
		t.Errorf("ImageRef = %q, want the value written before reopen", got.ImageRef)
	}
	if _, err := reopened.GetNetwork("default"); err != nil {
		t.Errorf("GetNetwork after reopen: %v", err)
	}
}

func TestDeleteRemovesInstanceDirectory(t *testing.T) {
	s := newTestManager(t)

	if err := s.CreateInstance(testInstance("web")); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	// A sibling file in the instance directory (an overlay disk, say) must go
	// away with the instance.
	disk := filepath.Join(s.InstanceDir("web"), "overlay.img")
	if err := os.WriteFile(disk, []byte("disk"), 0o600); err != nil {
		t.Fatalf("write overlay: %v", err)
	}

	if err := s.DeleteInstance("web"); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if _, err := os.Stat(s.InstanceDir("web")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("instance directory still present after delete")
	}
}

func TestMalformedDefinitionIsSkippedNotFatal(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{DataDir: filepath.Join(dir, "data")}

	s, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := s.CreateInstance(testInstance("good")); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	// A hand-edit gone wrong must not stop the daemon from starting.
	bad := filepath.Join(cfg.DataDir, instancesDir, "bad")
	if err := os.MkdirAll(bad, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bad, configFile), []byte("{{{not yaml"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	reopened, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager with a malformed definition present: %v", err)
	}

	if _, err := reopened.GetInstance("good"); err != nil {
		t.Errorf("good instance should still load: %v", err)
	}
	if _, err := reopened.GetInstance("bad"); !errors.Is(err, dicer.ErrNotFound) {
		t.Errorf("malformed instance error = %v, want ErrNotFound", err)
	}
}

// A definition copied to another place by hand still names its old one. It
// is skipped rather than loaded under a name that its own contents contradict.
func TestMisplacedDefinitionIsSkipped(t *testing.T) {
	cfg := Config{DataDir: filepath.Join(t.TempDir(), "data")}
	s, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := s.CreateInstance(testInstance("web")); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	copied := filepath.Join(cfg.DataDir, instancesDir, "copy")
	if err := os.MkdirAll(copied, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.InstanceDir("web"), configFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(copied, configFile), data, 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := reopened.GetInstance("copy"); !errors.Is(err, dicer.ErrNotFound) {
		t.Errorf("GetInstance(copy) = %v, want the misplaced definition skipped", err)
	}
	if got, err := reopened.GetInstance("web"); err != nil || got.Name != "web" {
		t.Errorf("GetInstance(web) = %+v, %v; want the original", got, err)
	}
}

func TestCreateRejectsPathTraversal(t *testing.T) {
	s := newTestManager(t)

	err := s.CreateNetwork(dicer.Network{ID: "n1", Name: "../escape"})
	if !errors.Is(err, dicer.ErrInvalidArgument) {
		t.Fatalf("CreateNetwork(../escape) = %v, want ErrInvalidArgument", err)
	}
}

// Manager is the store access enrols clients into.
var _ access.Store = (*Manager)(nil)

// A trusted client is looked up by the fingerprint its certificate presents,
// and must still be trusted after a restart.
func TestTrustedClientsPersistAndResolveByFingerprint(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	s, err := NewManager(Config{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}

	client := dicer.TrustedClient{Name: "laptop", Fingerprint: "sha256:" + strings.Repeat("ab", 32)}
	if err := s.CreateClient(client); err != nil {
		t.Fatalf("CreateClient: %v", err)
	}

	reopened, err := NewManager(Config{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}

	got, err := reopened.GetClient(client.Fingerprint)
	if err != nil {
		t.Fatalf("GetClient by fingerprint after reopen: %v", err)
	}
	if got.Name != client.Name {
		t.Errorf("client = %+v, want %+v", got, client)
	}

	if err := reopened.DeleteClient(client.Name); err != nil {
		t.Fatalf("DeleteClient: %v", err)
	}
	if _, err := reopened.GetClient(client.Fingerprint); !errors.Is(err, dicer.ErrNotFound) {
		t.Errorf("GetClient after delete = %v, want ErrNotFound", err)
	}
}
