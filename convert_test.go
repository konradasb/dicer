// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// The tests in this file keep the client in step with the API. Each message
// the daemon sends is converted with one of its fields set at a time, and
// the result must not be empty; each request is built with one field of the
// client's input set at a time, and every field of the message must be set
// by one of them. A field added to the API and not to the client fails them.

// fromProtoConversion is the conversion of one message the daemon sends.
type fromProtoConversion struct {
	message proto.Message
	convert func(proto.Message) any

	// unconverted are fields the conversion leaves to its caller.
	unconverted []protoreflect.Name
}

// conversion returns the fromProtoConversion of convert.
func conversion[M proto.Message, T any](convert func(M) T, unconverted ...protoreflect.Name) fromProtoConversion {
	var zero M
	return fromProtoConversion{
		message: zero.ProtoReflect().Type().New().Interface(),
		convert: func(m proto.Message) any {
			typed, _ := m.(M)
			return convert(typed)
		},
		unconverted: unconverted,
	}
}

// dereferenced returns convert, returning what the pointer it returns
// points at.
func dereferenced[M proto.Message, T any](convert func(M) *T) func(M) T {
	return func(m M) T { return *convert(m) }
}

// fromProtoConversions are the conversions of what the daemon sends.
var fromProtoConversions = []fromProtoConversion{
	conversion(instanceFromProto),
	conversion(dereferenced(healthCheckFromProto)),
	conversion(dereferenced(healthFromProto)),
	conversion(restartPolicyFromProto),
	conversion(mountFromProto),
	conversion(portMappingFromProto),
	conversion(processFromProto),
	conversion(instanceStatsFromProto),
	conversion(instanceStatsBatchFromProto),
	conversion(snapshotFromProto),
	conversion(networkFromProto),
	conversion(networkAllocationFromProto),
	conversion(volumeFromProto),
	conversion(imageFromProto),
	conversion(pullProgressFromProto, "image"),
	conversion(pruneResultFromProto),
	conversion(kernelFromProto),
	conversion(hostInfoFromProto),
	conversion(hypervisorInfoFromProto),
	conversion(resourcesFromProto),
	conversion(resourceCapacityFromProto),
	conversion(diskUsageFromProto),
	conversion(instanceResourcesFromProto),
	conversion(eventFromProto),
	conversion(eventBatchFromProto),
}

// TestFromProtoConversionsCarryEveryField checks that each field of what the
// daemon sends reaches the client's value.
func TestFromProtoConversionsCarryEveryField(t *testing.T) {
	for _, tt := range fromProtoConversions {
		descriptor := tt.message.ProtoReflect().Descriptor()
		t.Run(string(descriptor.Name()), func(t *testing.T) {
			for i := range descriptor.Fields().Len() {
				field := descriptor.Fields().Get(i)
				if slices.Contains(tt.unconverted, field.Name()) {
					continue
				}

				m := tt.message.ProtoReflect().New()
				m.Set(field, fieldValue(m, field))
				if reflect.ValueOf(tt.convert(m.Interface())).IsZero() {
					t.Errorf("%s is not converted", field.Name())
				}
			}
		})
	}
}

// requestBuilder builds one request the client sends from its inputs.
type requestBuilder struct {
	message proto.Message

	// inputs are the types of what the builder takes.
	inputs []reflect.Type
	build  func(inputs []any) (proto.Message, error)
}

// builder returns the requestBuilder of a build taking one input.
func builder[A any, M proto.Message](build func(A) (M, error)) requestBuilder {
	var zero M
	return requestBuilder{
		message: zero.ProtoReflect().Type().New().Interface(),
		inputs:  []reflect.Type{reflect.TypeFor[A]()},
		build: func(in []any) (proto.Message, error) {
			a, _ := in[0].(A)
			return build(a)
		},
	}
}

// builder2 returns the requestBuilder of a build taking two inputs.
func builder2[A, B any, M proto.Message](build func(A, B) (M, error)) requestBuilder {
	var zero M
	return requestBuilder{
		message: zero.ProtoReflect().Type().New().Interface(),
		inputs:  []reflect.Type{reflect.TypeFor[A](), reflect.TypeFor[B]()},
		build: func(in []any) (proto.Message, error) {
			a, _ := in[0].(A)
			b, _ := in[1].(B)
			return build(a, b)
		},
	}
}

// requestBuilders are what build the requests the client sends from its
// inputs.
var requestBuilders = []requestBuilder{
	builder2(createInstanceRequest),
	builder2(updateInstanceRequest),
	builder2(forkInstanceRequest),
	builder2(forkSnapshotRequest),
	builder2(func(name string, opts ResizeOptions) (*dicerdv1.ResizeInstanceRequest, error) {
		return resizeInstanceRequest(name, opts), nil
	}),
	builder(healthCheckToProto),
	builder(execInstanceStart),
	builder2(getInstanceLogsRequest),
	builder(func(spec NetworkSpec) (*dicerdv1.CreateNetworkRequest, error) { return createNetworkRequest(spec), nil }),
	builder(importKernelStart),
	builder(getEventsRequest),
}

// unsentFields are the fields of a client input that no request carries:
// the parts of a Cmd that are its streams.
var unsentFields = map[reflect.Type][]string{
	reflect.TypeFor[Cmd](): {"Stdin", "Stdout", "Stderr"},
}

// TestRequestBuildersSetEveryField checks that each field of a request is
// set from the client's input, and that each field of the input sets one.
func TestRequestBuildersSetEveryField(t *testing.T) {
	for _, tt := range requestBuilders {
		descriptor := tt.message.ProtoReflect().Descriptor()
		t.Run(string(descriptor.Name()), func(t *testing.T) {
			set := make(map[protoreflect.Name]bool)
			for i, input := range tt.inputs {
				for _, variant := range inputVariants(input) {
					inputs := make([]any, len(tt.inputs))
					for j, t := range tt.inputs {
						inputs[j] = reflect.New(t).Elem().Interface()
					}
					inputs[i] = variant.value

					req, err := tt.build(inputs)
					if err != nil {
						t.Fatalf("with %s set: %v", variant.field, err)
					}
					fields := setFields(req)
					if len(fields) == 0 {
						t.Errorf("%s sets nothing in the request", variant.field)
					}
					for _, name := range fields {
						set[name] = true
					}
				}
			}

			for i := range descriptor.Fields().Len() {
				if name := descriptor.Fields().Get(i).Name(); !set[name] {
					t.Errorf("%s is never set", name)
				}
			}
		})
	}
}

// sentByName are the requests sent with nothing but values passed as they
// are, such as a name, and the fields they have. A field added to one of
// them must be added to the client, and here.
var sentByName = map[protoreflect.Name][]protoreflect.Name{
	"RenameInstanceRequest":         {"name", "new_name"},
	"StartInstanceRequest":          {"name"},
	"StopInstanceRequest":           {"name"},
	"PauseInstanceRequest":          {"name"},
	"ResumeInstanceRequest":         {"name"},
	"StandbyInstanceRequest":        {"name"},
	"DeleteInstanceRequest":         {"name", "force"},
	"ListInstancesRequest":          {},
	"GetInstanceRequest":            {"name"},
	"GetInstanceStatsRequest":       {"names", "follow"},
	"ListInstanceProcessesRequest":  {"name"},
	"CreateSnapshotRequest":         {"instance", "name"},
	"ListSnapshotsRequest":          {"instance"},
	"GetSnapshotRequest":            {"name"},
	"DeleteSnapshotRequest":         {"name"},
	"RestoreSnapshotRequest":        {"name"},
	"ExecInstanceRequest":           {"start", "stdin", "resize"},
	"CopyToInstanceRequest":         {"start", "data"},
	"ImportKernelRequest":           {"start", "data"},
	"CopyFromInstanceRequest":       {"name", "path"},
	"ListNetworksRequest":           {},
	"GetNetworkRequest":             {"name"},
	"DeleteNetworkRequest":          {"name"},
	"ListNetworkAllocationsRequest": {"name"},
	"CreateVolumeRequest":           {"name", "size_bytes"},
	"ListVolumesRequest":            {},
	"GetVolumeRequest":              {"name"},
	"DeleteVolumeRequest":           {"name"},
	"PullImageRequest":              {"ref"},
	"ListImagesRequest":             {},
	"GetImageRequest":               {"ref"},
	"DeleteImageRequest":            {"ref", "force"},
	"PruneImagesRequest":            {},
	"ListKernelsRequest":            {},
	"GetKernelRequest":              {"name"},
	"DeleteKernelRequest":           {"name"},
	"GetHostInfoRequest":            {},
	"GetResourcesRequest":           {},
}

// returnedAsTheyAre are the responses whose fields the client returns
// without converting them, or converts with the conversion of a message in
// fromProtoConversions, and the fields they have.
var returnedAsTheyAre = map[protoreflect.Name][]protoreflect.Name{
	"Empty":                          {},
	"ListInstancesResponse":          {"instances"},
	"ListInstanceProcessesResponse":  {"processes"},
	"InstanceLogChunk":               {"data"},
	"ListSnapshotsResponse":          {"snapshots"},
	"ExecInstanceResponse":           {"stdout", "stderr", "exit_code"},
	"CopyFromInstanceResponse":       {"data"},
	"ListNetworksResponse":           {"networks"},
	"ListNetworkAllocationsResponse": {"allocations"},
	"ListVolumesResponse":            {"volumes"},
	"ListImagesResponse":             {"images"},
	"ListKernelsResponse":            {"kernels"},
}

// TestEveryCallIsCovered checks that each call's request and response is
// one the tests above, or the tables beside them, cover, so that a call
// added to the API is not left out of the client unnoticed.
func TestEveryCallIsCovered(t *testing.T) {
	built := make(map[protoreflect.Name]bool)
	for _, b := range requestBuilders {
		built[b.message.ProtoReflect().Descriptor().Name()] = true
	}
	converted := make(map[protoreflect.Name]bool)
	for _, c := range fromProtoConversions {
		converted[c.message.ProtoReflect().Descriptor().Name()] = true
	}

	service := dicerdv1.File_dicerd_v1_dicerd_proto.Services().ByName("DaemonService")
	for i := range service.Methods().Len() {
		method := service.Methods().Get(i)

		request := method.Input()
		if fields, ok := sentByName[request.Name()]; ok {
			if got := fieldNames(request); !slices.Equal(got, fields) {
				t.Errorf("%s: %s has fields %v; the client sends %v", method.Name(), request.Name(), got, fields)
			}
		} else if !built[request.Name()] {
			t.Errorf("%s: nothing builds %s", method.Name(), request.Name())
		}

		response := method.Output()
		if fields, ok := returnedAsTheyAre[response.Name()]; ok {
			if got := fieldNames(response); !slices.Equal(got, fields) {
				t.Errorf("%s: %s has fields %v; the client reads %v", method.Name(), response.Name(), got, fields)
			}
		} else if !converted[response.Name()] {
			t.Errorf("%s: nothing converts %s", method.Name(), response.Name())
		}
	}
}

// fieldNames returns the names of a message's fields, in their order.
func fieldNames(descriptor protoreflect.MessageDescriptor) []protoreflect.Name {
	names := []protoreflect.Name{}
	for i := range descriptor.Fields().Len() {
		names = append(names, descriptor.Fields().Get(i).Name())
	}
	return names
}

// setFields returns the names of the fields set in m.
func setFields(m proto.Message) []protoreflect.Name {
	var names []protoreflect.Name
	m.ProtoReflect().Range(func(field protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		names = append(names, field.Name())
		return true
	})
	return names
}

// fieldValue returns a value for field of m that is not empty, with every
// field of a message set.
func fieldValue(m protoreflect.Message, field protoreflect.FieldDescriptor) protoreflect.Value {
	switch {
	case field.IsList():
		list := m.NewField(field).List()
		list.Append(singularValue(list.NewElement, field))
		return protoreflect.ValueOfList(list)
	case field.IsMap():
		entries := m.NewField(field).Map()
		entries.Set(protoreflect.ValueOfString("k").MapKey(), singularValue(entries.NewValue, field.MapValue()))
		return protoreflect.ValueOfMap(entries)
	default:
		return singularValue(func() protoreflect.Value { return m.NewField(field) }, field)
	}
}

// singularValue returns one value of field's kind that is not empty. A
// message is made with newMessage and has every field set.
func singularValue(newMessage func() protoreflect.Value, field protoreflect.FieldDescriptor) protoreflect.Value {
	switch field.Kind() {
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(true)
	case protoreflect.EnumKind:
		return protoreflect.ValueOfEnum(1)
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return protoreflect.ValueOfInt32(1)
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return protoreflect.ValueOfInt64(1)
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return protoreflect.ValueOfUint32(1)
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return protoreflect.ValueOfUint64(1)
	case protoreflect.FloatKind:
		return protoreflect.ValueOfFloat32(1.5)
	case protoreflect.DoubleKind:
		return protoreflect.ValueOfFloat64(1.5)
	case protoreflect.StringKind:
		return protoreflect.ValueOfString("x")
	case protoreflect.BytesKind:
		return protoreflect.ValueOfBytes([]byte("x"))
	default:
		v := newMessage()
		m := v.Message()
		fields := m.Descriptor().Fields()
		for i := range fields.Len() {
			m.Set(fields.Get(i), fieldValue(m, fields.Get(i)))
		}
		return v
	}
}

// inputVariant is a client input with one of its fields set.
type inputVariant struct {
	field string
	value any
}

// inputVariants returns a value of type t with each of its fields set in
// turn, or set itself if it is not a struct or a pointer to one.
func inputVariants(t reflect.Type) []inputVariant {
	structType := t
	if t.Kind() == reflect.Pointer {
		structType = t.Elem()
	}
	if structType.Kind() != reflect.Struct {
		set := reflect.New(t).Elem()
		fill(set)
		return []inputVariant{{field: t.String(), value: set.Interface()}}
	}

	var variants []inputVariant
	for i := range structType.NumField() {
		field := structType.Field(i)
		if !field.IsExported() || slices.Contains(unsentFields[structType], field.Name) {
			continue
		}
		set := reflect.New(structType)
		fill(set.Elem().Field(i))
		value := set.Interface()
		if t.Kind() != reflect.Pointer {
			value = set.Elem().Interface()
		}
		variants = append(variants, inputVariant{field: structType.Name() + "." + field.Name, value: value})
	}
	return variants
}

// validValues are values of the client's enumerations, and of types whose
// zero value has meaning of its own, that fill uses.
var validValues = map[reflect.Type]any{
	reflect.TypeFor[HypervisorType](): HypervisorTypeFirecracker,
	reflect.TypeFor[InitMode]():       InitModeExec,
	reflect.TypeFor[RestartMode]():    RestartModeAlways,
	reflect.TypeFor[MountType]():      MountTypeTmpfs,
	reflect.TypeFor[Protocol]():       ProtocolUDP,
	reflect.TypeFor[PullPolicy]():     PullPolicyAlways,
	reflect.TypeFor[LogSource]():      LogSourceHypervisor,
	reflect.TypeFor[Architecture]():   ArchitectureAArch64,
	reflect.TypeFor[EventKind]():      EventKindInstance,
	reflect.TypeFor[time.Duration]():  time.Minute,
	reflect.TypeFor[time.Time]():      time.Unix(1, 0),
	// A health check has one probe at a time.
	reflect.TypeFor[HealthCheck](): HealthCheck{Exec: []string{"true"}, Interval: time.Second},
}

// fill sets v to a value that is not empty, and valid for the client to
// send.
func fill(v reflect.Value) {
	if valid, ok := validValues[v.Type()]; ok {
		v.Set(reflect.ValueOf(valid))
		return
	}

	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1.5)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem())
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0))
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		key, value := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fill(key)
		fill(value)
		v.SetMapIndex(key, value)
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i))
			}
		}
	}
}
