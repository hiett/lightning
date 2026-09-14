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
		"type Mutation {\n  touch: Boolean!\n}",
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
	require.Equal(t, "String!", fieldTypeString(t, inner, "x"))
	require.Equal(t, "String", fieldTypeString(t, inner, "y"))
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

type collidingPayload struct{ Totally string }

// TestSchemaRejectsANameCollision checks that two different types sharing a
// name is an error rather than a coin toss.
//
// A GraphQL schema has one namespace. Keeping the first type and dropping the
// second lost every type reachable only through the loser, and which one won
// was decided by Go map iteration — so the exported schema was wrong, silently,
// and differently on each run. The schema builder catches the case it can see,
// and the printer catches the rest.
func TestSchemaRejectsANameCollision(t *testing.T) {
	schema := schemabuilder.NewSchema()

	inner := schema.Object("SDLInner", sdlInnerSource{})
	inner.Key("key")

	// A hand-written type whose name collides with the generated edge type.
	schema.Object("SDLInnerEdge", collidingPayload{})

	query := schema.Query()
	query.FieldFunc("inners", func() []*sdlInnerSource { return nil }, schemabuilder.Paginated)
	query.FieldFunc("impostor", func() *collidingPayload { return nil })

	_, err := schema.Build()
	require.Error(t, err, "a generated type name colliding with a registered one must be refused")
	require.Contains(t, err.Error(), "SDLInnerEdge")
}

// TestPrintSchemaRejectsANameCollision covers the printer's own guard, for a
// schema assembled by hand rather than through the builder.
func TestPrintSchemaRejectsANameCollision(t *testing.T) {
	shared := &graphql.Scalar{Type: "String"}
	stringField := func() *graphql.Field {
		return &graphql.Field{Type: &graphql.NonNull{Type: shared}}
	}

	// Two different objects, both called Thing.
	first := &graphql.Object{Name: "Thing", Fields: map[string]*graphql.Field{"a": stringField()}}
	second := &graphql.Object{Name: "Thing", Fields: map[string]*graphql.Field{"b": stringField()}}

	built := &graphql.Schema{
		Query: &graphql.Object{
			Name: "Query",
			Fields: map[string]*graphql.Field{
				"first":  {Type: first},
				"second": {Type: second},
			},
		},
	}

	// Whichever type collect happens to reach first, the answer must be the
	// same error every time.
	for range 8 {
		_, err := graphql.PrintSchema(built)
		require.Error(t, err)
		require.Contains(t, err.Error(), "both named Thing")
	}
}

type sdlInnerSource struct {
	Key  string
	Name string
}

// TestPrintSchemaNamesAnonymousInputsDeterministically checks that an input
// object the schema left unnamed gets the same generated name every time, and
// that printing does not modify the schema it is printing.
//
// The name used to come from a counter on the printer written straight into the
// InputObject, so printing the same schema twice produced two different names,
// two concurrent prints were a data race, and the name depended on map
// iteration order.
//
// schemabuilder refuses an anonymous nested argument struct outright, so this
// is only reachable for a schema assembled by hand.
func TestPrintSchemaNamesAnonymousInputsDeterministically(t *testing.T) {
	build := func() *graphql.Schema {
		str := &graphql.NonNull{Type: &graphql.Scalar{Type: "String"}}
		unnamed := &graphql.InputObject{
			InputFields: map[string]graphql.Type{"term": str, "scope": str},
		}
		return &graphql.Schema{
			Query: &graphql.Object{
				Name: "Query",
				Fields: map[string]*graphql.Field{
					"search": {
						Type: str,
						Args: map[string]graphql.Type{"filter": unnamed},
					},
				},
			},
		}
	}

	built := build()

	first, err := graphql.PrintSchema(built)
	require.NoError(t, err)

	// The same schema printed again, which used to bump a counter stored on the
	// schema itself and so produce a different name.
	second, err := graphql.PrintSchema(built)
	require.NoError(t, err)
	require.Equal(t, first, second, "printing twice must give the same document")

	// And a fresh build of the same schema, which is what a CI run does.
	third, err := graphql.PrintSchema(build())
	require.NoError(t, err)
	require.Equal(t, first, third, "the name must not depend on process state")

	require.Contains(t, first, "input AnonymousInput_")
}
