package graphql

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// This file renders a runtime *Schema as GraphQL SDL, and loads that SDL into a
// gqlparser *ast.Schema.
//
// The SDL text is the artifact relay-compiler and other schema-driven tooling
// consume; the *ast.Schema is what query validation runs against. Both come
// from the same printer so the two can never disagree.

// builtinScalars are provided by gqlparser's prelude and must not be
// re-declared in printed SDL.
var builtinScalars = map[string]bool{
	"String":  true,
	"Int":     true,
	"Float":   true,
	"Boolean": true,
	"ID":      true,
}

// isReservedName reports whether a schema member is part of the introspection
// system, which is built in rather than printed.
func isReservedName(name string) bool {
	return strings.HasPrefix(name, "__")
}

// PrintSchema renders a schema as GraphQL SDL.
//
// Output is deterministic: types, fields, arguments and enum values are each
// emitted in sorted order, so the result can be committed to a repository and
// diffed.
func PrintSchema(schema *Schema) (string, error) {
	p := &sdlPrinter{types: map[string]Type{}}

	if err := p.collect(schema.Query); err != nil {
		return "", err
	}
	if err := p.collect(schema.Mutation); err != nil {
		return "", err
	}
	if err := p.collect(schema.Subscription); err != nil {
		return "", err
	}

	return p.print(schema)
}

// ASTSchema renders a schema as SDL and loads it with gqlparser, producing the
// schema representation the query validator needs.
//
// Loading is itself a check on the schema: a runtime schema that cannot produce
// a legal SDL document is reported here rather than failing lazily during
// execution.
func ASTSchema(schema *Schema) (*ast.Schema, error) {
	sdl, err := PrintSchema(schema)
	if err != nil {
		return nil, err
	}

	astSchema, err := gqlparser.LoadSchema(&ast.Source{Name: "schema.graphql", Input: sdl})
	if err != nil {
		return nil, fmt.Errorf("loading printed schema: %w", err)
	}
	return astSchema, nil
}

type sdlPrinter struct {
	// types holds every named type reachable from the schema roots, keyed by
	// its GraphQL name.
	types map[string]Type
	// anonymous counts unnamed input objects, so each can be given a stable
	// synthetic name.
	anonymous int
}

// typeName returns the GraphQL name of a named type, assigning one to an
// anonymous input object if necessary.
func (p *sdlPrinter) typeName(typ Type) (string, error) {
	switch typ := typ.(type) {
	case *Scalar:
		return typ.Type, nil
	case *Enum:
		return typ.Type, nil
	case *Object:
		return typ.Name, nil
	case *Union:
		return typ.Name, nil
	case *Interface:
		return typ.Name, nil
	case *InputObject:
		if typ.Name == "" {
			p.anonymous++
			typ.Name = fmt.Sprintf("AnonymousInput%d", p.anonymous)
		}
		return typ.Name, nil
	default:
		return "", fmt.Errorf("type %T has no name", typ)
	}
}

// collect walks the schema recording every named type it can reach.
func (p *sdlPrinter) collect(typ Type) error {
	switch typ := typ.(type) {
	case nil:
		return nil

	case *List:
		return p.collect(typ.Type)

	case *NonNull:
		return p.collect(typ.Type)

	case *Scalar, *Enum:
		name, err := p.typeName(typ)
		if err != nil {
			return err
		}
		p.types[name] = typ
		return nil

	case *Object:
		if isReservedName(typ.Name) {
			return nil
		}
		if _, seen := p.types[typ.Name]; seen {
			return nil
		}
		p.types[typ.Name] = typ

		for name, field := range typ.Fields {
			if isReservedName(name) {
				continue
			}
			if err := p.collect(field.Type); err != nil {
				return err
			}
			for _, arg := range field.Args {
				if err := p.collect(arg); err != nil {
					return err
				}
			}
		}
		for _, iface := range typ.Interfaces {
			if err := p.collect(iface); err != nil {
				return err
			}
		}
		return nil

	case *Interface:
		if _, seen := p.types[typ.Name]; seen {
			return nil
		}
		p.types[typ.Name] = typ

		for name, field := range typ.Fields {
			if isReservedName(name) {
				continue
			}
			if err := p.collect(field.Type); err != nil {
				return err
			}
			for _, arg := range field.Args {
				if err := p.collect(arg); err != nil {
					return err
				}
			}
		}
		for _, possible := range typ.PossibleTypes {
			if err := p.collect(possible); err != nil {
				return err
			}
		}
		return nil

	case *Union:
		if _, seen := p.types[typ.Name]; seen {
			return nil
		}
		p.types[typ.Name] = typ
		for _, member := range typ.Types {
			if err := p.collect(member); err != nil {
				return err
			}
		}
		return nil

	case *InputObject:
		name, err := p.typeName(typ)
		if err != nil {
			return err
		}
		if _, seen := p.types[name]; seen {
			return nil
		}
		p.types[name] = typ
		for _, field := range typ.InputFields {
			if err := p.collect(field); err != nil {
				return err
			}
		}
		return nil

	default:
		return fmt.Errorf("cannot print unknown type %T", typ)
	}
}

// ref renders a type reference, for example "[Foo!]!".
func (p *sdlPrinter) ref(typ Type) (string, error) {
	switch typ := typ.(type) {
	case *NonNull:
		inner, err := p.ref(typ.Type)
		if err != nil {
			return "", err
		}
		return inner + "!", nil
	case *List:
		inner, err := p.ref(typ.Type)
		if err != nil {
			return "", err
		}
		return "[" + inner + "]", nil
	default:
		return p.typeName(typ)
	}
}

func (p *sdlPrinter) print(schema *Schema) (string, error) {
	var b strings.Builder

	rootNames, err := p.printRoots(&b, schema)
	if err != nil {
		return "", err
	}

	names := make([]string, 0, len(p.types))
	for name := range p.types {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		// A root type that carries no fields is not printed; printRoots has
		// already left it out of the schema block.
		if rootNames[name] && !p.hasFields(p.types[name]) {
			continue
		}
		if err := p.printType(&b, name, p.types[name]); err != nil {
			return "", err
		}
	}

	return b.String(), nil
}

// printRoots writes the schema block and reports which type names are roots.
func (p *sdlPrinter) printRoots(b *strings.Builder, schema *Schema) (map[string]bool, error) {
	roots := map[string]bool{}

	var lines []string
	for _, root := range []struct {
		operation string
		typ       Type
	}{
		{"query", schema.Query},
		{"mutation", schema.Mutation},
		{"subscription", schema.Subscription},
	} {
		if root.typ == nil {
			continue
		}
		name, err := p.typeName(root.typ)
		if err != nil {
			return nil, err
		}
		roots[name] = true

		// A root operation type with no fields is not a legal GraphQL object,
		// and an operation the server cannot serve should not be advertised.
		// schemabuilder always creates a Mutation object, even when nothing was
		// registered on it.
		if !p.hasFields(root.typ) {
			continue
		}
		lines = append(lines, fmt.Sprintf("  %s: %s", root.operation, name))
	}

	if len(lines) == 0 {
		return nil, fmt.Errorf("schema has no root operation type with any fields")
	}

	b.WriteString("schema {\n")
	for _, line := range lines {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("}\n\n")

	return roots, nil
}

// hasFields reports whether an object type has at least one printable field.
func (p *sdlPrinter) hasFields(typ Type) bool {
	object, ok := typ.(*Object)
	if !ok {
		return true
	}
	for name := range object.Fields {
		if !isReservedName(name) {
			return true
		}
	}
	return false
}

func (p *sdlPrinter) printType(b *strings.Builder, name string, typ Type) error {
	switch typ := typ.(type) {
	case *Scalar:
		if builtinScalars[name] {
			return nil
		}
		writeDescription(b, "", scalarDescription(typ))
		fmt.Fprintf(b, "scalar %s\n\n", name)
		return nil

	case *Enum:
		writeDescription(b, "", typ.Description)
		fmt.Fprintf(b, "enum %s {\n", name)
		values := append([]string(nil), typ.Values...)
		sort.Strings(values)
		for _, value := range values {
			if !isValidEnumValue(value) {
				return fmt.Errorf("enum %s has value %q, which is not a legal GraphQL enum value", name, value)
			}
			writeDescription(b, "  ", typ.Descriptions[value])
			fmt.Fprintf(b, "  %s%s\n", value, deprecationSuffix(typ.DeprecationReasons[value]))
		}
		b.WriteString("}\n\n")
		return nil

	case *Union:
		writeDescription(b, "", typ.Description)
		members := make([]string, 0, len(typ.Types))
		for member := range typ.Types {
			members = append(members, member)
		}
		sort.Strings(members)
		if len(members) == 0 {
			return fmt.Errorf("union %s has no member types", name)
		}
		fmt.Fprintf(b, "union %s = %s\n\n", name, strings.Join(members, " | "))
		return nil

	case *Interface:
		writeDescription(b, "", typ.Description)
		fmt.Fprintf(b, "interface %s {\n", name)
		if err := p.printFields(b, typ.Fields); err != nil {
			return fmt.Errorf("interface %s: %w", name, err)
		}
		b.WriteString("}\n\n")
		return nil

	case *Object:
		writeDescription(b, "", typ.Description)
		fmt.Fprintf(b, "type %s", name)
		if len(typ.Interfaces) > 0 {
			ifaceNames := make([]string, 0, len(typ.Interfaces))
			for _, iface := range typ.Interfaces {
				ifaceNames = append(ifaceNames, iface.Name)
			}
			sort.Strings(ifaceNames)
			fmt.Fprintf(b, " implements %s", strings.Join(ifaceNames, " & "))
		}
		b.WriteString(" {\n")
		if err := p.printFields(b, typ.Fields); err != nil {
			return fmt.Errorf("type %s: %w", name, err)
		}
		b.WriteString("}\n\n")
		return nil

	case *InputObject:
		writeDescription(b, "", typ.Description)
		fmt.Fprintf(b, "input %s {\n", name)

		fieldNames := make([]string, 0, len(typ.InputFields))
		for fieldName := range typ.InputFields {
			fieldNames = append(fieldNames, fieldName)
		}
		sort.Strings(fieldNames)
		if len(fieldNames) == 0 {
			return fmt.Errorf("input %s has no fields", name)
		}
		for _, fieldName := range fieldNames {
			ref, err := p.ref(typ.InputFields[fieldName])
			if err != nil {
				return fmt.Errorf("input %s field %s: %w", name, fieldName, err)
			}
			writeDescription(b, "  ", typ.FieldDescriptions[fieldName])
			fmt.Fprintf(b, "  %s: %s\n", fieldName, ref)
		}
		b.WriteString("}\n\n")
		return nil

	default:
		return fmt.Errorf("cannot print type %T", typ)
	}
}

func (p *sdlPrinter) printFields(b *strings.Builder, fields map[string]*Field) error {
	names := make([]string, 0, len(fields))
	for name := range fields {
		if isReservedName(name) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	if len(names) == 0 {
		return fmt.Errorf("has no fields")
	}

	for _, name := range names {
		field := fields[name]

		ref, err := p.ref(field.Type)
		if err != nil {
			return fmt.Errorf("field %s: %w", name, err)
		}

		args, err := p.printArgs(field)
		if err != nil {
			return fmt.Errorf("field %s: %w", name, err)
		}

		writeDescription(b, "  ", field.Description)
		fmt.Fprintf(b, "  %s%s: %s%s\n", name, args, ref, deprecationSuffix(field.DeprecationReason))
	}
	return nil
}

// printArgs renders a field's argument list. Documented arguments force the
// multi-line form, because a description cannot sit inside a one-line list.
func (p *sdlPrinter) printArgs(field *Field) (string, error) {
	args := field.Args
	if len(args) == 0 {
		return "", nil
	}

	names := make([]string, 0, len(args))
	for name := range args {
		names = append(names, name)
	}
	sort.Strings(names)

	documented := false
	for _, name := range names {
		if field.ArgDescriptions[name] != "" {
			documented = true
			break
		}
	}

	if !documented {
		parts := make([]string, 0, len(names))
		for _, name := range names {
			ref, err := p.ref(args[name])
			if err != nil {
				return "", fmt.Errorf("arg %s: %w", name, err)
			}
			parts = append(parts, fmt.Sprintf("%s: %s", name, ref))
		}
		return "(" + strings.Join(parts, ", ") + ")", nil
	}

	var b strings.Builder
	b.WriteString("(\n")
	for _, name := range names {
		ref, err := p.ref(args[name])
		if err != nil {
			return "", fmt.Errorf("arg %s: %w", name, err)
		}
		writeDescription(&b, "    ", field.ArgDescriptions[name])
		fmt.Fprintf(&b, "    %s: %s\n", name, ref)
	}
	b.WriteString("  )")
	return b.String(), nil
}

// deprecationSuffix renders the @deprecated directive for a non-empty reason.
func deprecationSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return fmt.Sprintf(" @deprecated(reason: %s)", quoteGraphQLString(reason))
}

func scalarDescription(s *Scalar) string {
	return s.Description
}

// writeDescription writes a description as a block string, if there is one.
func writeDescription(b *strings.Builder, indent, description string) {
	if description == "" {
		return
	}

	// A description holding a block-string terminator would close the string
	// early and leave the rest of the text to be lexed as schema syntax. The
	// specification's escape for it inside a block string is \""".
	description = strings.ReplaceAll(description, `"""`, `\"""`)

	b.WriteString(indent)
	b.WriteString(`"""`)
	b.WriteString("\n")
	for _, line := range strings.Split(description, "\n") {
		b.WriteString(indent)
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString(indent)
	b.WriteString(`"""`)
	b.WriteString("\n")
}

// quoteGraphQLString renders a Go string as a GraphQL string literal.
func quoteGraphQLString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// isValidEnumValue reports whether name is legal as an enum value: a Name that
// is not one of the three literals the specification excludes.
func isValidEnumValue(name string) bool {
	switch name {
	case "true", "false", "null":
		return false
	}
	return isValidGraphQLName(name)
}

// isValidGraphQLName reports whether name matches the specification's Name
// production, /[_A-Za-z][_0-9A-Za-z]*/.
func isValidGraphQLName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
