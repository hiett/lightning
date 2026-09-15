package lightning

import (
	"context"
	"encoding"
	"fmt"
	"reflect"

	"github.com/hiett/lightning/graphql"
)

// This file turns a Go type into a GraphQL type.
//
// It is where the library's central convention lives: **the Go type says
// everything**, including nullability, at every level.
//
//	string      String!        a value cannot be absent
//	*string     String         a pointer can be nil
//	[]string    [String!]!
//	[]*string   [String]!      the elements are nullable, the list is not
//	*[]string   [String!]
//	*Task       Task           an object, nullable, as objects usually are
//	Task        Task!
//
// Deriving rather than declaring means a resolver signature is the whole
// declaration: there is no second place to state the type, and so no second
// place for the two to disagree.

var (
	textMarshalerType   = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	textUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
	errorType           = reflect.TypeOf((*error)(nil)).Elem()
	contextType         = reflect.TypeOf((*context.Context)(nil)).Elem()
	bytesType           = reflect.TypeOf([]byte(nil))
)

// graphQLType converts a Go type to the GraphQL type a field of that type has.
//
// at names the declaration being built, so that a failure can say where it came
// from. Types it does not recognise are reported rather than guessed at: an
// unregistered struct is a schema mistake, and naming it is more useful than
// inventing a type for it.
func (b *Builder) graphQLType(goType reflect.Type, at string) (graphql.Type, error) {
	// A pointer makes whatever it points at nullable.
	if goType.Kind() == reflect.Ptr {
		return b.namedOrList(goType.Elem(), at)
	}

	inner, err := b.namedOrList(goType, at)
	if err != nil {
		return nil, err
	}

	// A Go interface value can be nil without being a pointer, so an
	// interface-typed result is nullable for the same reason a pointer is: the
	// resolver is able to return nothing.
	if goType.Kind() == reflect.Interface {
		return inner, nil
	}

	return &graphql.NonNull{Type: inner}, nil
}

// namedOrList handles the non-pointer cases: a list, or a named type.
func (b *Builder) namedOrList(goType reflect.Type, at string) (graphql.Type, error) {
	// A byte slice is the Bytes scalar, not a list of Int.
	if goType == bytesType {
		return newScalar(scalarBytes), nil
	}

	if goType.Kind() == reflect.Slice {
		elem, err := b.graphQLType(goType.Elem(), at)
		if err != nil {
			return nil, err
		}
		return &graphql.List{Type: elem}, nil
	}

	return b.namedType(goType, at)
}

// namedType resolves a Go type to a named GraphQL type: a scalar, or something
// registered on the builder.
func (b *Builder) namedType(goType reflect.Type, at string) (graphql.Type, error) {
	// A custom scalar wins over everything: a Go type registered as one is
	// that scalar, whatever its Kind would otherwise suggest.
	if binding := b.scalarBindingFor(goType); binding != nil {
		return b.customScalarType(binding), nil
	}

	// A registered type wins over the built-in mapping, so an application can
	// declare a defined string type as an enum rather than have it silently be
	// a String.
	if decl := b.declFor(goType); decl != nil {
		built, err := b.buildDecl(decl)
		if err != nil {
			return nil, err
		}
		return built, nil
	}

	if name, ok := scalarFor(goType); ok {
		return newScalar(name), nil
	}

	// A Go interface must have been registered: whether it is a GraphQL
	// interface or a union, and which types belong to it, cannot be worked out
	// from the Go type alone.
	if goType.Kind() == reflect.Interface {
		return nil, fmt.Errorf("%s: the Go interface %s is not registered; declare it with lightning.Interface or lightning.Union", at, typeName(goType))
	}

	// A type that marshals itself to text is a String. This is how time-like
	// and id-like types are supported without registering a scalar for each.
	if implementsTextMarshaler(goType) {
		return textMarshalerScalar(), nil
	}

	if goType.Kind() == reflect.Struct {
		return nil, fmt.Errorf("%s: the Go type %s is not registered; declare it with lightning.Object", at, typeName(goType))
	}

	return nil, fmt.Errorf("%s: cannot express the Go type %s in GraphQL", at, typeName(goType))
}

// textMarshalerScalar is the String a text-marshalling type becomes.
//
// The conversion happens here rather than in the JSON encoder so that a type
// which fails to marshal reports a GraphQL error naming the field, instead of
// failing halfway through writing the response.
func textMarshalerScalar() *graphql.Scalar {
	scalar := newScalar(scalarString)
	scalar.Unwrapper = func(source any) (any, error) {
		value := reflect.ValueOf(source)
		if !value.IsValid() || (value.Kind() == reflect.Ptr && value.IsNil()) {
			return nil, nil
		}
		marshaler, ok := value.Interface().(encoding.TextMarshaler)
		if !ok {
			// A value type whose MarshalText is on the pointer receiver.
			if value.CanAddr() {
				marshaler, ok = value.Addr().Interface().(encoding.TextMarshaler)
			}
			if !ok {
				addressable := reflect.New(value.Type())
				addressable.Elem().Set(value)
				marshaler, ok = addressable.Interface().(encoding.TextMarshaler)
			}
			if !ok {
				return nil, fmt.Errorf("%T does not marshal itself to text", source)
			}
		}
		text, err := marshaler.MarshalText()
		if err != nil {
			return nil, err
		}
		return string(text), nil
	}
	return scalar
}

// implementsTextMarshaler reports whether values of goType, or pointers to
// them, render themselves as text.
func implementsTextMarshaler(goType reflect.Type) bool {
	return goType.Implements(textMarshalerType) || reflect.PointerTo(goType).Implements(textMarshalerType)
}

// typeName renders a Go type for an error message, qualified enough to be
// unambiguous without being noisy.
func typeName(t reflect.Type) string {
	if t == nil {
		return "nil"
	}
	if t.Name() == "" || t.PkgPath() == "" {
		return t.String()
	}
	return t.String()
}

// structOf reduces a Go type to the struct or interface it ultimately refers
// to, reporting whether it found one.
//
// Resolvers hand back *T, T, []*T and so on; the type a declaration is *about*
// is what lies underneath.
func structOf(t reflect.Type) (reflect.Type, bool) {
	for {
		switch t.Kind() {
		case reflect.Ptr, reflect.Slice:
			t = t.Elem()
		case reflect.Struct, reflect.Interface:
			return t, true
		default:
			return t, false
		}
	}
}
