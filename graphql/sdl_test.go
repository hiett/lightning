package graphql_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

type SDLInner struct {
	lightning.Meta `graphql:"SDLInner"`

	Name  string
	Count int64
}

type SDLNestedArg struct {
	lightning.Meta `graphql:"SDLNestedArg"`

	X string
	Y *string
}

type sdlSearchArgs struct {
	Term    string
	Limit   *int64
	Nested  SDLNestedArg
	Options []string
}

func sdlTestSchema(t *testing.T) *graphql.Schema {
	t.Helper()

	b := lightning.New()
	lightning.Object[SDLInner](b)

	query := b.Query()
	query.Field("inner", func(ctx context.Context, _ *lightning.Root) (SDLInner, error) {
		return SDLInner{}, nil
	})
	query.Field("inners", func(ctx context.Context, _ *lightning.Root) ([]*SDLInner, error) {
		return nil, nil
	})
	query.FieldArgs("search", func(ctx context.Context, _ *lightning.Root, args sdlSearchArgs) (*SDLInner, error) {
		return nil, nil
	})

	b.Mutation().Field("touch", func(ctx context.Context, _ *lightning.Root) (bool, error) {
		return true, nil
	})

	return b.MustBuild()
}

func TestPrintSchema(t *testing.T) {
	sdl, err := graphql.PrintSchema(sdlTestSchema(t))
	require.NoError(t, err)

	for _, want := range []string{
		"schema {\n  query: Query\n  mutation: Mutation\n}",
		"type SDLInner {",
		"input SDLNestedArg {",
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

	for _, name := range []string{"SDLInner", "SDLNestedArg", "Query", "Mutation"} {
		require.Contains(t, astSchema.Types, name, "printed schema:\n%s", sdl)
	}

	// Nullability survives: a *string field is nullable, a string field is not.
	inner := astSchema.Types["SDLNestedArg"]
	require.Equal(t, "String!", fieldTypeString(t, inner, "x"))
	require.Equal(t, "String", fieldTypeString(t, inner, "y"))
}

func fieldTypeString(t *testing.T, def *ast.Definition, name string) string {
	t.Helper()
	field := def.Fields.ForName(name)
	require.NotNil(t, field, "type %s has no field %s", def.Name, name)
	return field.Type.String()
}

// TestPrintSchemaOmitsEmptyMutation checks that the always-present but often
// empty Mutation object is left out rather than printed as an illegal fieldless
// type.
func TestPrintSchemaOmitsEmptyMutation(t *testing.T) {
	build := func() *graphql.Schema {
		b := lightning.New()
		b.Query().Field("ok", func(ctx context.Context, _ *lightning.Root) (bool, error) {
			return true, nil
		})
		return b.MustBuild()
	}

	sdl, err := graphql.PrintSchema(build())
	require.NoError(t, err)

	require.NotContains(t, sdl, "mutation: Mutation")
	require.NotContains(t, sdl, "type Mutation")
	require.True(t, strings.Contains(sdl, "type Query {"))

	_, err = graphql.ASTSchema(build())
	require.NoError(t, err)
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

// TestPrintSchemaNamesAnonymousInputsDeterministically checks that an input
// object the schema left unnamed gets the same generated name every time, and
// that printing does not modify the schema it is printing.
//
// The name used to come from a counter on the printer written straight into the
// InputObject, so printing the same schema twice produced two different names,
// two concurrent prints were a data race, and the name depended on map
// iteration order.
//
// The schema builder names every input object after the Go type it came from,
// so this is only reachable for a schema assembled by hand.
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
