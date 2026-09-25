// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package compose reads compose files: a project's instances, networks and
// volumes, described together in YAML as Docker Compose describes
// containers, and turned into the requests the daemon's API takes.
//
// A project's instances are found again by their labels: LabelProject names
// the project, LabelService the service, and LabelConfigHash is a digest of
// the definition, so that a service whose definition has changed can be told
// from one that has not.
package compose

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"

	"google.golang.org/protobuf/proto"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// The labels a project's instances carry.
const (
	// LabelPrefix starts every label dicer compose sets. A compose file may
	// not set one of its own.
	LabelPrefix = "dicer.compose."
	// LabelProject is the name of the project an instance belongs to.
	LabelProject = LabelPrefix + "project"
	// LabelService is the service an instance runs.
	LabelService = LabelPrefix + "service"
	// LabelConfigHash is ConfigHash of the definition the instance was
	// created from.
	LabelConfigHash = LabelPrefix + "config-hash"
)

// Project is a compose file, checked and resolved: its variables
// substituted, its relative paths made absolute, and its names made the
// daemon's.
type Project struct {
	// Name is the project's name, which its resources' names start with.
	Name string
	// Dir is the directory relative paths in the file are taken from.
	Dir string
	// File is the compose file's path.
	File string

	Services map[string]*Service
	Networks map[string]*Network
	Volumes  map[string]*Volume

	// Written is the file with its anchors expanded and its extensions left
	// out, but its variables as they are written: ${DB_PASSWORD}, not the
	// password.
	Written []byte
	// Resolved is the file as it was read: its variables substituted, its
	// anchors expanded and its extensions left out.
	Resolved []byte
}

// Service is one of a project's services: an instance, and what it waits
// for before it starts.
type Service struct {
	// Name is the service's name in the file.
	Name string
	// Instance is the request that defines the service's instance, without
	// starting it. Its labels include the project's.
	Instance *dicerdv1.CreateInstanceRequest
	// DependsOn are the services to start first, in the order given.
	DependsOn []Dependency
}

// Dependency is a service another waits for, and what it waits for.
type Dependency struct {
	Service   string
	Condition Condition
}

// Condition is what a service waits for from one it depends on.
type Condition string

// The conditions depends_on takes, named as Docker Compose names them.
const (
	// ConditionStarted waits for the service to be running. The default.
	ConditionStarted Condition = "service_started"
	// ConditionHealthy waits for the service's health check to pass.
	ConditionHealthy Condition = "service_healthy"
	// ConditionCompletedSuccessfully waits for the service to end, with
	// exit code 0.
	ConditionCompletedSuccessfully Condition = "service_completed_successfully"
)

// Network is a network a project uses: its own, created with it, or an
// external one that must already exist.
type Network struct {
	// Key is the network's name in the file, and Name the daemon's.
	Key  string
	Name string
	// External networks are not created or deleted with the project.
	External bool
	// Request creates the network. Nil for an external one.
	Request *dicerdv1.CreateNetworkRequest
}

// Volume is a volume a project uses: its own, created with it, or an
// external one that must already exist.
type Volume struct {
	// Key is the volume's name in the file, and Name the daemon's.
	Key  string
	Name string
	// External volumes are not created or deleted with the project.
	External bool
	// Request creates the volume. Nil for an external one.
	Request *dicerdv1.CreateVolumeRequest
}

// ServiceNames returns the names of the project's services, sorted.
func (p *Project) ServiceNames() []string {
	return slices.Sorted(maps.Keys(p.Services))
}

// Order returns the services named, and every service they depend on,
// each after those it depends on. No names means every service.
func (p *Project) Order(names ...string) ([]*Service, error) {
	if len(names) == 0 {
		names = p.ServiceNames()
	}

	var (
		order   []*Service
		visited = make(map[string]bool)
		visit   func(name string)
	)
	visit = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true

		s := p.Services[name]
		for _, dep := range s.DependsOn {
			visit(dep.Service)
		}
		order = append(order, s)
	}

	for _, name := range names {
		if _, ok := p.Services[name]; !ok {
			return nil, fmt.Errorf("no such service: %s", name)
		}
		visit(name)
	}
	return order, nil
}

// Select returns the services named, and nothing they depend on, in the
// order Order would give them. No names means every service.
func (p *Project) Select(names ...string) ([]*Service, error) {
	order, err := p.Order(names...)
	if err != nil || len(names) == 0 {
		return order, err
	}

	return slices.DeleteFunc(order, func(s *Service) bool {
		return !slices.Contains(names, s.Name)
	}), nil
}

// ServiceFor returns the service an instance of the project runs, if it
// does.
func (p *Project) ServiceFor(inst *dicerdv1.Instance) (*Service, bool) {
	if inst.GetLabels()[LabelProject] != p.Name {
		return nil, false
	}
	s, ok := p.Services[inst.GetLabels()[LabelService]]
	return s, ok
}

// ConfigHash digests the definition a request gives an instance, leaving out
// its config-hash label and whether to start it. Two requests with the same
// hash define the same instance.
func ConfigHash(req *dicerdv1.CreateInstanceRequest) string {
	def, ok := proto.Clone(req).(*dicerdv1.CreateInstanceRequest)
	if !ok {
		panic("compose: clone of a CreateInstanceRequest is not one")
	}
	def.Start = false
	delete(def.GetLabels(), LabelConfigHash)

	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(def)
	if err != nil {
		panic(fmt.Sprintf("compose: marshal instance definition: %v", err))
	}

	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
