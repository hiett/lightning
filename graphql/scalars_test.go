package graphql_test

import (
	"math"
	"strings"
	"testing"

	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/graphql/schemabuilder"
	"github.com/hiett/lightning/internal"
	"github.com/hiett/lightning/internal/testgraphql"
	"github.com/stretchr/testify/require"
)

type scalarBag struct {
	Str     string
	Boolean bool
	Small   int32
	Tiny    int8
	Wide    int64
	Plain   int
	Unsig   uint64
	Float   float64
	Float32 float32
	Ident   schemabuilder.ID
	Raw     []byte
}

func scalarSchema(t *testing.T) *graphql.Schema {
	t.Helper()
	schema := schemabuilder.NewSchema()
	schema.Query().FieldFunc("bag", func() scalarBag {
		return scalarBag{
			Str:     "hello",
			Boolean: true,
			Small:   -7,
			Tiny:    3,
			Wide:    math.MaxInt64,
			Plain:   42,
			Unsig:   math.MaxUint64,
			Float:   1.5,
			Float32: 2.5,
			Ident:   schemabuilder.NewID("abc"),
			Raw:     []byte("bar"),
		}
	})
	return schema.MustBuild()
}

// TestStandardScalarNames checks that Go types map onto the scalar names the
// specification and every GraphQL tool expect.
func TestStandardScalarNames(t *testing.T) {
	sdl, err := graphql.PrintSchema(scalarSchema(t))
	require.NoError(t, err)

	for field, want := range map[string]string{
		"str":     "String!",
		"boolean": "Boolean!",
		"small":   "Int!",
		"tiny":    "Int!",
		"wide":    "Int64!",
		"plain":   "Int64!",
		"unsig":   "Int64!",
		"float":   "Float!",
		"float32": "Float!",
		"ident":   "ID!",
		"raw":     "Bytes!",
	} {
		require.Contains(t, sdl, "  "+field+": "+want+"\n", "field %s in:\n%s", field, sdl)
	}

	// The five built-in scalars come from the specification and must not be
	// redeclared; the custom ones must be.
	for _, builtin := range []string{"String", "Int", "Float", "Boolean", "ID"} {
		require.NotContains(t, sdl, "scalar "+builtin+"\n", "built-in scalar %s must not be redeclared", builtin)
	}
	require.Contains(t, sdl, "scalar Int64")
	require.Contains(t, sdl, "scalar Bytes")

	_, err = graphql.ASTSchema(scalarSchema(t))
	require.NoError(t, err)
}

// TestInt64SerialisesWithoutLoss checks that a 64-bit integer survives the
// round trip to the wire, which a JSON number could not do.
func TestInt64SerialisesWithoutLoss(t *testing.T) {
	built := scalarSchema(t)

	q := graphql.MustParse(`{ bag { str boolean small tiny wide plain unsig float ident raw } }`, nil)
	require.NoError(t, graphql.PrepareQuery(t.Context(), built.Query, q.SelectionSet))

	e := testgraphql.NewExecutorWrapper(t)
	result, err := e.Execute(t.Context(), built.Query, nil, q)
	require.NoError(t, err)

	bag := internal.AsJSON(result).(map[string]interface{})["bag"].(map[string]interface{})

	require.Equal(t, "hello", bag["str"])
	require.Equal(t, true, bag["boolean"])
	require.Equal(t, float64(-7), bag["small"], "an Int stays a JSON number")
	require.Equal(t, float64(3), bag["tiny"])
	require.Equal(t, "9223372036854775807", bag["wide"], "int64 max must survive exactly")
	require.Equal(t, "42", bag["plain"])
	require.Equal(t, "18446744073709551615", bag["unsig"], "uint64 max must survive exactly")
	require.Equal(t, 1.5, bag["float"])
	require.Equal(t, "abc", bag["ident"])
	require.Equal(t, "YmFy", bag["raw"], "bytes are base64")
}

// TestInt64ArgumentAcceptsStringAndNumber checks the input side of Int64.
func TestInt64ArgumentAcceptsStringAndNumber(t *testing.T) {
	schema := schemabuilder.NewSchema()
	schema.Query().FieldFunc("echo", func(args struct{ V int64 }) int64 { return args.V })
	built := schema.MustBuild()

	run := func(t *testing.T, literal string) (interface{}, error) {
		t.Helper()
		q, err := graphql.Parse("{ echo(v: "+literal+") }", nil)
		if err != nil {
			return nil, err
		}
		if err := graphql.PrepareQuery(t.Context(), built.Query, q.SelectionSet); err != nil {
			return nil, err
		}
		e := graphql.NewExecutor(graphql.NewImmediateGoroutineScheduler())
		result, err := e.Execute(t.Context(), built.Query, nil, q)
		if err != nil {
			return nil, err
		}
		return internal.AsJSON(result).(map[string]interface{})["echo"], nil
	}

	got, err := run(t, `"9223372036854775807"`)
	require.NoError(t, err)
	require.Equal(t, "9223372036854775807", got, "a string carries the full range")

	got, err = run(t, `5`)
	require.NoError(t, err)
	require.Equal(t, "5", got, "a small JSON number is still accepted")

	_, err = run(t, `1.5`)
	require.Error(t, err, "a non-integer must be rejected rather than truncated")

	_, err = run(t, `"not a number"`)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "Int64"), "error should name the scalar: %v", err)
}

// TestIDArgument checks that an ID argument accepts both wire forms the
// specification allows.
func TestIDArgument(t *testing.T) {
	schema := schemabuilder.NewSchema()
	schema.Query().FieldFunc("echo", func(args struct{ V schemabuilder.ID }) schemabuilder.ID { return args.V })
	built := schema.MustBuild()

	run := func(literal string) interface{} {
		q := graphql.MustParse("{ echo(v: "+literal+") }", nil)
		require.NoError(t, graphql.PrepareQuery(t.Context(), built.Query, q.SelectionSet))
		e := graphql.NewExecutor(graphql.NewImmediateGoroutineScheduler())
		result, err := e.Execute(t.Context(), built.Query, nil, q)
		require.NoError(t, err)
		return internal.AsJSON(result).(map[string]interface{})["echo"]
	}

	require.Equal(t, "xyz", run(`"xyz"`))
	require.Equal(t, "4", run(`4`), "an integer ID is the same identifier as its string form")
}

// TestIntArgumentIsBounded checks that an Int argument that does not fit its Go
// destination, or is not an integer at all, is rejected rather than silently
// truncated or rounded.
func TestIntArgumentIsBounded(t *testing.T) {
	schema := schemabuilder.NewSchema()
	schema.Query().FieldFunc("small", func(args struct{ V int32 }) int32 { return args.V })
	schema.Query().FieldFunc("tiny", func(args struct{ V int8 }) int32 { return int32(args.V) })
	schema.Query().FieldFunc("unsigned", func(args struct{ V uint8 }) int32 { return int32(args.V) })
	built := schema.MustBuild()

	run := func(t *testing.T, field, literal string) (interface{}, error) {
		t.Helper()
		q, err := graphql.Parse("{ "+field+"(v: "+literal+") }", nil)
		if err != nil {
			return nil, err
		}
		if err := graphql.PrepareQuery(t.Context(), built.Query, q.SelectionSet); err != nil {
			return nil, err
		}
		e := graphql.NewExecutor(graphql.NewImmediateGoroutineScheduler())
		result, err := e.Execute(t.Context(), built.Query, nil, q)
		if err != nil {
			return nil, err
		}
		return internal.AsJSON(result).(map[string]interface{})[field], nil
	}

	got, err := run(t, "small", "2147483647")
	require.NoError(t, err)
	require.Equal(t, float64(2147483647), got, "the largest Int must survive")

	_, err = run(t, "small", "2147483648")
	require.Error(t, err, "a value past the 32-bit maximum must be rejected, not wrapped")

	_, err = run(t, "small", "1.5")
	require.Error(t, err, "a fractional value must be rejected, not truncated")

	_, err = run(t, "tiny", "200")
	require.Error(t, err, "200 does not fit in an int8")

	_, err = run(t, "unsigned", "-1")
	require.Error(t, err, "a negative value must not become a large unsigned one")

	got, err = run(t, "unsigned", "255")
	require.NoError(t, err)
	require.Equal(t, float64(255), got)
}
