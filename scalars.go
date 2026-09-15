package lightning

import (
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/hiett/lightning/graphql"
)

// This file maps Go types onto GraphQL scalars.
//
// The five scalars every GraphQL tool assumes exist — String, Int, Float,
// Boolean and ID — are spelled as the specification spells them. Anything Go
// can express that those five cannot represent faithfully gets a custom scalar,
// which is spec-legal, rather than being squeezed into a built-in and losing
// information.

// Scalar names. These are the only names that reach a client.
const (
	scalarString  = "String"
	scalarInt     = "Int"
	scalarFloat   = "Float"
	scalarBoolean = "Boolean"
	scalarID      = "ID"

	// scalarInt64 carries a 64-bit integer as a decimal string. A GraphQL Int
	// is 32-bit, and a JSON number loses precision above 2^53 once a JavaScript
	// client parses it, so neither can carry a Go int64 without silently
	// corrupting large values.
	scalarInt64 = "Int64"

	// scalarTime carries a time.Time as an RFC 3339 timestamp.
	scalarTime = "Time"

	// scalarBytes carries a []byte as a base64 string.
	scalarBytes = "Bytes"
)

// ID is the Go representation of the GraphQL ID scalar: an opaque identifier
// that travels as a string but is not meant to be read.
//
// It is a struct rather than a defined string type because a defined string
// type is indistinguishable from any other string by kind, which is how the
// scalar mapping works.
type ID struct {
	Value string
}

// NewID returns an ID holding value.
func NewID(value string) ID { return ID{Value: value} }

// String returns the ID's underlying value.
func (id ID) String() string { return id.Value }

var scalarDescriptions = map[string]string{
	scalarInt64: "A 64-bit signed integer, serialised as a decimal string because a JSON number cannot carry the full range without loss of precision.",
	scalarTime:  "An instant in time, serialised as an RFC 3339 timestamp.",
	scalarBytes: "Arbitrary binary data, serialised as a base64 string.",
}

// scalars maps a Go type to the GraphQL scalar it becomes.
var scalars = map[reflect.Type]string{
	reflect.TypeOf(bool(false)): scalarBoolean,

	// A GraphQL Int is a 32-bit signed integer, so only Go types that fit in
	// one map to it.
	reflect.TypeOf(int8(0)):   scalarInt,
	reflect.TypeOf(int16(0)):  scalarInt,
	reflect.TypeOf(int32(0)):  scalarInt,
	reflect.TypeOf(uint8(0)):  scalarInt,
	reflect.TypeOf(uint16(0)): scalarInt,

	// Everything wider goes to Int64. Go's int is 64 bits on every platform
	// this library targets, so it belongs here rather than in Int.
	reflect.TypeOf(int(0)):    scalarInt64,
	reflect.TypeOf(int64(0)):  scalarInt64,
	reflect.TypeOf(uint(0)):   scalarInt64,
	reflect.TypeOf(uint32(0)): scalarInt64,
	reflect.TypeOf(uint64(0)): scalarInt64,

	reflect.TypeOf(float32(0)): scalarFloat,
	reflect.TypeOf(float64(0)): scalarFloat,

	reflect.TypeOf(string("")): scalarString,

	reflect.TypeOf(ID{}):        scalarID,
	reflect.TypeOf(time.Time{}): scalarTime,
	reflect.TypeOf([]byte{}):    scalarBytes,
}

// scalarFor reports the scalar a Go type maps to.
//
// A defined type whose underlying kind is a scalar kind maps to the same scalar
// as its underlying type, so `type Email string` is a String without having to
// say so.
func scalarFor(goType reflect.Type) (string, bool) {
	if name, ok := scalars[goType]; ok {
		return name, true
	}
	for candidate, name := range scalars {
		if goType.Kind() == candidate.Kind() && isScalarKind(goType.Kind()) {
			return name, true
		}
	}
	return "", false
}

func isScalarKind(k reflect.Kind) bool {
	switch k {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64,
		reflect.String:
		return true
	}
	return false
}

// scalarUnwrappers converts a Go value into the shape a scalar has on the wire.
// A scalar with no entry here is written as encoding/json writes it.
var scalarUnwrappers = map[string]func(any) (any, error){
	scalarInt64: unwrapInt64,
	scalarID:    unwrapID,
}

// newScalar builds the runtime type for a named scalar, with its serialisation
// and its description.
func newScalar(name string) *graphql.Scalar {
	return &graphql.Scalar{
		Type:        name,
		Description: scalarDescriptions[name],
		Unwrapper:   scalarUnwrappers[name],
	}
}

// deref follows pointers to a concrete value, reporting whether it is nil.
func deref(source any) (reflect.Value, bool) {
	v := reflect.ValueOf(source)
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return reflect.Value{}, true
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return reflect.Value{}, true
	}
	return v, false
}

// unwrapInt64 renders a wide integer as a decimal string.
func unwrapInt64(source any) (any, error) {
	v, isNil := deref(source)
	if isNil {
		return nil, nil
	}

	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10), nil
	default:
		return nil, fmt.Errorf("cannot serialise %s as %s", v.Type(), scalarInt64)
	}
}

// unwrapID renders an ID as its underlying string.
func unwrapID(source any) (any, error) {
	v, isNil := deref(source)
	if isNil {
		return nil, nil
	}

	if id, ok := v.Interface().(ID); ok {
		return id.Value, nil
	}
	if v.Kind() == reflect.String {
		return v.String(), nil
	}
	return nil, fmt.Errorf("cannot serialise %s as %s", v.Type(), scalarID)
}
