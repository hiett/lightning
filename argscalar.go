package lightning

import (
	"encoding/base64"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"time"
)

// This file reads GraphQL argument values into Go values.
//
// Every conversion here is strict. A value that does not fit its destination is
// an error rather than a truncation: silently turning 2147483648 into a
// negative number, or 1.5 into 1, corrupts an argument in a way the client
// cannot see and the server cannot detect later.

func base64Decode(text string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil, fmt.Errorf("not valid base64")
	}
	return decoded, nil
}

// scalarArgParser returns the parser for a scalar Go type.
func scalarArgParser(goType reflect.Type) (func(any, reflect.Value) error, bool) {
	switch goType {
	case reflect.TypeOf(ID{}):
		return parseIDArg, true
	case reflect.TypeOf(time.Time{}):
		return parseTimeArg, true
	}

	switch goType.Kind() {
	case reflect.Bool:
		return func(value any, dest reflect.Value) error {
			v, ok := value.(bool)
			if !ok {
				return fmt.Errorf("expected a boolean")
			}
			dest.SetBool(v)
			return nil
		}, true

	case reflect.String:
		return func(value any, dest reflect.Value) error {
			v, ok := value.(string)
			if !ok {
				return fmt.Errorf("expected a string")
			}
			dest.SetString(v)
			return nil
		}, true

	case reflect.Float32, reflect.Float64:
		return func(value any, dest reflect.Value) error {
			v, ok := asFloat(value)
			if !ok {
				return fmt.Errorf("expected a number")
			}
			dest.SetFloat(v)
			return nil
		}, true

	case reflect.Int, reflect.Int64:
		return func(value any, dest reflect.Value) error {
			v, err := parseInt64Arg(value)
			if err != nil {
				return err
			}
			dest.SetInt(v)
			return nil
		}, true

	case reflect.Int8, reflect.Int16, reflect.Int32:
		bits := goType.Bits()
		return func(value any, dest reflect.Value) error {
			v, err := parseBoundedInt(value, bits)
			if err != nil {
				return err
			}
			dest.SetInt(v)
			return nil
		}, true

	case reflect.Uint, reflect.Uint64, reflect.Uint32:
		return func(value any, dest reflect.Value) error {
			v, err := parseUint64Arg(value)
			if err != nil {
				return err
			}
			if goType.Kind() == reflect.Uint32 && v > math.MaxUint32 {
				return fmt.Errorf("%d does not fit in a 32-bit unsigned integer", v)
			}
			dest.SetUint(v)
			return nil
		}, true

	case reflect.Uint8, reflect.Uint16:
		bits := goType.Bits()
		return func(value any, dest reflect.Value) error {
			v, err := parseBoundedUint(value, bits)
			if err != nil {
				return err
			}
			dest.SetUint(v)
			return nil
		}, true
	}

	return nil, false
}

func parseIDArg(value any, dest reflect.Value) error {
	switch v := value.(type) {
	case string:
		dest.Set(reflect.ValueOf(ID{Value: v}))
		return nil
	case float64:
		// The specification allows an ID to arrive as an integer, and says it
		// is the same identifier as its string form.
		if v != math.Trunc(v) {
			return fmt.Errorf("not a valid ID")
		}
		dest.Set(reflect.ValueOf(ID{Value: strconv.FormatInt(int64(v), 10)}))
		return nil
	case int64:
		dest.Set(reflect.ValueOf(ID{Value: strconv.FormatInt(v, 10)}))
		return nil
	default:
		return fmt.Errorf("not a valid ID")
	}
}

func parseTimeArg(value any, dest reflect.Value) error {
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("expected an RFC 3339 timestamp")
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return fmt.Errorf("not an RFC 3339 timestamp")
	}
	dest.Set(reflect.ValueOf(parsed))
	return nil
}

func asFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int64:
		return float64(v), true
	}
	return 0, false
}

// parseInt64Arg reads an Int64 argument. The wire form is a string, but a JSON
// number is accepted when it is exactly an integer — a client that sends 5
// rather than "5" should not be punished, while one that sends a value that has
// already lost precision should be.
func parseInt64Arg(value any) (int64, error) {
	switch v := value.(type) {
	case int64:
		return v, nil
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("not a valid Int64: %q", v)
		}
		return parsed, nil
	case float64:
		parsed := int64(v)
		if float64(parsed) != v {
			return 0, fmt.Errorf("%v is not an integer, or has already lost precision as a JSON number; send Int64 as a string", v)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("not a valid Int64")
	}
}

func parseUint64Arg(value any) (uint64, error) {
	switch v := value.(type) {
	case int64:
		if v < 0 {
			return 0, fmt.Errorf("%d is negative", v)
		}
		return uint64(v), nil
	case string:
		parsed, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("not a valid Int64: %q", v)
		}
		return parsed, nil
	case float64:
		parsed := uint64(v)
		if v < 0 || float64(parsed) != v {
			return 0, fmt.Errorf("%v is not an unsigned integer, or has already lost precision as a JSON number; send Int64 as a string", v)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("not a valid Int64")
	}
}

// parseBoundedInt reads an Int argument into a signed destination of the given
// width, refusing anything that does not fit.
func parseBoundedInt(value any, bits int) (int64, error) {
	var parsed int64

	switch v := value.(type) {
	case float64:
		parsed = int64(v)
		if float64(parsed) != v {
			return 0, fmt.Errorf("%v is not an integer", v)
		}
	case int64:
		parsed = v
	default:
		return 0, fmt.Errorf("expected a number")
	}

	limit := int64(1) << (bits - 1)
	if parsed < -limit || parsed > limit-1 {
		return 0, fmt.Errorf("%d does not fit in a %d-bit signed integer", parsed, bits)
	}
	return parsed, nil
}

// parseBoundedUint is parseBoundedInt for unsigned destinations.
func parseBoundedUint(value any, bits int) (uint64, error) {
	var parsed int64

	switch v := value.(type) {
	case float64:
		parsed = int64(v)
		if float64(parsed) != v {
			return 0, fmt.Errorf("%v is not an integer", v)
		}
	case int64:
		parsed = v
	default:
		return 0, fmt.Errorf("expected a number")
	}

	if parsed < 0 {
		return 0, fmt.Errorf("%d is negative", parsed)
	}
	if uint64(parsed) > (uint64(1)<<bits)-1 {
		return 0, fmt.Errorf("%d does not fit in a %d-bit unsigned integer", parsed, bits)
	}
	return uint64(parsed), nil
}
