// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cloudhypervisor

import (
	"testing"

	"github.com/konradasb/dicer/internal/hypervisor"
)

func TestToVMConfigSharesDirectories(t *testing.T) {
	spec := hypervisor.VirtualMachine{
		Memory: hypervisor.MemoryConfig{SizeBytes: 512 << 20},
		Filesystems: []hypervisor.FilesystemConfig{
			{Tag: "dicerfs0", Socket: "/run/dicer/instances/web/fs0.sock"},
			{Tag: "dicerfs1", Socket: "/run/dicer/instances/web/fs1.sock"},
		},
	}

	cfg := ToVMConfig(spec)
	if cfg.Fs == nil || len(*cfg.Fs) != 2 {
		t.Fatalf("fs = %v, want a device for each directory", cfg.Fs)
	}
	for i, fs := range *cfg.Fs {
		want := spec.Filesystems[i]
		if fs.Tag != want.Tag || fs.Socket != want.Socket || fs.NumQueues < 1 || fs.QueueSize < 1 {
			t.Errorf("fs[%d] = %+v, want tag %s on %s with queues", i, fs, want.Tag, want.Socket)
		}
	}
	// virtiofsd reaches into guest memory, which must be shared with it.
	if cfg.Memory.Shared == nil || !*cfg.Memory.Shared {
		t.Error("memory is not shared, which a vhost-user device needs")
	}
}

func TestToVMConfigWithoutDirectories(t *testing.T) {
	cfg := ToVMConfig(hypervisor.VirtualMachine{Memory: hypervisor.MemoryConfig{SizeBytes: 512 << 20}})
	if cfg.Fs != nil {
		t.Errorf("fs = %v, want none", *cfg.Fs)
	}
	if cfg.Memory.Shared != nil {
		t.Error("memory is shared with no device that needs it")
	}
}
