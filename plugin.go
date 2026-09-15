package lightning

import (
	"reflect"
	"sort"

	"github.com/hiett/lightning/graphql"
)

// This file is the plugin seam.
//
// A plugin is a value installed on a Builder that extends what a schema can
// express. Relay — the Node interface, global identifiers and connections —
// is one, and lives in its own package with no privileged access: everything it
// does, anything else can do.
//
// The seam is deliberately a set of small optional interfaces rather than one
// large one. A plugin implements only what it needs, and a capability added
// later does not break the plugins that came before it.

// Plugin is the minimum a plugin must be: something with a name, for error
// messages that say which plugin objected.
type Plugin interface {
	PluginName() string
}

// InstallPlugin is implemented by a plugin that contributes types or root
// fields. Install runs when the plugin is passed to New, before any of the
// application's own declarations.
type InstallPlugin interface {
	Plugin
	Install(b *Builder) error
}

// FieldPlugin is implemented by a plugin that inspects or wraps every field as
// it is built. It is how a plugin adds authorisation, tracing or batching
// without the field's author naming it.
//
// The field is the runtime field, so a plugin may replace its resolver. Doing
// so should preserve the original: wrapping is composition, and several
// plugins may wrap the same field.
//
// A batch field has two resolvers — Resolve for one parent and BatchResolver
// for many — and which one runs is decided per request. A plugin that wraps one
// must wrap the other, or its behaviour will come and go with the batching.
type FieldPlugin interface {
	Plugin
	Field(b *Builder, typeName string, info FieldInfo, field *graphql.Field) error
}

// BeforeBuildPlugin is implemented by a plugin that contributes declarations
// derived from what the application declared — the Node interface, say, which
// cannot be assembled until every node type is known.
type BeforeBuildPlugin interface {
	Plugin
	BeforeBuild(b *Builder) error
}

// AfterBuildPlugin is implemented by a plugin that inspects or adjusts the
// finished schema.
type AfterBuildPlugin interface {
	Plugin
	AfterBuild(b *Builder, schema *graphql.Schema) error
}

// Errorf records a build problem from a plugin. Problems are reported together
// when the schema is built.
func (b *Builder) Errorf(format string, args ...any) {
	b.errorf(format, args...)
}

// Plugins returns the installed plugins, so one plugin can find another.
func (b *Builder) Plugins() []Plugin { return b.plugins }

// RootField adds a field to a root operation type from a plugin.
//
// The resolver is given in its runtime form, because a plugin's root fields are
// usually assembled rather than written: relay's node(id:) field resolves to a
// type the application never names.
func (b *Builder) RootField(op RootOperation, name string, field *graphql.Field) {
	decl := b.root(operation(op)).decl
	decl.fields = append(decl.fields, &fieldDecl{
		name:    name,
		built:   field,
		source:  "plugin",
		meta:    map[string]any{},
		builtBy: "plugin",
	})
}

// RootOperation names a root operation type.
type RootOperation int

const (
	// QueryRoot is the query root type.
	QueryRoot RootOperation = RootOperation(opQuery)
	// MutationRoot is the mutation root type.
	MutationRoot RootOperation = RootOperation(opMutation)
	// SubscriptionRoot is the subscription root type.
	SubscriptionRoot RootOperation = RootOperation(opSubscription)
)

// DeclaredTypes returns every declared type, in declaration order, so a plugin
// can find the types it cares about.
func (b *Builder) DeclaredTypes() []DeclaredType {
	out := make([]DeclaredType, 0, len(b.order))
	for _, decl := range b.order {
		out = append(out, DeclaredType{b: b, decl: decl})
	}
	return out
}

// DeclaredType is a read-only view of a declared type, for plugins.
type DeclaredType struct {
	b    *Builder
	decl *typeDecl
}

// Name returns the type's name in the schema.
func (d DeclaredType) Name() string { return d.decl.name }

// GoType returns the Go type behind the declaration.
func (d DeclaredType) GoType() reflect.Type { return d.decl.goType }

// IsObject reports whether the declaration is an object type.
func (d DeclaredType) IsObject() bool { return d.decl.kind == kindObject }

// IsInterface reports whether the declaration is an interface type.
func (d DeclaredType) IsInterface() bool { return d.decl.kind == kindInterface }

// Members returns the types belonging to an interface or union.
func (d DeclaredType) Members() []DeclaredType {
	out := make([]DeclaredType, 0, len(d.decl.members))
	for _, member := range d.decl.members {
		out = append(out, DeclaredType{b: d.b, decl: member})
	}
	return out
}

// HasField reports whether the type will have a field of this name, whether it
// was declared or comes from a struct field.
func (d DeclaredType) HasField(name string) bool {
	return d.b.fieldNames(d.decl)[name]
}

// FieldNames returns every field name the type will have, in schema order.
func (d DeclaredType) FieldNames() []string {
	names := d.b.fieldNames(d.decl)
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// SortableFields names the fields a paginated list over this type may be
// ordered by, in schema order. It is complete only once the type is built.
func (d DeclaredType) SortableFields() []string { return d.decl.sortable }

// FilterableFields names the fields whose text a paginated list over this type
// may be searched by. It is complete only once the type is built.
func (d DeclaredType) FilterableFields() []string { return d.decl.filterable }

// Meta returns plugin data attached to the type.
func (d DeclaredType) Meta(key string) (any, bool) {
	v, ok := d.decl.meta[key]
	return v, ok
}

// SetMeta attaches plugin data to the type. The key should be namespaced by
// the plugin that owns it.
func (d DeclaredType) SetMeta(key string, value any) {
	if d.decl.meta == nil {
		d.decl.meta = map[string]any{}
	}
	d.decl.meta[key] = value
}

// SetKeyField names the field whose value identifies one value of this type.
//
// The live-query diff uses it to line up the elements of a list between one
// push and the next: with a key, moving an item is a reorder, and without one
// it is a delete and an insert of everything after it. The key is not part of
// the schema — it travels as __key alongside the payload.
func (d DeclaredType) SetKeyField(field *graphql.Field) {
	d.decl.keyField = field
}

// AddField adds a field to a declared type from a plugin, in its runtime form.
func (d DeclaredType) AddField(name string, field *graphql.Field) {
	d.decl.fields = append(d.decl.fields, &fieldDecl{
		name:    name,
		built:   field,
		source:  "plugin",
		meta:    map[string]any{},
		builtBy: "plugin",
	})
}
