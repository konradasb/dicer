// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/konradasb/dicer/internal/compose"
)

// composeVariables follows the compose file's keys: how values are
// substituted, which is interpolate.go's.
const composeVariables = "\n## Variables {#variables}\n\n" +
	"Values, not keys, may hold variables, which are taken from the environment, then from `.env` " +
	"in the project directory, or the file `--env-file` names:\n\n" +
	"| Written | Is |\n|---|---|\n" +
	"| `$VAR`, `${VAR}` | The value, or nothing if it is not set. |\n" +
	"| `${VAR:-default}` | `default` if `VAR` is not set or is empty. |\n" +
	"| `${VAR-default}` | `default` if `VAR` is not set. |\n" +
	"| `${VAR:?message}` | Fails, saying `message`, if `VAR` is not set or is empty. |\n" +
	"| `${VAR?message}` | Fails if `VAR` is not set. |\n" +
	"| `${VAR:+other}` | `other` if `VAR` is set and not empty, otherwise nothing. |\n" +
	"| `${VAR+other}` | `other` if `VAR` is set, otherwise nothing. |\n" +
	"| `$$` | A `$`. |\n\n" +
	"A default, message or other may hold variables itself: `${PORT:-${DEFAULT_PORT}}`.\n\n" +
	"An unquoted value takes the type of what it becomes, so `cpus: ${CPUS:-2}` is a number and " +
	"`external: ${EXTERNAL:-true}` a boolean. A quoted one, `\"${CPUS}\"`, is always a string.\n\n" +
	"`dicer compose config` shows variables as they are written, not their values, though it " +
	"substitutes them to check the file; `--interpolate` shows the values. The values reach the " +
	"daemon all the same: an instance's environment is part of its definition, which `dicer inspect` " +
	"shows to anyone who can use the daemon."

// composeUnsupported returns the section listing the service keys of Docker
// Compose's that dicer compose refuses, from those it refuses.
func composeUnsupported(keys map[string]bool) string {
	var b strings.Builder

	b.WriteString("\n## Not supported {#not-supported}\n\n" +
		"These keys of a service are refused, saying why, as are `secrets` and `configs` at the top " +
		"of the file. Most describe what a virtual machine already has, such as its isolation from " +
		"the host, or something Dicer has no counterpart of.\n\n" +
		"| Key | Why |\n|---|---|\n")

	unsupported := compose.Unsupported()
	for _, key := range slices.Sorted(maps.Keys(unsupported)) {
		why := strings.ReplaceAll(unsupported[key], "dicer-init", "`dicer-init`")
		if !strings.HasPrefix(why, "`") {
			why = strings.ToUpper(why[:1]) + why[1:]
		}
		fmt.Fprintf(&b, "| `%s` | %s. |\n", key, cell(asCode(why, keys, commandPaths())))
	}

	return b.String()
}
