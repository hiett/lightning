package schemabuilder

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/hiett/lightning/graphql"
)

// Interface is a special marker struct that can be embedded into a struct to
// denote that the type should be treated as a GraphQL interface. The struct's
// embedded pointer fields name the object types that implement it.
//
// For example, an interface implemented by *User and *Post looks like:
//
//	type Node struct {
//	  schemabuilder.Interface
//	  *User
//	  *Post
//	}
//
// This mirrors [Union]: a field returning an interface returns this struct with
// exactly one member set, and the member that is set is the concrete type the
// selection resolves against.
//
// By default the interface exposes every field that all of its implementing
// types share under the same name with the same type. Call
// (*InterfaceObject).Fields to declare the field set explicitly instead, which
// is what an interface with a contract — Relay's `Node`, say — wants.
type Interface struct{}

var interfaceMarkerType = reflect.TypeOf(Interface{})

// InterfaceObject holds the schema-level configuration for an interface type.
// It is returned by (*Schema).Interface.
type InterfaceObject struct {
	Name        string
	Type        interface{}
	Description string

	// fieldNames, when non-nil, is the explicit field set of the interface.
	fieldNames []string
}

// Fields declares the interface's field set explicitly, replacing the default
// of "every field all implementing types share".
//
// Every named field must exist on every implementing type with an identical
// type and arguments, or building the schema fails.
func (o *InterfaceObject) Fields(names ...string) *InterfaceObject {
	o.fieldNames = append(o.fieldNames, names...)
	return o
}

// Describe sets the interface's description, which introspection reports and
// exported SDL carries.
func (o *InterfaceObject) Describe(description string) *InterfaceObject {
	o.Description = description
	return o
}

// Interface registers configuration for an interface type. typ must be a struct
// embedding [Interface].
//
// Registration is optional: a struct embedding the marker is recognised as an
// interface wherever it is used. Register it to give the interface a name other
// than its Go type name, a description, or an explicit field set.
func (s *Schema) Interface(name string, typ interface{}, options ...InterfaceOption) *InterfaceObject {
	if existing, ok := s.interfaces[name]; ok {
		if reflect.TypeOf(existing.Type) != reflect.TypeOf(typ) {
			panic("re-registered interface with different type")
		}
		return existing
	}

	object := &InterfaceObject{Name: name, Type: typ}
	s.interfaces[name] = object

	for _, opt := range options {
		opt(object)
	}
	return object
}

// InterfaceOption configures an interface at registration time.
type InterfaceOption func(*InterfaceObject)

// InterfaceFields is the InterfaceOption form of (*InterfaceObject).Fields.
func InterfaceFields(names ...string) InterfaceOption {
	return func(o *InterfaceObject) { o.Fields(names...) }
}

// InterfaceDescription is the InterfaceOption form of
// (*InterfaceObject).Describe.
func InterfaceDescription(description string) InterfaceOption {
	return func(o *InterfaceObject) { o.Describe(description) }
}

// hasInterfaceMarkerEmbedded determines if a struct has an embedded
// schemabuilder.Interface field.
func hasInterfaceMarkerEmbedded(typ reflect.Type) bool {
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Anonymous && field.Type == interfaceMarkerType {
			return true
		}
	}
	return false
}

// interfaceMember records one implementing type and where to find it in the
// marker struct.
type interfaceMember struct {
	object     *graphql.Object
	fieldIndex int
}

// buildInterfaceStruct builds a graphql.Interface from a struct embedding the
// Interface marker.
func (sb *schemaBuilder) buildInterfaceStruct(typ reflect.Type) error {
	name := typ.Name()
	if name == "" {
		return fmt.Errorf("bad type %s: should have a name", typ)
	}

	var registered *InterfaceObject
	for _, candidate := range sb.interfaces {
		if reflect.TypeOf(candidate.Type) == typ {
			registered = candidate
			break
		}
	}
	if registered != nil {
		name = registered.Name
	}

	if originalType, ok := sb.typeNames[name]; ok {
		return fmt.Errorf("duplicate name %s: seen both %v and %v", name, originalType, typ)
	}

	iface := &graphql.Interface{
		Name:          name,
		Fields:        make(map[string]*graphql.Field),
		PossibleTypes: make(map[string]*graphql.Object),
	}
	if registered != nil {
		iface.Description = registered.Description
	}
	// Register before walking members so a member that refers back to the
	// interface does not recurse forever.
	sb.types[typ] = iface
	sb.typeNames[name] = typ

	members := make([]interfaceMember, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" || (field.Anonymous && field.Type == interfaceMarkerType) {
			continue
		}
		if !field.Anonymous {
			return fmt.Errorf("bad type %s: interface member types must be anonymous", name)
		}

		// Pass forceListEntryNonNull as true to match the union builder.
		memberType, err := sb.getType(field.Type, true)
		if err != nil {
			return err
		}

		object, ok := memberType.(*graphql.Object)
		if !ok {
			return fmt.Errorf("bad type %s: interface member must be a pointer to a struct, received %s", name, memberType.String())
		}
		if iface.PossibleTypes[object.Name] != nil {
			return fmt.Errorf("bad type %s: interface member %s may only appear once", name, object.Name)
		}

		iface.PossibleTypes[object.Name] = object
		members = append(members, interfaceMember{object: object, fieldIndex: i})

		if object.Interfaces == nil {
			object.Interfaces = make(map[string]*graphql.Interface)
		}
		object.Interfaces[name] = iface
	}

	if len(members) == 0 {
		return fmt.Errorf("bad type %s: an interface must have at least one implementing type", name)
	}

	fields, err := interfaceFields(name, members, registered)
	if err != nil {
		return err
	}
	iface.Fields = fields

	iface.TypeResolver = interfaceTypeResolver(name, members)

	return nil
}

// interfaceFields works out the interface's field set: the explicitly declared
// names if there are any, otherwise every field all members agree on.
func interfaceFields(name string, members []interfaceMember, registered *InterfaceObject) (map[string]*graphql.Field, error) {
	first := members[0].object

	if registered == nil || registered.fieldNames == nil {
		shared := make(map[string]*graphql.Field)
		names := make([]string, 0, len(first.Fields))
		for fieldName := range first.Fields {
			names = append(names, fieldName)
		}
		sort.Strings(names)

		for _, fieldName := range names {
			if agreed, ok := fieldAgreedByAll(fieldName, members); ok {
				shared[fieldName] = agreed
			}
		}
		return shared, nil
	}

	declared := make(map[string]*graphql.Field, len(registered.fieldNames))
	for _, fieldName := range registered.fieldNames {
		agreed, ok := fieldAgreedByAll(fieldName, members)
		if !ok {
			return nil, fmt.Errorf(
				"bad interface %s: field %q must exist on every implementing type with the same type and arguments",
				name, fieldName)
		}
		declared[fieldName] = agreed
	}
	return declared, nil
}

// fieldAgreedByAll returns the field if every member declares it with an
// identical signature.
func fieldAgreedByAll(fieldName string, members []interfaceMember) (*graphql.Field, bool) {
	first, ok := members[0].object.Fields[fieldName]
	if !ok {
		return nil, false
	}

	for _, member := range members[1:] {
		other, ok := member.object.Fields[fieldName]
		if !ok || !sameFieldSignature(first, other) {
			return nil, false
		}
	}
	return first, true
}

// sameFieldSignature reports whether two fields present the same contract: the
// same result type and the same arguments.
func sameFieldSignature(a, b *graphql.Field) bool {
	if a.Type.String() != b.Type.String() {
		return false
	}
	if len(a.Args) != len(b.Args) {
		return false
	}
	for argName, argType := range a.Args {
		otherType, ok := b.Args[argName]
		if !ok || argType.String() != otherType.String() {
			return false
		}
	}
	return true
}

// interfaceTypeResolver builds the function the executor uses to find which
// concrete type a value of the interface carries.
//
// Members are matched by struct field index rather than by name, so an object
// registered under a GraphQL name different from its Go type name still works.
func interfaceTypeResolver(name string, members []interfaceMember) func(interface{}) (string, interface{}, error) {
	return func(source interface{}) (string, interface{}, error) {
		value := reflect.ValueOf(source)
		for value.Kind() == reflect.Ptr {
			if value.IsNil() {
				return "", nil, nil
			}
			value = value.Elem()
		}
		if !value.IsValid() || value.Kind() != reflect.Struct {
			return "", nil, nil
		}

		found := ""
		var concrete interface{}
		for _, member := range members {
			inner := value.Field(member.fieldIndex)
			if inner.IsNil() {
				continue
			}
			if found != "" {
				return "", nil, fmt.Errorf(
					"interface %s should carry exactly one type, but received both %s and %s",
					name, found, member.object.Name)
			}
			found = member.object.Name
			concrete = inner.Interface()
		}
		return found, concrete, nil
	}
}
