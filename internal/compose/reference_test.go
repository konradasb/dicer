// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package compose

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/docker/go-units"
)

// fileReference is the documentation's page for the compose file, which make
// docs-gen generates from rawFile's doc comments.
const fileReference = "../../docs/content/docs/reference/compose-file.md"

// TestFileReferenceStatesTheDefaults keeps the defaults the compose file
// reference states in step with the ones an instance and a volume get.
func TestFileReferenceStatesTheDefaults(t *testing.T) {
	page, err := os.ReadFile(fileReference)
	if err != nil {
		t.Fatal(err)
	}

	for key, want := range map[string]string{
		"services.*.vcpus":  strconv.Itoa(defaultVCPUs),
		"services.*.memory": units.BytesSize(defaultMemoryBytes),
		"services.*.disk":   units.BytesSize(defaultDiskBytes),
		"volumes.*.size":    units.BytesSize(defaultVolumeBytes),
	} {
		_, entry, ok := strings.Cut(string(page), "\n### `"+key+"` {#")
		if !ok {
			t.Errorf("the compose file reference has no entry for %s: run make docs-gen", key)
			continue
		}
		entry, _, _ = strings.Cut(entry, "\n#")

		if !strings.Contains(entry, "Unset is "+want+".") {
			t.Errorf("the compose file reference does not say %s is %s when unset:\n%s", key, want, entry)
		}
	}
}
