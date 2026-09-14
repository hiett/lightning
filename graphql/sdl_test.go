package graphql_test

import (
	"strings"
	"testing"

	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/graphql/schemabuilder"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

type SDLInner struct {
	Name  string
	Count int64
}

type SDLNestedArg struct {
	X string
	Y *string
}

func sdlTestSchema(t *testing.T) *graphql.Schema {
	t.Helper()

	schema := schemabuilder.NewSchema()

	query := schema.Query()
	query.FieldFunc("inner", func() SDLInner { return SDLInner{} })
	query.FieldFunc("inners", func() []*SDLInner { return nil })
	query.FieldFunc("search", func(args struct {
		Term    string
		Limit   *int64
		Nested  SDLNestedArg
		Options []string
	}) *SDLInner {
		return nil
	})

	mutation := schema.Mutation()
	mutation.FieldFunc("touch", func() bool { return true })

	return schema.MustBuild()
}

func TestPrintSchema(t *testing.T) {
	sdl, err := graphql.PrintSchema(sdlTestSchema(t))
	require.NoError(t, err)

	for _, want := range []string{
		"schema {\n  query: Query\n  mutation: Mutation\n}",
		"type SDLInner {",
		"input SDLNestedArg_InputObject {",
		"type Mutation {\n  touch: bool!\n}",
	} {
		require.Contains(t, sdl, want, "printed schema:\n%s", sdl)
	}

	// Output must be stable so it can be committed and diffed.
	again, err := graphql.PrintSchema(sdlTestSchema(t))
	require.NoError(t, err)
	require.Equal(t, sdl, again)
}

// TestPrintSchemaRoundTrips checks the acceptance criterion for SDL export:
// the printed document parses back with gqlparser, and the types it declares
// survive the round trip.
func TestPrintSchemaRoundTrips(t *testing.T) {
	built := sdlTestSchema(t)

	sdl, err := graphql.PrintSchema(built)
	require.NoError(t, err)

	astSchema, err := graphql.ASTSchema(built)
	require.NoError(t, err)

	require.NotNil(t, astSchema.Query)
	require.Equal(t, "Query", astSchema.Query.Name)
	require.NotNil(t, astSchema.Mutation)
	require.Equal(t, "Mutation", astSchema.Mutation.Name)

	for _, name := range []string{"SDLInner", "SDLNestedArg_InputObject", "Query", "Mutation"} {
		require.Contains(t, astSchema.Types, name, "printed schema:\n%s", sdl)
	}

	// Nullability survives: a *string field is nullable, a string field is not.
	inner := astSchema.Types["SDLNestedArg_InputObject"]
	require.Equal(t, "string!", fieldTypeString(t, inner, "x"))
	require.Equal(t, "string", fieldTypeString(t, inner, "y"))
}

func fieldTypeString(t *testing.T, def *ast.Definition, name string) string {
	t.Helper()
	field := def.Fields.ForName(name)
	require.NotNil(t, field, "type %s has no field %s", def.Name, name)
	return field.Type.String()
}

// TestPrintSchemaOmitsEmptyMutation checks that schemabuilder's always-present
// but often empty Mutation object is left out rather than printed as an illegal
// fieldless type.
func TestPrintSchemaOmitsEmptyMutation(t *testing.T) {
	schema := schemabuilder.NewSchema()
	schema.Query().FieldFunc("ok", func() bool { return true })
	_ = schema.Mutation()

	sdl, err := graphql.PrintSchema(schema.MustBuild())
	require.NoError(t, err)

	require.NotContains(t, sdl, "mutation: Mutation")
	require.NotContains(t, sdl, "type Mutation")
	require.True(t, strings.Contains(sdl, "type Query {"))

	_, err = graphql.ASTSchema(schema.MustBuild())
	require.NoError(t, err)
}
