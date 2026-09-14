package schemabuilder

import (
	"encoding"
	"fmt"
	"reflect"

	"github.com/hiett/lightning/graphql"
)

// schemaBuilder is a struct for holding all the graph information for types as
// we build out graphql types for our graphql schema.  Resolved graphQL "types"
// are stored in the type map which we can use to see sections of the graph.
type schemaBuilder struct {
	types        map[reflect.Type]graphql.Type
	typeNames    map[string]reflect.Type
	objects      map[reflect.Type]*Object
	interfaces   map[string]*InterfaceObject
	enumMappings map[reflect.Type]*EnumMapping

	// nodeInterface is the Relay Node interface, present only when at least
	// one type registered as a node.
	nodeInterface  *graphql.Interface
	nodeRootFields map[string]*graphql.Field
	typeCache      map[reflect.Type]cachedType // typeCache maps Go types to GraphQL datatypes
}

// EnumMapping is a representation of an enum that includes both the mapping and
// reverse mapping.
type EnumMapping struct {
	Map        map[string]interface{}
	ReverseMap map[interface{}]string

	// Description documents the enum type; see EnumDescription.
	Description string

	// Descriptions documents individual values; see EnumValueDescriptions.
	Descriptions map[string]string

	// DeprecationReasons marks individual values deprecated; see
	// EnumValueDeprecations.
	DeprecationReasons map[string]string
}

// cachedType is a container for GraphQL datatype and the list of its fields
type cachedType struct {
	argType *graphql.InputObject
	fields  map[string]argField
}

// getType is the "core" function of the GraphQL schema builder.  It takes in a
// reflect type and builds the appropriate graphQL "type".  This includes going
// through struct fields and attached object methods to generate the entire
// graphql graph of possible queries.  This function will be called recursively
// for types as we go through the graph.
func (sb *schemaBuilder) getType(nodeType reflect.Type, forceListEntryNonNull bool) (graphql.Type, error) {
	// Support scalars and optional scalars. Scalars have precedence over structs
	// to have eg. time.Time function as a scalar.
	if typeName, values, ok := sb.getEnum(nodeType); ok {
		enum := &graphql.Enum{
			Type:       typeName,
			Values:     values,
			ReverseMap: sb.enumMappings[nodeType].ReverseMap,
		}
		sb.enumMappings[nodeType].applyDocs(enum)
		return &graphql.NonNull{Type: enum}, nil
	}

	// A *NodeRef is a value being returned through the Node interface.
	if nodeType.Kind() == reflect.Ptr && nodeType.Elem() == nodeRefType {
		if sb.nodeInterface == nil {
			return nil, fmt.Errorf("a field returns *schemabuilder.NodeRef but no type has registered as a node")
		}
		return sb.nodeInterface, nil
	}

	if typeName, ok := getScalar(nodeType); ok {
		return &graphql.NonNull{Type: newScalar(typeName)}, nil
	}
	if nodeType.Kind() == reflect.Ptr {
		if typeName, ok := getScalar(nodeType.Elem()); ok {
			return newScalar(typeName), nil // XXX: prefix typ with "*"
		}
	}

	if nodeType.Implements(textMarshalerType) {
		return sb.getTextMarshalerType(nodeType)
	}

	// Structs
	if nodeType.Kind() == reflect.Struct {
		if err := sb.buildStruct(nodeType); err != nil {
			return nil, err
		}
		return &graphql.NonNull{Type: sb.types[nodeType]}, nil
	}
	if nodeType.Kind() == reflect.Ptr && nodeType.Elem().Kind() == reflect.Struct {
		if err := sb.buildStruct(nodeType.Elem()); err != nil {
			return nil, err
		}
		return sb.types[nodeType.Elem()], nil
	}

	switch nodeType.Kind() {
	case reflect.Slice:
		elementType, err := sb.getType(nodeType.Elem(), forceListEntryNonNull)
		if err != nil {
			return nil, err
		}

		if forceListEntryNonNull {
			// Wrap all slice elements in NonNull.
			if _, ok := elementType.(*graphql.NonNull); !ok {
				elementType = &graphql.NonNull{Type: elementType}
			}
		}

		return &graphql.NonNull{Type: &graphql.List{Type: elementType}}, nil

	default:
		return nil, fmt.Errorf("bad type %s: should be a scalar, slice, or struct type", nodeType)
	}
}

// getTextMarshalerType returns a graphQL type that can be used to parse a
// encoding.TextMarshaler and convert it's value into a string in the graphQL
// response.
func (sb *schemaBuilder) getTextMarshalerType(typ reflect.Type) (graphql.Type, error) {
	scalar := &graphql.Scalar{
		Type: ScalarString,
		Unwrapper: func(source interface{}) (interface{}, error) {
			i := reflect.ValueOf(source)
			if i.Kind() == reflect.Ptr && i.IsNil() {
				return "", nil
			}
			marshalVal, ok := i.Interface().(encoding.TextMarshaler)
			if !ok {
				return nil, fmt.Errorf("cannot convert field to text")
			}
			val, err := marshalVal.MarshalText()
			if err != nil {
				return nil, err
			}
			return string(val), nil
		},
	}
	if typ.Kind() == reflect.Ptr {
		return scalar, nil
	}
	return &graphql.NonNull{Type: scalar}, nil
}

// getEnum gets the Enum type information for the passed in reflect.Type by
// looking it up in our enum mappings.
func (sb *schemaBuilder) getEnum(typ reflect.Type) (string, []string, bool) {
	if sb.enumMappings[typ] != nil {
		var values []string
		for mapping := range sb.enumMappings[typ].Map {
			values = append(values, mapping)
		}
		return typ.Name(), values, true
	}
	return "", nil, false
}
