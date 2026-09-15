package lightning

import (
	"fmt"
	"reflect"

	"github.com/hiett/lightning/graphql"
)

// This file registers a Go type as a GraphQL scalar.
//
// The built-in mapping covers what Go's own types mean — a string is a String,
// an int64 is an Int64. A custom scalar is for a Go type that carries something
// the wire format flattens: relay's GID travels as an ID and arrives decoded,
// so a resolver receives the type name and local identifier rather than a
// string it has to decode itself.

// scalarBinding is a Go type registered as a scalar.
type scalarBinding struct {
	name   string
	encode func(any) (any, error)
	decode func(any) (any, error)
}

// Scalar registers the Go type T as a custom GraphQL scalar named name.
//
// encode turns a Go value into what a client receives; decode turns what a
// client sends into a Go value. A decode error is reported to the client, so it
// should say what was wrong with the input.
func Scalar[T any](b *Builder, name string, encode func(T) (any, error), decode func(any) (T, error)) {
	ScalarAs[T](b, name, encode, decode)
}

// ScalarAs registers the Go type T under an existing scalar name, which is how
// a type gets the wire form of a built-in scalar while being something richer
// in Go.
//
// relay.GID is registered as an ID: it travels as an ID and is documented as
// one, and the Go value a resolver receives is the decoded identifier.
func ScalarAs[T any](b *Builder, name string, encode func(T) (any, error), decode func(any) (T, error)) {
	goType := reflect.TypeFor[T]()

	if b.scalars == nil {
		b.scalars = map[reflect.Type]*scalarBinding{}
	}
	if existing, taken := b.scalars[goType]; taken {
		b.errorf("%s is registered as the scalar %s and again as %s", typeName(goType), existing.name, name)
		return
	}
	if _, declared := b.decls[goType]; declared {
		b.errorf("%s is declared as a type and cannot also be a scalar", typeName(goType))
		return
	}

	b.scalars[goType] = &scalarBinding{
		name: name,
		encode: func(value any) (any, error) {
			typed, ok := value.(T)
			if !ok {
				// A pointer to T arrives when the field is nullable.
				rv := reflect.ValueOf(value)
				if rv.Kind() == reflect.Ptr {
					if rv.IsNil() {
						return nil, nil
					}
					typed, ok = rv.Elem().Interface().(T)
				}
				if !ok {
					return nil, fmt.Errorf("cannot serialise %T as %s", value, name)
				}
			}
			return encode(typed)
		},
		decode: func(value any) (any, error) {
			out, err := decode(value)
			if err != nil {
				return nil, err
			}
			return out, nil
		},
	}
}

// scalarBindingFor returns the custom scalar registered for a Go type.
func (b *Builder) scalarBindingFor(goType reflect.Type) *scalarBinding {
	if b.scalars == nil {
		return nil
	}
	return b.scalars[goType]
}

// customScalarType builds the runtime type for a custom scalar.
func (b *Builder) customScalarType(binding *scalarBinding) graphql.Type {
	scalar := newScalar(binding.name)
	scalar.Unwrapper = binding.encode
	return scalar
}

// ScalarType returns the runtime type for one of the built-in scalars, for a
// plugin assembling a field by hand.
func ScalarType(name string) *graphql.Scalar { return newScalar(name) }
