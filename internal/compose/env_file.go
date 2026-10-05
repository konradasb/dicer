// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package compose

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// parseEnvFile reads KEY=VALUE lines, as a .env or env_file holds them. Blank
// lines and lines starting with # are skipped, an export before the key is
// allowed, and a value in matching quotes has them removed. A line with only
// a KEY is recorded with no value.
func parseEnvFile(r io.Reader) (map[string]*string, error) {
	env := make(map[string]*string)

	scanner := bufio.NewScanner(r)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimPrefix(text, "export ")

		key, value, ok := strings.Cut(text, "=")
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, " \t") {
			return nil, fmt.Errorf("line %d: want KEY=VALUE, not %q", line, text)
		}
		if !ok {
			env[key] = nil
			continue
		}

		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		env[key] = &value
	}

	return env, scanner.Err()
}
