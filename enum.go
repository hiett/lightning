package lightning

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/hiett/lightning/graphql"
)

// Enum declares the Go type T as a GraphQL enum.
//
// The map gives each GraphQL value name the Go value behind it:
//
//	type Status int32
//	const (
//	    StatusTodo Status = iota
//	    StatusDone
//	)
//
//	lightning.Enum(b, "TaskStatus", map[string]Status{
//	    "TODO": StatusTodo,
//	    "DONE": StatusDone,
//	})
//
// A field returning Status then has the enum type, with no further declaration.
func Enum[T comparable](b *Builder, name string, values map[string]T) *EnumType[T] {
	goType := reflect.TypeFor[T]()
	decl := b.declare(goType, kindEnum)
	decl.name = name
	decl.enumGoValues = map[string]reflect.Value{}

	names := make([]string, 0, len(values))
	for valueName := range values {
		names = append(names, valueName)
	}
	sort.Strings(names)

	seen := map[T]string{}
	for _, valueName := range names {
		value := values[valueName]
		if other, taken := seen[value]; taken {
			b.errorf("enum %s: %s and %s are the same Go value", name, other, valueName)
			continue
		}
		seen[value] = valueName

		decl.enumValues = append(decl.enumValues, enumValue{name: valueName, value: reflect.ValueOf(value)})
		decl.enumGoValues[valueName] = reflect.ValueOf(value)
	}

	if len(decl.enumValues) == 0 {
		b.errorf("enum %s has no values", name)
	}

	return &EnumType[T]{b: b, decl: decl}
}

// EnumType is a handle on a declared enum.
type EnumType[T comparable] struct {
	b    *Builder
	decl *typeDecl
}

// Describe sets the enum's description.
func (e *EnumType[T]) Describe(description string) *EnumType[T] {
	e.decl.description = description
	return e
}

// Value documents one of the enum's values.
func (e *EnumType[T]) Value(name string) *EnumValue[T] {
	for i := range e.decl.enumValues {
		if e.decl.enumValues[i].name == name {
			return &EnumValue[T]{decl: &e.decl.enumValues[i]}
		}
	}
	e.b.errorf("enum %s has no value named %s", e.decl.name, name)
	return &EnumValue[T]{decl: &enumValue{}}
}

// EnumValue is one value of an enum, returned so that it can be documented.
type EnumValue[T comparable] struct{ decl *enumValue }

// Describe sets the value's description.
func (v *EnumValue[T]) Describe(description string) *EnumValue[T] {
	v.decl.description = description
	return v
}

// Deprecate marks the value deprecated.
func (v *EnumValue[T]) Deprecate(reason string) *EnumValue[T] {
	if reason == "" {
		reason = DefaultDeprecationReason
	}
	v.decl.deprecated = reason
	return v
}

// buildEnum constructs an enum type.
func (b *Builder) buildEnum(decl *typeDecl) (graphql.Type, error) {
	enum := &graphql.Enum{
		Type:               decl.name,
		Description:        decl.description,
		ReverseMap:         map[any]string{},
		Descriptions:       map[string]string{},
		DeprecationReasons: map[string]string{},
	}

	for _, value := range decl.enumValues {
		enum.Values = append(enum.Values, value.name)
		enum.ReverseMap[value.value.Interface()] = value.name
		if value.description != "" {
			enum.Descriptions[value.name] = value.description
		}
		if value.deprecated != "" {
			enum.DeprecationReasons[value.name] = value.deprecated
		}
	}

	if len(enum.Values) == 0 {
		return nil, fmt.Errorf("enum %s has no values", decl.name)
	}

	b.built[decl] = enum
	return enum, nil
}

// buildInput constructs an input object type.
func (b *Builder) buildInput(decl *typeDecl) (graphql.Type, error) {
	input := &graphql.InputObject{
		Name:              decl.name,
		Description:       decl.description,
		InputFields:       map[string]graphql.Type{},
		FieldDescriptions: map[string]string{},
	}
	b.built[decl] = input

	goType := decl.goType
	for i := 0; i < goType.NumField(); i++ {
		field := goType.Field(i)
		if isMarkerField(field) || field.PkgPath != "" {
			continue
		}

		docs, err := readFieldDocs(field)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", decl.name, err)
		}
		if docs.skip {
			continue
		}

		fieldType, err := b.graphQLType(field.Type, fmt.Sprintf("%s.%s", decl.name, docs.name))
		if err != nil {
			return nil, err
		}
		if docs.hasDefault {
			fieldType = unwrapNonNull(fieldType)
		}

		input.InputFields[docs.name] = fieldType
		if docs.description != "" {
			input.FieldDescriptions[docs.name] = docs.description
		}
	}

	if len(input.InputFields) == 0 {
		return nil, fmt.Errorf("the input type %s has no fields", decl.name)
	}

	return input, nil
}
