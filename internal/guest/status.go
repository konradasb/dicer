// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package guest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// StatusSize is the size of the status disk, and of an encoded Status.
const StatusSize = 4096

// Status is what the guest reports of its own end, on the status disk: a raw
// disk, with no filesystem, that the host zeroes before every boot and reads
// after the VMM exits. It needs nothing of the daemon while the guest runs,
// so a guest that ends while no daemon is watching still says how.
//
// The disk holds one JSON document, padded with zero bytes.
type Status struct {
	// Boots counts the times dicer-init has come up since the host zeroed
	// the disk. A second boot means the guest reset without saying why --
	// a kernel panic, or a reboot from inside -- and a VMM that reboots
	// guests in place would otherwise carry on as if nothing happened.
	Boots int `json:"boots"`

	// ExitCode is the workload's exit code, once it has exited.
	ExitCode *int `json:"exit_code,omitempty"`
}

// Encode returns the status as the status disk holds it.
func (s Status) Encode() []byte {
	// Marshalling an int and a pointer to one cannot fail.
	data, _ := json.Marshal(s)

	out := make([]byte, StatusSize)
	copy(out, data)
	return out
}

// DecodeStatus reads a status as Encode wrote it. A disk that is still all
// zeroes holds the zero Status: nothing booted from it.
func DecodeStatus(data []byte) (Status, error) {
	if i := bytes.IndexByte(data, 0); i >= 0 {
		data = data[:i]
	}
	if len(data) == 0 {
		return Status{}, nil
	}

	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		return Status{}, fmt.Errorf("decode status: %w", err)
	}
	return s, nil
}

// ReadStatus reads the status disk at device.
func ReadStatus(device string) (Status, error) {
	f, err := os.Open(device)
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, StatusSize)
	if _, err := f.ReadAt(buf, 0); err != nil {
		return Status{}, fmt.Errorf("read %s: %w", device, err)
	}
	return DecodeStatus(buf)
}

// WriteStatus writes st to the status disk at device, and has it reach the
// disk before returning: the machine may end the moment it does.
func WriteStatus(device string, st Status) error {
	f, err := os.OpenFile(device, os.O_WRONLY|os.O_SYNC, 0)
	if err != nil {
		return err
	}

	if _, err := f.WriteAt(st.Encode(), 0); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", device, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync %s: %w", device, err)
	}
	return f.Close()
}
