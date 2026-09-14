package graphql_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/graphql/introspection"
	"github.com/hiett/lightning/graphql/schemabuilder"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// This file checks, mechanically, the acceptance criteria the refactor was
// carried out against, so that a regression in any of them fails the build
// rather than being noticed by a client.

type AcceptanceThing struct {
	Key    string
	Name   string
	Count  int32
	Wide   int64
	Ratio  float64
	Live   bool
	Ident  schemabuilder.ID
	Binary []byte
}

type AcceptanceOther struct {
	Key   string
	Name  string
	Extra string
}

// AcceptanceNamed is an interface over the two object types.
type AcceptanceNamed struct {
	schemabuilder.Interface

	*AcceptanceThing
	*AcceptanceOther
}

func acceptanceSchema(t *testing.T) *graphql.Schema {
	t.Helper()

	builder := schemabuilder.NewSchema()

	builder.Interface("Named", AcceptanceNamed{}).Fields("name")

	thing := builder.Object("Thing", AcceptanceThing{})
	thing.Key("key")
	thing.Node(
		func(v *AcceptanceThing) string { return v.Key },
		func(ctx context.Context, id string) (*AcceptanceThing, error) {
			return &AcceptanceThing{Key: id, Name: "thing " + id}, nil
		},
	)

	other := builder.Object("Other", AcceptanceOther{})
	other.Node(
		func(v *AcceptanceOther) string { return v.Key },
		func(ctx context.Context, id string) (*AcceptanceOther, error) {
			return &AcceptanceOther{Key: id, Name: "other " + id}, nil
		},
	)

	query := builder.Query()
	query.FieldFunc("things", func(ctx context.Context) []*AcceptanceThing {
		return []*AcceptanceThing{{Key: "1", Name: "one"}, {Key: "2", Name: "two"}}
	}, schemabuilder.Paginated)
	query.FieldFunc("named", func() *AcceptanceNamed {
		return &AcceptanceNamed{AcceptanceThing: &AcceptanceThing{Key: "1", Name: "one"}}
	})

	builder.Mutation().FieldFunc("touch", func() bool { return true })
	builder.Subscription().FieldFunc("things", func(ctx context.Context) []*AcceptanceThing { return nil },
		schemabuilder.Paginated)

	return builder.MustBuild()
}

// TestAcceptanceIntrospectionReportsBuiltinScalars checks the five scalars every
// GraphQL tool assumes exist.
func TestAcceptanceIntrospectionReportsBuiltinScalars(t *testing.T) {
	built := acceptanceSchema(t)
	introspection.AddIntrospectionToSchema(built)

	raw, err := introspection.RunIntrospectionQuery(built)
	require.NoError(t, err)

	var result struct {
		Schema struct {
			Types []struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"types"`
			SubscriptionType *struct {
				Name string `json:"name"`
			} `json:"subscriptionType"`
		} `json:"__schema"`
	}
	require.NoError(t, json.Unmarshal(raw, &result))

	scalars := map[string]bool{}
	for _, typ := range result.Schema.Types {
		if typ.Kind == "SCALAR" {
			scalars[typ.Name] = true
		}
	}

	for _, name := range []string{"String", "Int", "Float", "Boolean", "ID"} {
		require.True(t, scalars[name], "introspection must report the built-in scalar %s; saw %v", name, scalars)
	}
	for _, gone := range []string{"string", "int64", "bool", "float64", "bytes"} {
		require.False(t, scalars[gone], "the old scalar name %s must be gone", gone)
	}

	// The introspection query asks for subscriptionType, and the schema has one.
	require.NotNil(t, result.Schema.SubscriptionType)
	require.Equal(t, "Subscription", result.Schema.SubscriptionType.Name)
}

// TestAcceptanceIntrospectionReportsInterfaces checks that interfaces are real
// in introspection, not the hardcoded nil they used to be.
func TestAcceptanceIntrospectionReportsInterfaces(t *testing.T) {
	built := acceptanceSchema(t)
	introspection.AddIntrospectionToSchema(built)

	raw, err := introspection.RunIntrospectionQuery(built)
	require.NoError(t, err)

	var result struct {
		Schema struct {
			Types []struct {
				Kind          string                  `json:"kind"`
				Name          string                  `json:"name"`
				Interfaces    []struct{ Name string } `json:"interfaces"`
				PossibleTypes []struct{ Name string } `json:"possibleTypes"`
			} `json:"types"`
		} `json:"__schema"`
	}
	require.NoError(t, json.Unmarshal(raw, &result))

	byName := map[string]int{}
	for i, typ := range result.Schema.Types {
		byName[typ.Name] = i
	}

	named := result.Schema.Types[byName["Named"]]
	require.Equal(t, "INTERFACE", named.Kind, "the INTERFACE kind must be reported")
	require.Len(t, named.PossibleTypes, 2, "an interface must report its possible types")

	thing := result.Schema.Types[byName["Thing"]]
	require.Equal(t, "OBJECT", thing.Kind)
	implemented := []string{}
	for _, iface := range thing.Interfaces {
		implemented = append(implemented, iface.Name)
	}
	require.ElementsMatch(t, []string{"Named", "Node"}, implemented,
		"an object must report the interfaces it implements")
}

// TestAcceptanceSDLRoundTrips checks that the exported schema is legal SDL and
// re-parses to the same set of types introspection reports.
func TestAcceptanceSDLRoundTrips(t *testing.T) {
	built := acceptanceSchema(t)

	sdl, err := graphql.PrintSchema(built)
	require.NoError(t, err)

	reparsed, err := gqlparser.LoadSchema(&ast.Source{Name: "schema.graphql", Input: sdl})
	require.NoError(t, err, "exported SDL must parse back:\n%s", sdl)

	// Every type in the printed document is present after re-parsing, and the
	// roots line up.
	require.Equal(t, "Query", reparsed.Query.Name)
	require.Equal(t, "Mutation", reparsed.Mutation.Name)
	require.Equal(t, "Subscription", reparsed.Subscription.Name)

	for _, name := range []string{"Thing", "Other", "Named", "Node", "ThingConnection", "ThingEdge", "PageInfo", "Int64"} {
		require.Contains(t, reparsed.Types, name, "printed schema:\n%s", sdl)
	}

	// And the same set of scalars introspection reports.
	introspection.AddIntrospectionToSchema(built)
	raw, err := introspection.RunIntrospectionQuery(built)
	require.NoError(t, err)

	var result struct {
		Schema struct {
			Types []struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"types"`
		} `json:"__schema"`
	}
	require.NoError(t, json.Unmarshal(raw, &result))

	for _, typ := range result.Schema.Types {
		if strings.HasPrefix(typ.Name, "__") {
			continue
		}
		require.Contains(t, reparsed.Types, typ.Name,
			"introspection reports %s but the exported SDL does not declare it", typ.Name)
	}
}

// TestAcceptanceConnectionNames checks the Relay connection field names.
func TestAcceptanceConnectionNames(t *testing.T) {
	sdl, err := graphql.PrintSchema(acceptanceSchema(t))
	require.NoError(t, err)

	require.Contains(t, sdl, "hasPreviousPage")
	require.NotContains(t, sdl, "hasPrevPage")
	require.Contains(t, sdl, "type ThingConnection")
	require.NotContains(t, sdl, "NonNull")
}

// TestAcceptanceNodeRoundTrip checks a global id round-trips through node.
func TestAcceptanceNodeRoundTrip(t *testing.T) {
	built := acceptanceSchema(t)

	encoded, err := schemabuilder.Base64GlobalIDCodec{}.Encode("Thing", "abc")
	require.NoError(t, err)

	got, err := runNodeQuery(t, built, `{ node(id: "`+encoded+`") { __typename id ... on Thing { name } } }`)
	require.NoError(t, err)

	node := got.(map[string]interface{})["node"].(map[string]interface{})
	require.Equal(t, "Thing", node["__typename"])
	require.Equal(t, encoded, node["id"])
	require.Equal(t, "thing abc", node["name"])
}

// TestAcceptanceErrorsHaveMessageAndPath checks the response error shape.
func TestAcceptanceErrorsHaveMessageAndPath(t *testing.T) {
	builder := schemabuilder.NewSchema()
	builder.Query().FieldFunc("rows", func() []*AcceptanceOther {
		return []*AcceptanceOther{{Key: "ok"}, {Key: "bad"}}
	})
	other := builder.Object("Other", AcceptanceOther{})
	other.FieldFunc("check", func(o *AcceptanceOther) (string, error) {
		if o.Key == "bad" {
			return "", graphql.NewClientError("no good")
		}
		return "fine", nil
	})
	built := builder.MustBuild()

	q := graphql.MustParse(`{ rows { check } }`, nil)
	require.NoError(t, graphql.PrepareQuery(context.Background(), built.Query, q.SelectionSet))

	e := graphql.NewExecutor(graphql.NewImmediateGoroutineScheduler())
	_, execErr := e.Execute(context.Background(), built.Query, nil, q)
	require.Error(t, execErr)

	encoded, err := json.Marshal(graphql.NewResponse(nil, execErr))
	require.NoError(t, err)

	var response struct {
		Data   interface{} `json:"data"`
		Errors []struct {
			Message string        `json:"message"`
			Path    []interface{} `json:"path"`
		} `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(encoded, &response))

	require.Len(t, response.Errors, 1)
	require.Equal(t, "no good", response.Errors[0].Message)
	require.Equal(t, []interface{}{"rows", float64(1), "check"}, response.Errors[0].Path)

	// A field error keeps data present; a request error omits it.
	require.Contains(t, string(encoded), `"data"`)

	requestError, err := json.Marshal(graphql.NewRequestErrorResponse(graphql.NewClientError("nope")))
	require.NoError(t, err)
	require.NotContains(t, string(requestError), `"data"`)
}
