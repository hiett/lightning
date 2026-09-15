// Package lightning builds GraphQL schemas from Go types.
//
// The API is built around one conviction: the Go type already says what the
// GraphQL type is, so the programmer should not have to say it again. A
// resolver returning []*Task has declared a list of nullable Task; a struct
// field tagged with a description has documented itself. Everything else
// follows from those two.
//
//	type Task struct {
//	    lightning.Meta `graphql:"Task" description:"A unit of work."`
//
//	    Key   string `graphql:"-"`
//	    Title string `description:"What needs doing."`
//	    Done  bool   `description:"Whether it has been done."`
//	}
//
//	b := lightning.New()
//	task := lightning.Object[Task](b)
//	task.Field("owner", store.Owner).Describe("Whoever the task belongs to.")
//
//	b.Query().Field("tasks", store.Tasks)
//
//	schema := b.MustBuild()
//
// Mistakes are compile errors wherever the compiler can see them — a resolver
// whose parent or result type is wrong will not build — and named build errors
// everywhere else.
package lightning

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/hiett/lightning/graphql"
)

// ctxAlias exists so gotype.go can name context.Context without importing it
// under a different name.
type ctxAlias = context.Context

// Builder accumulates type declarations and turns them into a schema.
//
// Declaration and construction are separate passes, which is what lets two
// types refer to each other: the declaration of Task may mention User before
// User has been declared, because nothing is resolved until Build.
type Builder struct {
	// decls holds every declared type, keyed by the Go type it is about.
	decls map[reflect.Type]*typeDecl
	// order preserves declaration order so that builds are deterministic and
	// error messages come out in the order the author wrote them.
	order []*typeDecl

	// built caches constructed runtime types, so a type referenced twice is one
	// type, and a cycle terminates.
	built map[*typeDecl]graphql.Type

	roots map[operation]*typeDecl

	plugins []Plugin

	// errs collects declaration-time problems. They are reported together by
	// Build rather than one per run, so a schema with three mistakes takes one
	// round trip to fix rather than three.
	errs []error
}

type operation int

const (
	opQuery operation = iota
	opMutation
	opSubscription
)

func (o operation) String() string {
	switch o {
	case opQuery:
		return "Query"
	case opMutation:
		return "Mutation"
	default:
		return "Subscription"
	}
}

// Root is the parent type of a root operation field. A resolver on Query,
// Mutation or Subscription receives one, and has nothing to read from it.
type Root struct{}

// New returns a Builder with the given plugins installed.
func New(plugins ...Plugin) *Builder {
	b := &Builder{
		decls:   map[reflect.Type]*typeDecl{},
		built:   map[*typeDecl]graphql.Type{},
		roots:   map[operation]*typeDecl{},
		plugins: plugins,
	}
	for _, p := range plugins {
		if p == nil {
			b.errorf("a nil plugin was installed")
			continue
		}
		if install, ok := p.(InstallPlugin); ok {
			if err := install.Install(b); err != nil {
				b.errorf("installing plugin %s: %s", p.PluginName(), err)
			}
		}
	}
	return b
}

// errorf records a declaration-time problem.
func (b *Builder) errorf(format string, args ...any) {
	b.errs = append(b.errs, fmt.Errorf(format, args...))
}

// declFor returns the declaration for a Go type, if it has one.
func (b *Builder) declFor(goType reflect.Type) *typeDecl {
	return b.decls[goType]
}

// declKind distinguishes the sorts of type a declaration can produce.
type declKind int

const (
	kindObject declKind = iota
	kindInterface
	kindUnion
	kindEnum
	kindInput
)

func (k declKind) String() string {
	switch k {
	case kindObject:
		return "object"
	case kindInterface:
		return "interface"
	case kindUnion:
		return "union"
	case kindEnum:
		return "enum"
	default:
		return "input"
	}
}

// typeDecl is a declared type, before it becomes a runtime type.
type typeDecl struct {
	name        string
	description string
	kind        declKind

	// goType is the struct or interface the declaration is about. It is the
	// identity of the declaration: a Go type maps to exactly one GraphQL type.
	goType reflect.Type

	fields []*fieldDecl

	// exposeAll reports whether the struct's own exported fields become GraphQL
	// fields. True for objects and inputs, which is what makes a plain data
	// type need no field declarations at all.
	exposeAll bool
	// hidden names struct fields to leave out, beyond those tagged `graphql:"-"`.
	hidden map[string]bool

	// implements names the interfaces this object joins. Membership is
	// explicit: satisfying a Go interface by accident is normal, and joining a
	// GraphQL interface by accident is not.
	implements []*typeDecl
	// members names the objects belonging to this interface or union, in
	// declaration order.
	members []*typeDecl

	// enumValues maps a GraphQL enum value name to the Go value behind it.
	enumValues   []enumValue
	enumGoValues map[string]reflect.Value

	// building guards against a declaration cycle that cannot terminate, as
	// opposed to the ordinary cycles that the built cache handles.
	building bool

	// meta carries per-type plugin data.
	meta map[string]any
}

type enumValue struct {
	name        string
	description string
	deprecated  string
	value       reflect.Value
}

// fieldDecl is a declared field, before it becomes a runtime field.
type fieldDecl struct {
	name        string
	description string
	deprecated  string

	// goResult is the Go type the resolver returns, from which the field's
	// GraphQL type and its nullability are derived.
	goResult reflect.Type
	// goArgs is the Go struct the resolver takes as arguments, or nil.
	goArgs reflect.Type

	resolve graphql.Resolver

	// nonNull overrides the nullability derived from goResult. Rarely needed:
	// the Go type is usually right, and when it is not, the honest fix is
	// usually to change the Go type.
	nonNull *bool

	expensive bool

	// source names where the field came from, for error messages: a struct
	// field, or the call site of a Field declaration.
	source string

	// meta carries per-field plugin data.
	meta map[string]any

	// built is a ready-made runtime field, for a plugin that assembles one
	// rather than declaring it from a Go resolver.
	built *graphql.Field
	// builtBy names what supplied a pre-built field, for error messages.
	builtBy string
}

// Build turns the declarations into a schema.
//
// Every problem found is reported together, so that a schema with several
// mistakes takes one round trip to fix rather than one per mistake.
func (b *Builder) Build() (*graphql.Schema, error) {
	if len(b.errs) > 0 {
		return nil, b.combinedError()
	}

	for _, p := range b.plugins {
		if before, ok := p.(BeforeBuildPlugin); ok {
			if err := before.BeforeBuild(b); err != nil {
				b.errorf("plugin %s: %s", p.PluginName(), err)
			}
		}
	}
	if len(b.errs) > 0 {
		return nil, b.combinedError()
	}

	if err := b.checkNames(); err != nil {
		return nil, err
	}

	// Build every declared type, not only those reachable from a root: a type
	// declared and then never referenced is a mistake worth reporting, and a
	// plugin may reach a type the roots do not.
	for _, decl := range b.order {
		if _, err := b.buildDecl(decl); err != nil {
			b.errs = append(b.errs, err)
		}
	}
	if len(b.errs) > 0 {
		return nil, b.combinedError()
	}

	if err := b.wireAbstractTypes(); err != nil {
		return nil, err
	}

	schema := &graphql.Schema{}
	for op, decl := range map[operation]*typeDecl{
		opQuery:        b.roots[opQuery],
		opMutation:     b.roots[opMutation],
		opSubscription: b.roots[opSubscription],
	} {
		if decl == nil {
			continue
		}
		built, err := b.buildDecl(decl)
		if err != nil {
			return nil, err
		}
		object, ok := unwrapNonNull(built).(*graphql.Object)
		if !ok {
			return nil, fmt.Errorf("the %s root must be an object", op)
		}
		switch op {
		case opQuery:
			schema.Query = object
		case opMutation:
			schema.Mutation = object
		case opSubscription:
			schema.Subscription = object
		}
	}

	if schema.Query == nil {
		return nil, errors.New("a schema needs a Query root; declare a field with b.Query()")
	}
	// The runtime expects a Mutation object to exist even when empty, as the
	// SDL printer leaves a fieldless root out of the schema block.
	if schema.Mutation == nil {
		schema.Mutation = &graphql.Object{Name: "Mutation", Fields: map[string]*graphql.Field{}}
	}

	for _, p := range b.plugins {
		if after, ok := p.(AfterBuildPlugin); ok {
			if err := after.AfterBuild(b, schema); err != nil {
				return nil, fmt.Errorf("plugin %s: %w", p.PluginName(), err)
			}
		}
	}
	if len(b.errs) > 0 {
		return nil, b.combinedError()
	}

	return schema, nil
}

// MustBuild is Build, panicking on failure. It suits a schema built at start-up
// by a server that cannot run without one.
func (b *Builder) MustBuild() *graphql.Schema {
	schema, err := b.Build()
	if err != nil {
		panic(err)
	}
	return schema
}

// combinedError joins every recorded problem into one error, in declaration
// order, one per line.
func (b *Builder) combinedError() error {
	if len(b.errs) == 1 {
		return b.errs[0]
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%d problems with this schema:", len(b.errs))
	for _, err := range b.errs {
		out.WriteString("\n  - ")
		out.WriteString(err.Error())
	}
	return errors.New(out.String())
}

// checkNames reports two declarations that claim the same GraphQL name.
func (b *Builder) checkNames() error {
	seen := map[string]*typeDecl{}
	for _, decl := range b.order {
		if other, ok := seen[decl.name]; ok {
			b.errorf("two types are both named %s: %s and %s", decl.name, typeName(other.goType), typeName(decl.goType))
			continue
		}
		seen[decl.name] = decl
	}
	if len(b.errs) > 0 {
		return b.combinedError()
	}
	return nil
}

// unwrapNonNull removes a NonNull wrapper, if there is one.
func unwrapNonNull(t graphql.Type) graphql.Type {
	if nn, ok := t.(*graphql.NonNull); ok {
		return nn.Type
	}
	return t
}

// sortedFieldNames returns a map's keys in a stable order.
func sortedFieldNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
