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
	if existing := b.scalars[goType]; existing != nil {
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

// ScalarBinding is how one Go type travels on the wire: the scalar it is sent
// as, and the two functions that convert between them.
type ScalarBinding struct {
	// Name is the GraphQL scalar the type travels as. It may be a built-in.
	Name string
	// Encode turns a Go value into what a client receives.
	Encode func(any) (any, error)
	// Decode turns what a client sends into a Go value, reporting a bad input
	// in terms the client can act on.
	Decode func(any) (any, error)
}

// ScalarShapes registers a function consulted for any Go type the builder does
// not otherwise recognise, so that a plugin can claim a whole family of types
// rather than registering them one at a time.
//
// It exists for a generic type: relay's ID[T] is a different Go type for every
// T, and nothing can enumerate the instantiations an application will use. The
// claim function is given the Go type and returns how it travels, or nil to
// pass.
//
// A type claimed this way is bound the first time it is seen, and the binding
// is kept, so the claim function runs once per type.
func (b *Builder) ScalarShapes(claim func(reflect.Type) *ScalarBinding) {
	b.scalarShapes = append(b.scalarShapes, claim)
}

// scalarBindingFor returns the custom scalar registered for a Go type, asking
// the registered shapes for one the first time a type is seen.
func (b *Builder) scalarBindingFor(goType reflect.Type) *scalarBinding {
	if b.scalars != nil {
		if binding, ok := b.scalars[goType]; ok {
			return binding
		}
	}

	for _, claim := range b.scalarShapes {
		claimed := claim(goType)
		if claimed == nil {
			continue
		}
		binding := &scalarBinding{
			name:   claimed.Name,
			encode: claimed.Encode,
			decode: claimed.Decode,
		}
		if b.scalars == nil {
			b.scalars = map[reflect.Type]*scalarBinding{}
		}
		b.scalars[goType] = binding
		return binding
	}

	// Remembering the miss keeps the claim functions off the hot path for the
	// types they do not want.
	if b.scalars == nil {
		b.scalars = map[reflect.Type]*scalarBinding{}
	}
	b.scalars[goType] = nil
	return nil
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
