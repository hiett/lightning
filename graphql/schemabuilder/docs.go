package schemabuilder

import (
	"reflect"

	"github.com/hiett/lightning/graphql"
)

// This file carries documentation — descriptions and deprecations — from Go
// into the schema, where introspection and exported SDL can report it.
//
// There are two ways to write a field, so there are two ways to document one:
//
//   - a field derived from a Go struct field is documented with struct tags:
//
//     type User struct {
//         Name  string `description:"The user's display name."`
//         Email string `description:"Their primary address." deprecated:"Use emails instead."`
//     }
//
//   - a field registered with FieldFunc is documented with options:
//
//     user.FieldFunc("friends", resolve,
//         schemabuilder.Description("Everyone this user follows."),
//         schemabuilder.ArgDescription("limit", "How many to return."),
//         schemabuilder.Deprecated("Use following instead."))
//
// Struct tags are used for struct fields rather than a registration call
// because the documentation then sits next to the thing it documents; a
// FieldFunc has no such place, so it takes options.

// DefaultDeprecationReason is reported when a field or enum value is marked
// deprecated without saying why. It matches the specification's default for the
// @deprecated directive.
const DefaultDeprecationReason = "No longer supported"

// Description documents a field registered with FieldFunc. The text is
// reported by introspection and printed into exported SDL.
func Description(description string) FieldFuncOption {
	return fieldFuncOptionFunc(func(m *method) {
		m.Description = description
	})
}

// ArgDescription documents one argument of a field registered with FieldFunc.
func ArgDescription(name, description string) FieldFuncOption {
	return fieldFuncOptionFunc(func(m *method) {
		if m.ArgDescriptions == nil {
			m.ArgDescriptions = map[string]string{}
		}
		m.ArgDescriptions[name] = description
	})
}

// Deprecated marks a field registered with FieldFunc as deprecated. An empty
// reason becomes DefaultDeprecationReason.
//
// A deprecated field still resolves; it is hidden from introspection unless the
// client asks for deprecated fields, and carries @deprecated in exported SDL.
func Deprecated(reason string) FieldFuncOption {
	if reason == "" {
		reason = DefaultDeprecationReason
	}
	return fieldFuncOptionFunc(func(m *method) {
		m.DeprecationReason = reason
	})
}

// Describe sets an object type's description.
func (o *Object) Describe(description string) *Object {
	o.Description = description
	return o
}

// fieldDocs is the documentation read off a Go struct field's tags.
type fieldDocs struct {
	description       string
	deprecationReason string
}

// parseFieldDocs reads the `description` and `deprecated` tags off a struct
// field.
//
// They are separate tags rather than options inside the existing comma
// separated `graphql` tag because prose contains commas.
func parseFieldDocs(field reflect.StructField) fieldDocs {
	docs := fieldDocs{description: field.Tag.Get("description")}

	if reason, ok := field.Tag.Lookup("deprecated"); ok {
		if reason == "" {
			reason = DefaultDeprecationReason
		}
		docs.deprecationReason = reason
	}

	return docs
}

// EnumOption configures an enum at registration time.
type EnumOption func(*EnumMapping)

// EnumDescription documents the enum type itself.
func EnumDescription(description string) EnumOption {
	return func(m *EnumMapping) { m.Description = description }
}

// EnumValueDescriptions documents individual enum values, keyed by the name the
// value has in the schema.
func EnumValueDescriptions(descriptions map[string]string) EnumOption {
	return func(m *EnumMapping) { m.Descriptions = descriptions }
}

// EnumValueDeprecations marks individual enum values deprecated, keyed by the
// name the value has in the schema. An empty reason becomes
// DefaultDeprecationReason.
func EnumValueDeprecations(reasons map[string]string) EnumOption {
	return func(m *EnumMapping) {
		filled := make(map[string]string, len(reasons))
		for name, reason := range reasons {
			if reason == "" {
				reason = DefaultDeprecationReason
			}
			filled[name] = reason
		}
		m.DeprecationReasons = filled
	}
}

// applyDocs copies documentation from an enum registration onto the built type.
func (m *EnumMapping) applyDocs(enum *graphql.Enum) {
	if m == nil {
		return
	}
	enum.Description = m.Description
	enum.Descriptions = m.Descriptions
	enum.DeprecationReasons = m.DeprecationReasons
}
