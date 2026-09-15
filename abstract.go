package lightning

import (
	"fmt"
	"reflect"

	"github.com/hiett/lightning/graphql"
)

// This file declares interfaces and unions.
//
// A GraphQL interface is a Go interface. A resolver returns the interface value
// itself, and the concrete Go type behind it decides which object type the
// value is:
//
//	type Actor interface{ DisplayName() string }
//
//	actor := lightning.Interface[Actor](b)
//	lightning.Implements(actor, user, func(u *User) Actor { return u })
//
//	task.Field("owner", func(ctx context.Context, t *Task) (Actor, error) {
//	    return store.Owner(ctx, t.OwnerID)
//	})
//
// There is no marker struct and no one-hot wrapper to build, so there is no way
// to build an invalid one.

// Interface declares the Go interface I as a GraphQL interface type.
//
// Fields declared on it are the interface's contract; every member must provide
// each of them, which is checked when the schema is built.
func Interface[I any](b *Builder) *AbstractType[I] {
	goType := reflect.TypeFor[I]()
	if goType.Kind() != reflect.Interface {
		b.errorf("lightning.Interface: %s is not a Go interface; a GraphQL interface is backed by one", typeName(goType))
		return &AbstractType[I]{b: b, decl: &typeDecl{name: typeName(goType), goType: goType}}
	}
	return &AbstractType[I]{b: b, decl: b.declare(goType, kindInterface)}
}

// AbstractType is a handle on a declared interface or union.
//
// It is distinct from Type because the parent a resolver receives differs: an
// object's resolver takes a *T, an interface's takes the interface value, since
// that is what the caller has.
type AbstractType[I any] struct {
	b    *Builder
	decl *typeDecl
}

// Describe sets the type's description.
func (a *AbstractType[I]) Describe(description string) *AbstractType[I] {
	a.decl.description = description
	return a
}

// Name overrides the GraphQL name of the type.
func (a *AbstractType[I]) Name(name string) *AbstractType[I] {
	a.decl.name = name
	return a
}

// GraphQLName returns the name this type has in the schema.
func (a *AbstractType[I]) GraphQLName() string { return a.decl.name }

// Field declares a field of the interface's contract.
//
// Every member must provide a field of this name and type, which is checked
// when the schema is built.
func (a *AbstractType[I]) Field[R any](name string, resolve func(ctx ctxAlias, parent I) (R, error)) *Field {
	decl := &fieldDecl{
		name:     name,
		goResult: reflect.TypeFor[R](),
		source:   callSite(2),
		meta:     map[string]any{},
		resolve: func(ctx ctxAlias, source, _ any, _ *graphql.SelectionSet) (any, error) {
			parent, ok := source.(I)
			if !ok {
				return nil, nil
			}
			return resolve(ctx, parent)
		},
	}
	a.decl.fields = append(a.decl.fields, decl)
	return &Field{b: a.b, parent: a.decl, decl: decl}
}

// Union declares the Go interface I as a GraphQL union type.
//
// A union differs from an interface in having no fields of its own: a client
// reaches its members through fragments. The Go interface behind it usually has
// no methods.
func Union[I any](b *Builder) *AbstractType[I] {
	goType := reflect.TypeFor[I]()
	if goType.Kind() != reflect.Interface {
		b.errorf("lightning.Union: %s is not a Go interface; a GraphQL union is backed by one", typeName(goType))
		return &AbstractType[I]{b: b, decl: &typeDecl{name: typeName(goType), goType: goType}}
	}
	return &AbstractType[I]{b: b, decl: b.declare(goType, kindUnion)}
}

// Implements registers that the object type T belongs to the abstract type I.
//
// The witness function is the point: writing `func(u *User) Actor { return u }`
// only compiles if *User satisfies Actor, so membership is checked by the
// compiler rather than by reflection at build time, and the error names the
// method that is missing.
//
// Membership is explicit rather than inferred from which Go types happen to
// satisfy the interface. Satisfying an interface by accident is ordinary Go;
// joining a GraphQL interface by accident is not.
func Implements[I, T any](abstract *AbstractType[I], object *Type[T], witness func(*T) I) {
	if abstract.b != object.b {
		abstract.b.errorf("lightning.Implements: %s and %s come from different builders", abstract.decl.name, object.decl.name)
		return
	}
	if abstract.decl.kind != kindInterface && abstract.decl.kind != kindUnion {
		abstract.b.errorf("lightning.Implements: %s is not an interface or a union", abstract.decl.name)
		return
	}
	if object.decl.kind != kindObject {
		abstract.b.errorf("lightning.Implements: %s is not an object type", object.decl.name)
		return
	}

	for _, member := range abstract.decl.members {
		if member == object.decl {
			return
		}
	}
	abstract.decl.members = append(abstract.decl.members, object.decl)
	object.decl.implements = append(object.decl.implements, abstract.decl)
}

// buildInterface constructs an interface type.
func (b *Builder) buildInterface(decl *typeDecl) (graphql.Type, error) {
	iface := &graphql.Interface{
		Name:          decl.name,
		Description:   decl.description,
		Fields:        map[string]*graphql.Field{},
		PossibleTypes: map[string]*graphql.Object{},
	}
	b.built[decl] = iface

	for _, field := range decl.fields {
		built, err := b.buildField(decl, field)
		if err != nil {
			return nil, err
		}
		iface.Fields[field.name] = built
	}

	if len(decl.members) == 0 {
		return nil, fmt.Errorf("the interface %s has no implementing types; register one with lightning.Implements", decl.name)
	}

	// An interface with no declared fields takes the fields its members agree
	// on, so a marker interface still produces a legal type.
	if len(iface.Fields) == 0 {
		shared, err := b.sharedFields(decl)
		if err != nil {
			return nil, err
		}
		if len(shared) == 0 {
			return nil, fmt.Errorf("the interface %s has no fields and its implementing types share none; declare at least one", decl.name)
		}
		iface.Fields = shared
	}

	iface.TypeResolver = b.abstractTypeResolver(decl)
	return iface, nil
}

// buildUnion constructs a union type.
func (b *Builder) buildUnion(decl *typeDecl) (graphql.Type, error) {
	union := &graphql.Union{
		Name:        decl.name,
		Description: decl.description,
		Types:       map[string]*graphql.Object{},
	}
	b.built[decl] = union

	if len(decl.fields) > 0 {
		return nil, fmt.Errorf("the union %s declares fields; a union has none, and its members are reached through fragments", decl.name)
	}
	if len(decl.members) == 0 {
		return nil, fmt.Errorf("the union %s has no member types; register one with lightning.Implements", decl.name)
	}

	union.TypeResolver = b.abstractTypeResolver(decl)
	return union, nil
}

// sharedFields returns the fields every member of an abstract type declares
// identically.
func (b *Builder) sharedFields(decl *typeDecl) (map[string]*graphql.Field, error) {
	first, err := b.memberObject(decl.members[0])
	if err != nil {
		return nil, err
	}

	shared := map[string]*graphql.Field{}
	for _, name := range sortedFieldNames(first.Fields) {
		candidate := first.Fields[name]
		agreed := true
		for _, member := range decl.members[1:] {
			object, err := b.memberObject(member)
			if err != nil {
				return nil, err
			}
			other, ok := object.Fields[name]
			if !ok || other.Type.String() != candidate.Type.String() {
				agreed = false
				break
			}
		}
		if agreed {
			shared[name] = candidate
		}
	}
	return shared, nil
}

// memberObject builds a member declaration and asserts it produced an object.
func (b *Builder) memberObject(decl *typeDecl) (*graphql.Object, error) {
	built, err := b.buildDecl(decl)
	if err != nil {
		return nil, err
	}
	object, ok := unwrapNonNull(built).(*graphql.Object)
	if !ok {
		return nil, fmt.Errorf("%s is not an object type", decl.name)
	}
	return object, nil
}

// abstractTypeResolver decides which member a value belongs to, by its Go type.
func (b *Builder) abstractTypeResolver(decl *typeDecl) func(any) (string, any, error) {
	return func(value any) (string, any, error) {
		if value == nil {
			return "", nil, nil
		}
		rv := reflect.ValueOf(value)
		if rv.Kind() == reflect.Ptr && rv.IsNil() {
			return "", nil, nil
		}

		goType := rv.Type()
		for goType.Kind() == reflect.Ptr {
			goType = goType.Elem()
		}

		for _, member := range decl.members {
			if member.goType == goType {
				return member.name, value, nil
			}
		}

		return "", nil, fmt.Errorf("%s: a value of Go type %s was returned, which is not one of its types; register it with lightning.Implements",
			decl.name, typeName(rv.Type()))
	}
}

// wireAbstractTypes links objects and their abstract types after every type has
// been built, and checks that each member honours its interfaces' contracts.
func (b *Builder) wireAbstractTypes() error {
	for _, decl := range b.order {
		if decl.kind != kindInterface && decl.kind != kindUnion {
			continue
		}

		built, ok := b.built[decl]
		if !ok {
			continue
		}

		for _, member := range decl.members {
			object, err := b.memberObject(member)
			if err != nil {
				b.errs = append(b.errs, err)
				continue
			}

			switch abstract := built.(type) {
			case *graphql.Interface:
				abstract.PossibleTypes[object.Name] = object
				if object.Interfaces == nil {
					object.Interfaces = map[string]*graphql.Interface{}
				}
				object.Interfaces[decl.name] = abstract

				for _, name := range sortedFieldNames(abstract.Fields) {
					want := abstract.Fields[name]
					have, ok := object.Fields[name]
					if !ok {
						// The member inherits the interface's field. Declaring
						// it once, on the interface, is the point of declaring
						// it there: the resolver takes the interface value, and
						// every member satisfies it.
						object.Fields[name] = want
						continue
					}
					if have.Type.String() != want.Type.String() {
						b.errorf("%s.%s is %s, but the interface %s declares it as %s", object.Name, name, have.Type, decl.name, want.Type)
					}
				}

			case *graphql.Union:
				abstract.Types[object.Name] = object
				if object.Unions == nil {
					object.Unions = map[string]*graphql.Union{}
				}
				object.Unions[decl.name] = abstract
			}
		}
	}

	if len(b.errs) > 0 {
		return b.combinedError()
	}
	return nil
}
