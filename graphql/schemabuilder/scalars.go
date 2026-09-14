package schemabuilder

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/internal"
)

// This file maps Go types onto GraphQL scalar types.
//
// The five scalars every GraphQL tool assumes exist — String, Int, Float,
// Boolean and ID — are spelled exactly as the specification spells them.
// Anything Go can express that those five cannot represent faithfully gets a
// custom scalar, which is spec-legal, rather than being squeezed into a
// built-in and losing information.

// ID is the Go representation of the GraphQL ID scalar: an opaque identifier
// that is a string on the wire but is not meant to be human-readable.
//
// It is a struct rather than a defined string type because the schema builder
// treats two Go types with the same kind as the same scalar, so `type ID
// string` would be indistinguishable from any other string.
type ID struct {
	Value string
}

// NewID returns an ID holding value.
func NewID(value string) ID { return ID{Value: value} }

// String returns the ID's underlying value.
func (id ID) String() string { return id.Value }

// Scalar names. These are the only names that reach a client.
const (
	ScalarString  = "String"
	ScalarInt     = "Int"
	ScalarFloat   = "Float"
	ScalarBoolean = "Boolean"
	ScalarID      = "ID"

	// ScalarInt64 carries a 64-bit integer as a decimal string. A GraphQL Int
	// is 32-bit, and a JSON number loses precision above 2^53 once a JavaScript
	// client parses it, so neither can carry a Go int64 without silently
	// corrupting large values.
	ScalarInt64 = "Int64"

	// ScalarTime carries a time.Time as an RFC 3339 timestamp.
	ScalarTime = "Time"

	// ScalarBytes carries a []byte as a base64 string.
	ScalarBytes = "Bytes"
)

var scalarDescriptions = map[string]string{
	ScalarInt64: "A 64-bit signed integer, serialised as a decimal string because a JSON number cannot carry the full range without loss of precision.",
	ScalarTime:  "An instant in time, serialised as an RFC 3339 timestamp.",
	ScalarBytes: "Arbitrary binary data, serialised as a base64 string.",
}

// getScalar grabs the appropriate scalar graphql field type name for the passed
// in variable reflect type.
func getScalar(typ reflect.Type) (string, bool) {
	for match, name := range scalars {
		if internal.TypesIdenticalOrScalarAliases(match, typ) {
			return name, true
		}
	}
	return "", false
}

var scalars = map[reflect.Type]string{
	reflect.TypeOf(bool(false)): ScalarBoolean,

	// A GraphQL Int is a 32-bit signed integer, so only Go types that fit in
	// one map to it.
	reflect.TypeOf(int8(0)):   ScalarInt,
	reflect.TypeOf(int16(0)):  ScalarInt,
	reflect.TypeOf(int32(0)):  ScalarInt,
	reflect.TypeOf(uint8(0)):  ScalarInt,
	reflect.TypeOf(uint16(0)): ScalarInt,

	// Everything wider goes to Int64. Go's int is 64-bit on every platform this
	// library targets, so it belongs here rather than in Int.
	reflect.TypeOf(int(0)):    ScalarInt64,
	reflect.TypeOf(int64(0)):  ScalarInt64,
	reflect.TypeOf(uint(0)):   ScalarInt64,
	reflect.TypeOf(uint32(0)): ScalarInt64,
	reflect.TypeOf(uint64(0)): ScalarInt64,

	reflect.TypeOf(float32(0)): ScalarFloat,
	reflect.TypeOf(float64(0)): ScalarFloat,

	reflect.TypeOf(string("")): ScalarString,

	reflect.TypeOf(ID{}):        ScalarID,
	reflect.TypeOf(time.Time{}): ScalarTime,
	reflect.TypeOf([]byte{}):    ScalarBytes,
}

// scalarUnwrappers converts a Go value into the shape a scalar has on the wire.
// A scalar with no entry here is written as encoding/json writes it.
var scalarUnwrappers = map[string]func(interface{}) (interface{}, error){
	ScalarInt64: unwrapInt64,
	ScalarID:    unwrapID,
}

// newScalar builds the runtime type for a named scalar, attaching its
// serialisation and its description.
func newScalar(name string) *graphql.Scalar {
	return &graphql.Scalar{
		Type:        name,
		Description: scalarDescriptions[name],
		Unwrapper:   scalarUnwrappers[name],
	}
}

// deref follows pointers down to a concrete value, reporting whether the value
// is nil.
func deref(source interface{}) (reflect.Value, bool) {
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
func unwrapInt64(source interface{}) (interface{}, error) {
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
		return nil, fmt.Errorf("cannot serialise %s as %s", v.Type(), ScalarInt64)
	}
}

// unwrapID renders an ID as its underlying string.
func unwrapID(source interface{}) (interface{}, error) {
	v, isNil := deref(source)
	if isNil {
		return nil, nil
	}

	id, ok := v.Interface().(ID)
	if !ok {
		return nil, fmt.Errorf("cannot serialise %s as %s", v.Type(), ScalarID)
	}
	return id.Value, nil
}

// parseInt64Arg reads an Int64 argument. The wire form is a string, but a JSON
// number is accepted too, so long as it is exactly representable — a client
// that sends 5 rather than "5" should not be punished for it, while one that
// sends a value that has already lost precision should be.
func parseInt64Arg(value interface{}) (int64, error) {
	switch value := value.(type) {
	case int64:
		// A literal too large to survive float64 keeps its integer type; see
		// valueToJSON.
		return value, nil
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("not a valid %s: %q", ScalarInt64, value)
		}
		return parsed, nil
	case float64:
		parsed := int64(value)
		if float64(parsed) != value {
			return 0, fmt.Errorf("%v is not an integer, or has already lost precision as a JSON number; send %s as a string", value, ScalarInt64)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("not a valid %s", ScalarInt64)
	}
}

// parseUint64Arg is parseInt64Arg for unsigned destinations, so that values
// above the signed maximum survive.
func parseUint64Arg(value interface{}) (uint64, error) {
	switch value := value.(type) {
	case int64:
		if value < 0 {
			return 0, fmt.Errorf("%d is negative", value)
		}
		return uint64(value), nil
	case string:
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("not a valid %s: %q", ScalarInt64, value)
		}
		return parsed, nil
	case float64:
		parsed := uint64(value)
		if value < 0 || float64(parsed) != value {
			return 0, fmt.Errorf("%v is not an unsigned integer, or has already lost precision as a JSON number; send %s as a string", value, ScalarInt64)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("not a valid %s", ScalarInt64)
	}
}

// parseBoundedInt reads an Int argument into a signed destination of the given
// width.
//
// A GraphQL Int is 32-bit, and nothing narrower has a scalar of its own, so the
// bound is the Go type's. Truncating a value that does not fit, or rounding a
// fractional one, would corrupt an argument silently; both are errors instead.
func parseBoundedInt(value interface{}, bits int) (int64, error) {
	var parsed int64

	switch value := value.(type) {
	case float64:
		parsed = int64(value)
		if float64(parsed) != value {
			return 0, fmt.Errorf("%v is not an integer", value)
		}
	case int64:
		// A literal too wide for float64; see valueToJSON. It cannot fit here
		// either, but the range check below says so properly.
		parsed = value
	default:
		return 0, errors.New("not a number")
	}

	limit := int64(1) << (bits - 1)
	if parsed < -limit || parsed > limit-1 {
		return 0, fmt.Errorf("%d does not fit in a %d-bit signed integer", parsed, bits)
	}
	return parsed, nil
}

// parseBoundedUint is parseBoundedInt for unsigned destinations.
func parseBoundedUint(value interface{}, bits int) (uint64, error) {
	var parsed int64

	switch value := value.(type) {
	case float64:
		parsed = int64(value)
		if float64(parsed) != value {
			return 0, fmt.Errorf("%v is not an integer", value)
		}
	case int64:
		parsed = value
	default:
		return 0, errors.New("not a number")
	}

	if parsed < 0 {
		return 0, fmt.Errorf("%d is negative", parsed)
	}
	if uint64(parsed) > (uint64(1)<<bits)-1 {
		return 0, fmt.Errorf("%d does not fit in a %d-bit unsigned integer", parsed, bits)
	}
	return uint64(parsed), nil
}
