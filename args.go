package lightning

import (
	"encoding"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/hiett/lightning/graphql"
)

// This file turns an argument struct into GraphQL arguments.
//
// The struct is the declaration. Its fields are the arguments, their Go types
// decide whether each is required, and their tags carry the names, descriptions
// and defaults — so everything an argument has to say about itself is written
// where the argument is:
//
//	type AddTaskArgs struct {
//	    Title   string        `description:"What needs doing."`
//	    OwnerID lightning.ID  `graphql:"ownerId" description:"Who it belongs to."`
//	    Limit   *int32        `description:"How many to return." default:"20"`
//	}

// argField is one parsed argument.
type argField struct {
	name        string
	index       int
	goType      reflect.Type
	optional    bool
	defaultText string
	hasDefault  bool
	parse       func(value any, dest reflect.Value) error
}

// buildArguments derives a field's arguments from the Go struct its resolver
// takes.
func (b *Builder) buildArguments(goType reflect.Type, at string) (map[string]graphql.Type, func(any) (any, error), map[string]string, error) {
	if goType.Kind() == reflect.Ptr {
		return nil, nil, nil, fmt.Errorf("%s: the arguments type %s is a pointer; use the struct itself", at, typeName(goType))
	}
	if goType.Kind() != reflect.Struct {
		return nil, nil, nil, fmt.Errorf("%s: the arguments type %s is not a struct", at, typeName(goType))
	}

	args := map[string]graphql.Type{}
	descriptions := map[string]string{}
	fields := make([]argField, 0, goType.NumField())

	for i := 0; i < goType.NumField(); i++ {
		field := goType.Field(i)
		if isMarkerField(field) || field.PkgPath != "" {
			continue
		}

		docs, err := readFieldDocs(field)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", at, err)
		}
		if docs.skip {
			continue
		}

		// A struct reached through an argument is an input object, and is
		// declared here rather than by the author: an argument's shape is
		// already fully described by the Go type it is read into.
		b.declareInputTypes(field.Type)

		argType, err := b.graphQLType(field.Type, fmt.Sprintf("%s(%s:)", at, docs.name))
		if err != nil {
			return nil, nil, nil, err
		}

		parse, err := b.argParser(field.Type, fmt.Sprintf("%s(%s:)", at, docs.name))
		if err != nil {
			return nil, nil, nil, err
		}

		optional := field.Type.Kind() == reflect.Ptr
		if docs.hasDefault {
			// A default makes an argument optional whatever its Go type: the
			// value is supplied when the client leaves it out.
			optional = true
			argType = unwrapNonNull(argType)
		}

		if _, taken := args[docs.name]; taken {
			return nil, nil, nil, fmt.Errorf("%s: two arguments are both named %s", at, docs.name)
		}

		args[docs.name] = argType
		if docs.description != "" {
			descriptions[docs.name] = docs.description
		}
		fields = append(fields, argField{
			name:        docs.name,
			index:       i,
			goType:      field.Type,
			optional:    optional,
			defaultText: docs.defaultText,
			hasDefault:  docs.hasDefault,
			parse:       parse,
		})
	}

	parse := func(raw any) (any, error) {
		out := reflect.New(goType).Elem()

		values, _ := raw.(map[string]any)
		for _, field := range fields {
			dest := out.Field(field.index)

			value, present := values[field.name]
			if !present || value == nil {
				if field.hasDefault {
					if err := applyDefault(dest, field); err != nil {
						return nil, err
					}
					continue
				}
				if !field.optional {
					return nil, fmt.Errorf("%s: required argument missing", field.name)
				}
				continue
			}

			if err := field.parse(value, dest); err != nil {
				return nil, fmt.Errorf("%s: %s", field.name, err)
			}
		}

		for name := range values {
			known := false
			for _, field := range fields {
				if field.name == name {
					known = true
					break
				}
			}
			if !known {
				return nil, fmt.Errorf("unknown argument %q", name)
			}
		}

		return out.Interface(), nil
	}

	return args, parse, descriptions, nil
}

// applyDefault fills an absent argument from its `default:"..."` tag.
func applyDefault(dest reflect.Value, field argField) error {
	target := dest
	if dest.Kind() == reflect.Ptr {
		target = reflect.New(dest.Type().Elem())
		dest.Set(target)
		target = target.Elem()
	}

	switch target.Kind() {
	case reflect.String:
		target.SetString(field.defaultText)
	case reflect.Bool:
		v, err := strconv.ParseBool(field.defaultText)
		if err != nil {
			return fmt.Errorf("%s: default %q is not a boolean", field.name, field.defaultText)
		}
		target.SetBool(v)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v, err := strconv.ParseInt(field.defaultText, 10, 64)
		if err != nil {
			return fmt.Errorf("%s: default %q is not an integer", field.name, field.defaultText)
		}
		target.SetInt(v)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v, err := strconv.ParseUint(field.defaultText, 10, 64)
		if err != nil {
			return fmt.Errorf("%s: default %q is not an unsigned integer", field.name, field.defaultText)
		}
		target.SetUint(v)
	case reflect.Float32, reflect.Float64:
		v, err := strconv.ParseFloat(field.defaultText, 64)
		if err != nil {
			return fmt.Errorf("%s: default %q is not a number", field.name, field.defaultText)
		}
		target.SetFloat(v)
	default:
		return fmt.Errorf("%s: a default cannot be expressed for %s", field.name, typeName(target.Type()))
	}
	return nil
}

// declareInputTypes declares, as input objects, every struct reachable through
// an argument's Go type.
//
// An argument's shape is completely described by the type it is read into, so
// requiring the author to declare it as well would be asking for the same
// information twice.
func (b *Builder) declareInputTypes(goType reflect.Type) {
	for goType.Kind() == reflect.Ptr || goType.Kind() == reflect.Slice {
		if goType == bytesType {
			return
		}
		goType = goType.Elem()
	}

	if goType.Kind() != reflect.Struct || isScalarStruct(goType) {
		return
	}
	if b.scalarBindingFor(goType) != nil {
		return
	}
	if _, declared := b.decls[goType]; declared {
		return
	}

	decl := b.declare(goType, kindInput)
	decl.exposeAll = true

	for i := 0; i < goType.NumField(); i++ {
		field := goType.Field(i)
		if isMarkerField(field) || field.PkgPath != "" {
			continue
		}
		b.declareInputTypes(field.Type)
	}
}

// argParser builds the function that reads one JSON value into a Go value.
func (b *Builder) argParser(goType reflect.Type, at string) (func(any, reflect.Value) error, error) {
	if goType.Kind() == reflect.Ptr {
		inner, err := b.argParser(goType.Elem(), at)
		if err != nil {
			return nil, err
		}
		return func(value any, dest reflect.Value) error {
			target := reflect.New(goType.Elem())
			if err := inner(value, target.Elem()); err != nil {
				return err
			}
			dest.Set(target)
			return nil
		}, nil
	}

	if goType == bytesType {
		return parseBytesArg, nil
	}

	if goType.Kind() == reflect.Slice {
		inner, err := b.argParser(goType.Elem(), at)
		if err != nil {
			return nil, err
		}
		return func(value any, dest reflect.Value) error {
			items, ok := value.([]any)
			if !ok {
				return fmt.Errorf("expected a list")
			}
			out := reflect.MakeSlice(goType, len(items), len(items))
			for i, item := range items {
				if err := inner(item, out.Index(i)); err != nil {
					return fmt.Errorf("[%d]: %s", i, err)
				}
			}
			dest.Set(out)
			return nil
		}, nil
	}

	// A custom scalar decodes itself.
	if binding := b.scalarBindingFor(goType); binding != nil {
		return func(value any, dest reflect.Value) error {
			out, err := binding.decode(value)
			if err != nil {
				return err
			}
			dest.Set(reflect.ValueOf(out))
			return nil
		}, nil
	}

	// An enum argument arrives as its GraphQL value name.
	if decl := b.declFor(goType); decl != nil && decl.kind == kindEnum {
		return func(value any, dest reflect.Value) error {
			name, ok := value.(string)
			if !ok {
				return fmt.Errorf("expected an enum value")
			}
			goValue, ok := decl.enumGoValues[name]
			if !ok {
				return fmt.Errorf("%q is not a value of %s", name, decl.name)
			}
			dest.Set(goValue)
			return nil
		}, nil
	}

	// A nested struct is an input object.
	if goType.Kind() == reflect.Struct && !isScalarStruct(goType) {
		b.declareInputTypes(goType)
		_, parse, _, err := b.buildArguments(goType, at)
		if err != nil {
			return nil, err
		}
		return func(value any, dest reflect.Value) error {
			parsed, err := parse(value)
			if err != nil {
				return err
			}
			dest.Set(reflect.ValueOf(parsed))
			return nil
		}, nil
	}

	if parse, ok := scalarArgParser(goType); ok {
		return parse, nil
	}

	if reflect.PointerTo(goType).Implements(textUnmarshalerType) {
		return func(value any, dest reflect.Value) error {
			text, ok := value.(string)
			if !ok {
				return fmt.Errorf("expected a string")
			}
			target := reflect.New(goType)
			if err := target.Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(text)); err != nil {
				return err
			}
			dest.Set(target.Elem())
			return nil
		}, nil
	}

	return nil, fmt.Errorf("%s: cannot read %s from a GraphQL argument", at, typeName(goType))
}

// isScalarStruct reports whether a struct is one of the scalar-backed types
// rather than an input object.
func isScalarStruct(goType reflect.Type) bool {
	switch goType {
	case reflect.TypeOf(ID{}), reflect.TypeOf(time.Time{}):
		return true
	}
	return reflect.PointerTo(goType).Implements(textUnmarshalerType)
}

func parseBytesArg(value any, dest reflect.Value) error {
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("expected a base64 string")
	}
	decoded, err := base64Decode(text)
	if err != nil {
		return err
	}
	dest.SetBytes(decoded)
	return nil
}
