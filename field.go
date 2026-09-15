package lightning

import (
	"fmt"
	"reflect"
	"runtime"
	"strings"

	"github.com/hiett/lightning/graphql"
)

// This file declares fields.
//
// A field declaration is its resolver. The GraphQL type comes from what the
// resolver returns, its nullability from whether that is a pointer, and its
// arguments from the struct the resolver takes — so the call site names the
// field and hands over a function, and nothing else:
//
//	task.Field("owner", store.Owner)
//
// The methods here are generic, which Go permits from 1.27. That is what lets
// the result type be inferred from the resolver rather than named again.

// Field declares a field resolved by a function.
//
// R is inferred from the resolver, and decides both the field's GraphQL type
// and its nullability: returning *User gives a nullable User, User gives
// User!, []*User gives [User]!.
//
//	user.Field("manager", func(ctx context.Context, u *User) (*User, error) {
//	    return store.User(ctx, u.ManagerID)
//	})
//
// An existing method of the right shape can be passed directly:
//
//	user.Field("manager", store.Manager)
func (t *Type[T]) Field[R any](name string, resolve func(ctx ctxAlias, parent *T) (R, error)) *Field {
	return t.declareField(name, reflect.TypeFor[R](), nil, func(ctx ctxAlias, source, _ any, _ *graphql.SelectionSet) (any, error) {
		parent, ok := sourceAs[T](source)
		if !ok {
			return nil, nil
		}
		return resolve(ctx, parent)
	})
}

// Attr declares a field computed from the parent alone.
//
// It is Field without the ceremony a pure accessor does not need: no context to
// ignore, no error to return nil for.
//
//	user.Attr("displayName", func(u *User) string { return u.Name })
func (t *Type[T]) Attr[R any](name string, resolve func(parent *T) R) *Field {
	return t.declareField(name, reflect.TypeFor[R](), nil, func(_ ctxAlias, source, _ any, _ *graphql.SelectionSet) (any, error) {
		parent, ok := sourceAs[T](source)
		if !ok {
			return nil, nil
		}
		return resolve(parent), nil
	})
}

// FieldArgs declares a field that takes arguments.
//
// A is inferred from the resolver and never named at the call site. It is an
// ordinary Go struct whose tags carry the arguments' names, documentation and
// defaults:
//
//	type SearchArgs struct {
//	    Term  string `description:"What to look for."`
//	    Limit *int32 `description:"How many to return." default:"20"`
//	}
//
//	q.FieldArgs("search", func(ctx context.Context, _ *lightning.Root, args SearchArgs) ([]*Task, error) {
//	    return store.Search(ctx, args.Term, args.Limit)
//	})
//
// A pointer field is an optional argument; a value field is required.
func (t *Type[T]) FieldArgs[R, A any](name string, resolve func(ctx ctxAlias, parent *T, args A) (R, error)) *Field {
	argsType := reflect.TypeFor[A]()
	return t.declareField(name, reflect.TypeFor[R](), argsType, func(ctx ctxAlias, source, args any, _ *graphql.SelectionSet) (any, error) {
		parent, ok := sourceAs[T](source)
		if !ok {
			return nil, nil
		}
		typed, ok := args.(A)
		if !ok {
			// The parser built by this package always produces an A; anything
			// else means a plugin replaced it with the wrong thing.
			return nil, fmt.Errorf("%s: arguments are %T, expected %s", name, args, typeName(argsType))
		}
		return resolve(ctx, parent, typed)
	})
}

// sourceAs converts an executor-supplied source value to the parent type a
// resolver expects.
//
// The executor hands back whatever the previous resolver returned, which for a
// root is nil and for an object is a *T or a T.
func sourceAs[T any](source any) (*T, bool) {
	switch typed := source.(type) {
	case *T:
		if typed == nil {
			return nil, false
		}
		return typed, true
	case T:
		return &typed, true
	case nil:
		var zero T
		return &zero, true
	}

	// A value reached through an interface, which is how an interface field's
	// concrete type arrives.
	value := reflect.ValueOf(source)
	for value.Kind() == reflect.Ptr {
		if value.IsNil() {
			return nil, false
		}
		value = value.Elem()
	}
	if value.IsValid() && value.Type() == reflect.TypeFor[T]() {
		out, _ := value.Interface().(T)
		return &out, true
	}

	var zero T
	return &zero, true
}

// declareField records a field on the type.
func (t *Type[T]) declareField(name string, goResult, goArgs reflect.Type, resolve graphql.Resolver) *Field {
	decl := &fieldDecl{
		name:     name,
		goResult: goResult,
		goArgs:   goArgs,
		resolve:  resolve,
		source:   callSite(),
		meta:     map[string]any{},
	}

	if name == "" {
		t.b.errorf("%s: a field needs a name (declared at %s)", t.decl.name, decl.source)
	}

	t.decl.fields = append(t.decl.fields, decl)
	return &Field{b: t.b, parent: t.decl, decl: decl}
}

// callSite reports the file and line of the caller's caller, for error
// messages that point at the declaration rather than at this package.
func callSite() string {
	// 0 is callSite, 1 is declareField, 2 is the Field/Attr/FieldArgs method,
	// 3 is the author's code.
	if _, file, line, ok := runtime.Caller(3); ok {
		if i := strings.LastIndex(file, "/"); i >= 0 {
			file = file[i+1:]
		}
		return fmt.Sprintf("%s:%d", file, line)
	}
	return "unknown"
}

// Field is a declared field, returned so that it can be documented.
type Field struct {
	b      *Builder
	parent *typeDecl
	decl   *fieldDecl
}

// Describe sets the field's description, which introspection reports and
// exported SDL carries.
func (f *Field) Describe(description string) *Field {
	f.decl.description = description
	return f
}

// Deprecate marks the field deprecated. An empty reason becomes
// DefaultDeprecationReason.
func (f *Field) Deprecate(reason string) *Field {
	if reason == "" {
		reason = DefaultDeprecationReason
	}
	f.decl.deprecated = reason
	return f
}

// NonNull overrides the nullability derived from the resolver, marking the
// field non-null.
//
// It is rarely the right tool. The Go type is usually right, and when it is
// not, changing the Go type says the same thing to every reader rather than
// only to this one field.
func (f *Field) NonNull() *Field {
	yes := true
	f.decl.nonNull = &yes
	return f
}

// Nullable overrides the nullability derived from the resolver, marking the
// field nullable.
func (f *Field) Nullable() *Field {
	no := false
	f.decl.nonNull = &no
	return f
}

// Expensive marks the field as slow enough to be worth running in parallel with
// its siblings rather than inline.
func (f *Field) Expensive() *Field {
	f.decl.expensive = true
	return f
}

// Meta attaches plugin data to the field. The key should be namespaced by the
// plugin that owns it.
func (f *Field) Meta(key string, value any) *Field {
	f.decl.meta[key] = value
	return f
}

// Name returns the field's name in the schema.
func (f *Field) Name() string { return f.decl.name }

// FieldInfo is a read-only view of a declared field, for plugins.
type FieldInfo struct{ decl *fieldDecl }

// Name returns the field's name in the schema.
func (i FieldInfo) Name() string { return i.decl.name }

// GoResult returns the Go type the field's resolver returns.
func (i FieldInfo) GoResult() reflect.Type { return i.decl.goResult }

// Meta returns plugin data attached to the field.
func (i FieldInfo) Meta(key string) (any, bool) {
	v, ok := i.decl.meta[key]
	return v, ok
}

// buildField constructs the runtime field for a declaration.
func (b *Builder) buildField(parent *typeDecl, decl *fieldDecl) (*graphql.Field, error) {
	at := fmt.Sprintf("%s.%s", parent.name, decl.name)

	// A plugin may hand over a finished field rather than a Go resolver, which
	// is how relay contributes node(id:) for a type the application never names.
	if decl.built != nil {
		for _, p := range b.plugins {
			if wrap, ok := p.(FieldPlugin); ok {
				if err := wrap.Field(b, parent.name, FieldInfo{decl: decl}, decl.built); err != nil {
					return nil, fmt.Errorf("plugin %s: %s: %w", p.PluginName(), at, err)
				}
			}
		}
		return decl.built, nil
	}

	fieldType, err := b.graphQLType(decl.goResult, at)
	if err != nil {
		return nil, err
	}
	if decl.nonNull != nil {
		if *decl.nonNull {
			if _, already := fieldType.(*graphql.NonNull); !already {
				fieldType = &graphql.NonNull{Type: fieldType}
			}
		} else {
			fieldType = unwrapNonNull(fieldType)
		}
	}

	field := &graphql.Field{
		Type:              fieldType,
		Description:       decl.description,
		DeprecationReason: decl.deprecated,
		Resolve:           decl.resolve,
		ParseArguments:    noArguments,
		Expensive:         decl.expensive,
	}

	if decl.goArgs != nil {
		args, parse, descriptions, err := b.buildArguments(decl.goArgs, at)
		if err != nil {
			return nil, err
		}
		field.Args = args
		field.ParseArguments = parse
		field.ArgDescriptions = descriptions
	}

	for _, p := range b.plugins {
		if wrap, ok := p.(FieldPlugin); ok {
			if err := wrap.Field(b, parent.name, FieldInfo{decl: decl}, field); err != nil {
				return nil, fmt.Errorf("plugin %s: %s: %w", p.PluginName(), at, err)
			}
		}
	}

	return field, nil
}
