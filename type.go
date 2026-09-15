package lightning

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/hiett/lightning/graphql"
)

// Type is a handle on a declared GraphQL type, carrying the Go type behind it.
//
// Every field declared through a Type[T] has its resolver checked against T by
// the compiler, so a resolver written for the wrong parent type does not build.
type Type[T any] struct {
	b    *Builder
	decl *typeDecl
}

// Object declares the Go type T as a GraphQL object type.
//
// The name and description come from an embedded lightning.Meta marker if the
// struct has one, and from the Go type's own name otherwise. Exported struct
// fields become GraphQL fields, so a plain data type needs no field
// declarations at all:
//
//	type Task struct {
//	    lightning.Meta `graphql:"Task" description:"A unit of work."`
//
//	    Title string `description:"What needs doing."`
//	    Done  bool   `description:"Whether it has been done."`
//	}
//
//	lightning.Object[Task](b)
func Object[T any](b *Builder) *Type[T] {
	goType := reflect.TypeFor[T]()
	if goType.Kind() == reflect.Ptr {
		goType = goType.Elem()
	}
	if goType.Kind() != reflect.Struct {
		b.errorf("lightning.Object: %s is not a struct; an object type is backed by a struct", typeName(goType))
		return &Type[T]{b: b, decl: &typeDecl{name: typeName(goType), goType: goType}}
	}

	decl := b.declare(goType, kindObject)
	decl.exposeAll = true
	return &Type[T]{b: b, decl: decl}
}

// declare registers a Go type, or returns the declaration it already has.
func (b *Builder) declare(goType reflect.Type, kind declKind) *typeDecl {
	if existing, ok := b.decls[goType]; ok {
		if existing.kind != kind {
			b.errorf("%s is declared as %s and again as %s", typeName(goType), existing.kind, kind)
		}
		return existing
	}

	docs := readTypeDocs(goType)
	name := docs.name
	if name == "" {
		name = goType.Name()
	}
	if name == "" {
		b.errorf("lightning: an anonymous type cannot be declared; give %s a name", goType.String())
		name = "Anonymous"
	}

	decl := &typeDecl{
		name:        name,
		description: docs.description,
		kind:        kind,
		goType:      goType,
		hidden:      map[string]bool{},
	}
	b.decls[goType] = decl
	b.order = append(b.order, decl)
	return decl
}

// Describe sets the type's description. A type documented on its embedded
// marker needs no such call; this is for a type declared without one.
func (t *Type[T]) Describe(description string) *Type[T] {
	t.decl.description = description
	return t
}

// Name overrides the GraphQL name of the type.
func (t *Type[T]) Name(name string) *Type[T] {
	t.decl.name = name
	return t
}

// Hide removes struct fields from the schema by their Go names.
//
// A field can also be hidden where it is declared, with `graphql:"-"`, which is
// usually better: the reader of the struct can see that it is not exposed.
func (t *Type[T]) Hide(goFieldNames ...string) *Type[T] {
	for _, name := range goFieldNames {
		t.decl.hidden[name] = true
	}
	return t
}

// Fields returns the declared fields, for a plugin that needs to inspect them.
func (t *Type[T]) Fields() []FieldInfo {
	out := make([]FieldInfo, 0, len(t.decl.fields))
	for _, f := range t.decl.fields {
		out = append(out, FieldInfo{decl: f})
	}
	return out
}

// GraphQLName returns the name this type has in the schema.
func (t *Type[T]) GraphQLName() string { return t.decl.name }

// Query returns the handle for the query root type.
func (b *Builder) Query() *Type[Root] { return b.root(opQuery) }

// Mutation returns the handle for the mutation root type.
func (b *Builder) Mutation() *Type[Root] { return b.root(opMutation) }

// Subscription returns the handle for the subscription root type.
//
// A subscription in lightning is a live query: the operation runs like a query
// and runs again whenever a resource it read is invalidated.
func (b *Builder) Subscription() *Type[Root] { return b.root(opSubscription) }

// rootGoTypes give each operation root a distinct Go identity, so the three
// share the Root parent type without sharing a declaration.
type queryRoot struct{ Root }
type mutationRoot struct{ Root }
type subscriptionRoot struct{ Root }

func (b *Builder) root(op operation) *Type[Root] {
	if decl, ok := b.roots[op]; ok {
		return &Type[Root]{b: b, decl: decl}
	}

	var goType reflect.Type
	switch op {
	case opQuery:
		goType = reflect.TypeFor[queryRoot]()
	case opMutation:
		goType = reflect.TypeFor[mutationRoot]()
	default:
		goType = reflect.TypeFor[subscriptionRoot]()
	}

	decl := b.declare(goType, kindObject)
	decl.name = op.String()
	// A root's fields are all declared; it has no data of its own to expose.
	decl.exposeAll = false
	b.roots[op] = decl
	return &Type[Root]{b: b, decl: decl}
}

// buildDecl constructs the runtime type for a declaration, once.
func (b *Builder) buildDecl(decl *typeDecl) (graphql.Type, error) {
	if built, ok := b.built[decl]; ok {
		return built, nil
	}
	if decl.building {
		// A cycle that reached here rather than through the cache means a type
		// contains itself by value, which has no finite representation.
		return nil, fmt.Errorf("%s contains itself; break the cycle with a pointer or a slice", decl.name)
	}
	decl.building = true
	defer func() { decl.building = false }()

	switch decl.kind {
	case kindObject:
		return b.buildObject(decl)
	case kindInterface:
		return b.buildInterface(decl)
	case kindUnion:
		return b.buildUnion(decl)
	case kindEnum:
		return b.buildEnum(decl)
	case kindInput:
		return b.buildInput(decl)
	default:
		return nil, fmt.Errorf("%s has an unknown kind", decl.name)
	}
}

// buildObject constructs an object type: its struct fields, then its declared
// fields.
func (b *Builder) buildObject(decl *typeDecl) (graphql.Type, error) {
	object := &graphql.Object{
		Name:        decl.name,
		Description: decl.description,
		Fields:      map[string]*graphql.Field{},
		Interfaces:  map[string]*graphql.Interface{},
	}
	// Cache before building fields, so a field that refers back to this type
	// finds it rather than recursing.
	b.built[decl] = object

	// A struct's own fields are turned into declarations rather than built
	// directly, so that there is one path a field can take: one place the name
	// is checked, one place a plugin is shown it, one place it is recorded as
	// sortable.
	fields := decl.fields
	if decl.exposeAll {
		exposed, err := b.structFields(decl, decl.goType, nil)
		if err != nil {
			return nil, err
		}
		fields = append(exposed, fields...)
	}

	for _, field := range fields {
		if err := checkFieldName(decl.name, field.name); err != nil {
			return nil, err
		}
		built, err := b.buildField(decl, field)
		if err != nil {
			return nil, err
		}
		if _, taken := object.Fields[field.name]; taken {
			return nil, fmt.Errorf("%s declares the field %s twice", decl.name, field.name)
		}
		object.Fields[field.name] = built
		if field.sortable {
			decl.sortable = append(decl.sortable, field.name)
		}
		if field.filterable {
			decl.filterable = append(decl.filterable, field.name)
		}
	}
	sort.Strings(decl.sortable)
	sort.Strings(decl.filterable)

	if len(object.Fields) == 0 {
		return nil, fmt.Errorf("%s has no fields; a GraphQL type needs at least one", decl.name)
	}

	object.KeyField = decl.keyField

	return object, nil
}

// structFields turns a struct's own exported fields into field declarations.
//
// This is the reason a plain data type needs no declarations: the struct is
// already a complete description of itself. They become declarations rather
// than finished fields so that every field takes one path — one place the name
// is checked, one place a plugin is shown it, one place it is recorded as
// sortable.
//
// The declaration the fields belong to is passed separately from the struct
// being walked, because an embedded struct contributes its fields to the type
// that embeds it, and `at` is the index path from the outer struct to the one
// being walked — which is how a promoted field is read.
func (b *Builder) structFields(decl *typeDecl, goType reflect.Type, at []int) ([]*fieldDecl, error) {
	var out []*fieldDecl

	for i := 0; i < goType.NumField(); i++ {
		field := goType.Field(i)

		if isMarkerField(field) {
			continue
		}
		// Unexported fields are not part of the type's public shape.
		if field.PkgPath != "" {
			continue
		}
		if decl.hidden[field.Name] {
			continue
		}

		docs, err := readFieldDocs(field)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", decl.name, err)
		}
		if docs.skip {
			continue
		}

		path := append(append([]int(nil), at...), i)

		// An embedded struct contributes its fields to the outer type, as Go
		// promotes them, and each is read through the path that reaches it.
		_, named := field.Tag.Lookup("graphql")
		if field.Anonymous && field.Type.Kind() == reflect.Struct && !named {
			inner := b.declare(field.Type, kindObject)
			inner.exposeAll = true
			promoted, err := b.structFields(decl, inner.goType, path)
			if err != nil {
				return nil, err
			}
			out = append(out, promoted...)
			continue
		}

		out = append(out, &fieldDecl{
			name:        docs.name,
			description: docs.description,
			deprecated:  docs.deprecated,
			goResult:    field.Type,
			sortable:    docs.sortable,
			filterable:  docs.filterable,
			source:      fmt.Sprintf("%s.%s", typeName(goType), field.Name),
			meta:        map[string]any{},
			resolve: func(ctx ctxAlias, source, _ any, _ *graphql.SelectionSet) (any, error) {
				value := reflect.ValueOf(source)
				for value.Kind() == reflect.Ptr {
					if value.IsNil() {
						return nil, nil
					}
					value = value.Elem()
				}
				return value.FieldByIndex(path).Interface(), nil
			},
		})
	}

	return out, nil
}

// fieldNames returns every field name the type will have, from its declarations
// and from the struct fields it exposes.
//
// A plugin asking whether a type already has a field needs both: the answer
// "no" followed by a collision at build time is worse than no answer at all.
func (b *Builder) fieldNames(decl *typeDecl) map[string]bool {
	names := make(map[string]bool, len(decl.fields))
	for _, field := range decl.fields {
		names[field.name] = true
	}
	if decl.exposeAll && decl.goType != nil && decl.goType.Kind() == reflect.Struct {
		exposed, err := b.structFields(decl, decl.goType, nil)
		if err != nil {
			return names
		}
		for _, field := range exposed {
			names[field.name] = true
		}
	}
	return names
}

// executorFieldNames are the names the executor answers itself, whatever a type
// says. A field declared under one of them would never be called, so declaring
// one is an error rather than a silent no-op.
var executorFieldNames = map[string]string{
	"__typename": "the executor answers it with the concrete type's name",
	"__key":      "the live-query diff supplies it from the type's key",
}

// checkFieldName rejects a field the executor would shadow.
func checkFieldName(typeName, fieldName string) error {
	if why, reserved := executorFieldNames[fieldName]; reserved {
		return fmt.Errorf("%s declares the field %s, which is reserved: %s", typeName, fieldName, why)
	}
	return nil
}

// noArguments is the argument parser for a field that takes none.
func noArguments(args any) (any, error) {
	if m, ok := args.(map[string]any); ok && len(m) > 0 {
		names := sortedFieldNames(m)
		return nil, fmt.Errorf("no arguments expected, got %v", names)
	}
	return nil, nil
}
