package graphql

import (
	"context"
	"fmt"
)

// Type represents a GraphQL type, and should be either an Object, a Scalar,
// or a List
type Type interface {
	String() string

	// isType() is a no-op used to tag the known values of Type, to prevent
	// arbitrary interface{} from implementing Type
	isType()
}

// Scalar is a leaf value.  A custom "Unwrapper" can be attached to the scalar
// so it can have a custom unwrapping (if nil we will use the default unwrapper).
type Scalar struct {
	Type        string
	Description string
	Unwrapper   func(interface{}) (interface{}, error)
}

func (s *Scalar) isType() {}

func (s *Scalar) String() string {
	return s.Type
}

// Enum is a leaf value
type Enum struct {
	Type        string
	Description string
	Values      []string
	ReverseMap  map[interface{}]string

	// Descriptions holds documentation for individual enum values, keyed by
	// value name.
	Descriptions map[string]string

	// DeprecationReasons marks individual enum values deprecated, keyed by
	// value name.
	DeprecationReasons map[string]string
}

func (e *Enum) isType() {}

func (e *Enum) String() string {
	return e.Type
}

func (e *Enum) enumValues() []string {
	return e.Values
}

// Object is a value with several fields
type Object struct {
	Name        string
	Description string
	KeyField    *Field
	Fields      map[string]*Field

	// Interfaces holds every interface this object declares it implements,
	// keyed by interface name.
	Interfaces map[string]*Interface

	// Unions holds every union this object is a member of, keyed by union
	// name. A fragment may name a union the enclosing object belongs to, so
	// matching a type condition needs to know about them.
	Unions map[string]*Union
}

func (o *Object) isType() {}

func (o *Object) String() string {
	return o.Name
}

// List is a collection of other values
type List struct {
	Type Type
}

func (l *List) isType() {}

func (l *List) String() string {
	return fmt.Sprintf("[%s]", l.Type)
}

type InputObject struct {
	Name        string
	Description string
	InputFields map[string]Type

	// FieldDescriptions holds documentation for individual input fields, keyed
	// by field name.
	FieldDescriptions map[string]string
}

func (io *InputObject) isType() {}

func (io *InputObject) String() string {
	return io.Name
}

// NonNull is a non-nullable other value
type NonNull struct {
	Type Type
}

func (n *NonNull) isType() {}

func (n *NonNull) String() string {
	return fmt.Sprintf("%s!", n.Type)
}

// Interface is an abstract type: a set of fields that several object types
// each promise to provide.
//
// A value of an interface type is always a value of one of its PossibleTypes;
// which one is decided at execution time by the interface's TypeResolver.
type Interface struct {
	Name        string
	Description string
	Fields      map[string]*Field

	// PossibleTypes holds every object type that implements this interface,
	// keyed by type name.
	PossibleTypes map[string]*Object

	// TypeResolver inspects a value flowing through a field of this interface
	// type and reports which of PossibleTypes it carries, along with the value
	// to resolve for that type. An empty name means the value is absent, and is
	// written as null.
	TypeResolver func(value interface{}) (name string, concrete interface{}, err error)
}

// Implements reports whether an object type implements the named interface.
func (o *Object) Implements(name string) bool {
	_, ok := o.Interfaces[name]
	return ok
}

// FragmentApplies reports whether a fragment with the given type condition
// applies to a value of object type o.
//
// A fragment with no type condition applies to whatever encloses it; otherwise
// the condition must name the object itself, an interface it implements, or a
// union it belongs to.
func FragmentApplies(on string, o *Object) bool {
	if on == "" || o == nil {
		return true
	}
	if on == o.Name {
		return true
	}
	if o.Implements(on) {
		return true
	}
	_, member := o.Unions[on]
	return member
}

func (i *Interface) isType() {}

func (i *Interface) String() string {
	return i.Name
}

// Union is a option between multiple types
type Union struct {
	Name        string
	Description string
	Types       map[string]*Object

	// TypeResolver inspects a value flowing through a field of this union type
	// and reports which of Types it carries, along with the value to resolve
	// for that type. An empty name means the value is absent.
	TypeResolver func(value interface{}) (name string, concrete interface{}, err error)
}

func (*Union) isType() {}

func (u *Union) String() string {
	return u.Name
}

// Verify *Scalar, *Object, *List, *InputObject, and *NonNull implement Type
var _ Type = &Scalar{}
var _ Type = &Object{}
var _ Type = &List{}
var _ Type = &InputObject{}
var _ Type = &NonNull{}
var _ Type = &Enum{}
var _ Type = &Union{}
var _ Type = &Interface{}

// A Resolver calculates the value of a field of an object
type Resolver func(ctx context.Context, source, args interface{}, selectionSet *SelectionSet) (interface{}, error)

// A BatchResolver calculates the value of a field for a slice of objects.
type BatchResolver func(ctx context.Context, sources []interface{}, args interface{}, selectionSet *SelectionSet) ([]interface{}, error)

// Field knows how to compute field values of an Object
//
// Fields are responsible for computing their value themselves.
type Field struct {
	Resolve        Resolver
	BatchResolver  BatchResolver
	Type           Type
	Args           map[string]Type
	ParseArguments func(json interface{}) (interface{}, error)

	// Description is the field's documentation, surfaced by introspection and
	// printed into exported SDL.
	Description string

	// ArgDescriptions holds documentation for individual arguments, keyed by
	// argument name.
	ArgDescriptions map[string]string

	// DeprecationReason, when non-empty, marks the field deprecated.
	DeprecationReason string

	UseBatchFunc func(context.Context) bool
	Batch        bool
	External     bool
	Expensive    bool

	// NumParallelInvocationsFunc controls how many goroutines we'll create for a
	// field execution (batch or non-expensive).  We pass in the number of srcs
	// we're executing with so implementers can write custom logic.
	NumParallelInvocationsFunc func(ctx context.Context, numNodes int) int
}

type Schema struct {
	Query        Type
	Mutation     Type
	Subscription Type
}

// SelectionSet represents a core GraphQL query
//
// A SelectionSet can contain multiple fields and multiple fragments. For
// example, the query
//
//	{
//	  name
//	  ... UserFragment
//	  memberships {
//	    organization { name }
//	  }
//	}
//
// results in a root SelectionSet with two selections (name and memberships),
// and one fragment (UserFragment). The subselection `organization { name }`
// is stored in the memberships selection.
//
// Because GraphQL allows multiple fragments with the same name or alias,
// selections are stored in an array instead of a map.
type SelectionSet struct {
	Selections []*Selection
	Fragments  []*Fragment
}

// ShallowCopy returns a shallow copy of SelectionSet.
func (s *SelectionSet) ShallowCopy() *SelectionSet {
	cp := &SelectionSet{}
	if s.Selections != nil {
		cp.Selections = make([]*Selection, len(s.Selections))
		copy(cp.Selections, s.Selections)
	}
	if s.Fragments != nil {
		cp.Fragments = make([]*Fragment, len(s.Fragments))
		copy(cp.Fragments, s.Fragments)
	}
	return cp
}

// A selection represents a part of a GraphQL query
//
// The selection
//
//	me: user(id: 166) { name }
//
// has name "user" (representing the source field to be queried), alias "me"
// (representing the name to be used in the output), args id: 166 (representing
// arguments passed to the source field to be queried), and subselection name
// representing the information to be queried from the resulting object.
type Selection struct {
	Name         string
	Alias        string
	Args         interface{}
	SelectionSet *SelectionSet
	Directives   []*Directive

	// The parsed flag is used to make sure the args for this Selection are only
	// parsed once.
	parsed bool

	// UnparsedArgs are the original json map[string]interface{} arguments.
	// This field is only available able after PrepareQuery has been called.
	UnparsedArgs map[string]interface{}

	// ParentType is the type that this field hangs off of.
	ParentType string

	// argsByParentType holds arguments parsed against a particular object type.
	//
	// A selection made directly on an interface is resolved against every type
	// that implements it, and each implementation parses arguments into its own
	// Go struct. Args alone cannot express that: it would hold whichever
	// implementation happened to be prepared first, and handing those to a
	// different implementation's resolver is a type error.
	argsByParentType map[string]interface{}
}

// SetArgsForType records arguments parsed against a specific object type.
func (s *Selection) SetArgsForType(typeName string, args interface{}) {
	if s.argsByParentType == nil {
		s.argsByParentType = make(map[string]interface{}, 2)
	}
	s.argsByParentType[typeName] = args
}

// ArgsForType returns the arguments to pass to typeName's resolver, falling
// back to Args for a selection that was only ever prepared against one type.
func (s *Selection) ArgsForType(typeName string) interface{} {
	if args, ok := s.argsByParentType[typeName]; ok {
		return args
	}
	return s.Args
}

// A Fragment represents a reusable part of a GraphQL query
//
// The On part of a Fragment represents the type of source object for which
// this Fragment should be used. That is not currently implemented in this
// package.
type Fragment struct {
	On           string
	SelectionSet *SelectionSet
	Directives   []*Directive
}

// A Directive can be attached to a field or fragment inclusion, and can
// affect execution of the query in any way the server desires.
//
// The selections
//
//	users @skip(if:true)
//	drivers @include(if:true)
//
// would skip the users selection and keep the drivers selection depending
// on the argument passed into the directive
type Directive struct {
	Name string
	Args interface{}
}
