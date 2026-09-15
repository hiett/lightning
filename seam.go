package lightning

import (
	"fmt"
	"reflect"

	"github.com/hiett/lightning/graphql"
)

// This file is the part of the plugin seam a plugin needs in order to declare
// a field whose GraphQL type it computes itself.
//
// The ordinary path derives a field's type from its resolver's Go return type,
// which is right for anything an application writes. A plugin like relay
// generates types — TaskConnection, TaskEdge — that have no Go counterpart, so
// it needs to say what the type is rather than have it read off a signature.

// Builder returns the builder a type handle belongs to, so a plugin given a
// handle can reach the rest of the schema.
func (t *Type[T]) Builder() *Builder { return t.b }

// Builder returns the builder an abstract type handle belongs to.
func (a *AbstractType[I]) Builder() *Builder { return a.b }

// SourceAs converts an executor-supplied source value to a typed parent, for a
// plugin writing a resolver by hand.
func SourceAs[T any](source any) (*T, bool) { return sourceAs[T](source) }

// RawFieldArgs declares a field whose GraphQL type the caller computes, with
// arguments read from a Go struct type given at run time.
//
// It is the escape hatch plugins use and applications should not need: a field
// declared this way loses the compile-time check on its resolver, because
// neither its result type nor its parent type is known to the compiler.
//
// typeOf is called during Build, once the rest of the schema exists, so the
// type it returns may depend on what the application declared.
func (t *Type[T]) RawFieldArgs(
	name string,
	argsType reflect.Type,
	resolve graphql.Resolver,
	typeOf func(*Builder) (graphql.Type, error),
) *Field {
	decl := &fieldDecl{
		name:     name,
		goArgs:   argsType,
		resolve:  resolve,
		typeOf:   typeOf,
		source:   "plugin",
		builtBy:  "plugin",
		meta:     map[string]any{},
		goResult: nil,
	}
	t.decl.fields = append(t.decl.fields, decl)
	return &Field{b: t.b, parent: t.decl, decl: decl}
}

// RawFieldArgsOf is RawFieldArgs for a field whose argument struct is itself
// computed during Build.
//
// relay needs it: which arguments a connection takes depends on what the node
// type declared sortable and filterable, and that is not known until the node
// type has been built.
func (t *Type[T]) RawFieldArgsOf(
	name string,
	argsOf func(*Builder) (reflect.Type, error),
	resolve graphql.Resolver,
	typeOf func(*Builder) (graphql.Type, error),
) *Field {
	field := t.RawFieldArgs(name, nil, resolve, typeOf)
	field.decl.goArgsOf = argsOf
	return field
}

// Placeholder returns a Field that records nothing, for a plugin that has
// already reported an error and needs something to return.
func (t *Type[T]) Placeholder(name string) *Field {
	return &Field{b: t.b, parent: t.decl, decl: &fieldDecl{name: name, meta: map[string]any{}}}
}

// BuiltType returns the runtime type for a declared Go type, building it if it
// has not been built yet.
//
// A plugin assembling a type around an application's type — a connection over
// Task, say — needs the built Task to put inside it.
func (b *Builder) BuiltType(goType reflect.Type) (graphql.Type, error) {
	decl := b.declFor(goType)
	if decl == nil {
		return nil, fmt.Errorf("%s is not declared; declare it with lightning.Object", typeName(goType))
	}
	return b.buildDecl(decl)
}
