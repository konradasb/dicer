// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package daemon

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// configReference is the documentation's page for the configuration, whose
// YAML block shows every key with its default.
const configReference = "../../docs/content/docs/reference/configuration.md"

// referenceConfig returns the YAML block of the configuration reference.
func referenceConfig(t *testing.T) []byte {
	t.Helper()

	page, err := os.ReadFile(configReference)
	if err != nil {
		t.Fatal(err)
	}
	_, block, ok := bytes.Cut(page, []byte("\n```yaml\n"))
	if !ok {
		t.Fatalf("%s has no yaml block", configReference)
	}
	block, _, ok = bytes.Cut(block, []byte("\n```"))
	if !ok {
		t.Fatalf("%s's yaml block does not end", configReference)
	}
	return block
}

// TestConfigReferenceIsTheDefaults keeps the configuration reference loadable
// and in step with defaultConfig.
func TestConfigReferenceIsTheDefaults(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, string(referenceConfig(t))))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if want := defaultConfig(); !reflect.DeepEqual(*cfg, want) {
		t.Errorf("the configuration reference = %+v\nwant the defaults %+v", *cfg, want)
	}
}

// TestConfigReferenceShowsEveryKey keeps the configuration reference complete.
func TestConfigReferenceShowsEveryKey(t *testing.T) {
	data := referenceConfig(t)

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}

	for _, key := range configKeys(reflect.TypeFor[Config](), "") {
		if lookupKey(doc.Content[0], strings.Split(key, ".")) == nil {
			t.Errorf("the configuration reference does not show %s", key)
		}
	}
}

// configKeys returns the dotted YAML path of every leaf key in t.
func configKeys(t reflect.Type, prefix string) []string {
	var keys []string
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "" || name == "-" {
			continue
		}
		if f.Type.Kind() == reflect.Struct && f.Type.PkgPath() == t.PkgPath() {
			keys = append(keys, configKeys(f.Type, prefix+name+".")...)
			continue
		}
		keys = append(keys, prefix+name)
	}
	return keys
}

// lookupKey returns the value at path in a mapping node, or nil.
func lookupKey(n *yaml.Node, path []string) *yaml.Node {
	if len(path) == 0 {
		return n
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == path[0] {
			return lookupKey(n.Content[i+1], path[1:])
		}
	}
	return nil
}
