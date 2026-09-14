package graphql_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/graphql/schemabuilder"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

type Widget struct {
	Key  string
	Name string
}

func connectionSchema(t *testing.T) *graphql.Schema {
	t.Helper()

	schema := schemabuilder.NewSchema()

	widget := schema.Object("Widget", Widget{})
	widget.Key("key")
	widget.Node(
		func(w *Widget) string { return w.Key },
		func(ctx context.Context, id string) (*Widget, error) { return &Widget{Key: id, Name: id}, nil },
	)

	query := schema.Query()
	query.FieldFunc("widgets", func(ctx context.Context) []*Widget {
		return []*Widget{{Key: "1", Name: "one"}, {Key: "2", Name: "two"}}
	}, schemabuilder.Paginated)

	_ = schema.Mutation()
	return schema.MustBuild()
}

// TestConnectionConformance checks that a generated connection has the shape
// the Relay Cursor Connections specification requires, which is what
// relay-compiler validates against.
func TestConnectionConformance(t *testing.T) {
	built := connectionSchema(t)

	astSchema, err := graphql.ASTSchema(built)
	require.NoError(t, err, "the generated connection must be legal SDL")

	// Generated type names are derived from the node's GraphQL type name.
	require.Contains(t, astSchema.Types, "WidgetConnection")
	require.Contains(t, astSchema.Types, "WidgetEdge")
	for name := range astSchema.Types {
		require.NotContains(t, name, "NonNull", "a wrapper name leaked into a generated type name")
	}

	fieldType := func(typeName, fieldName string) string {
		t.Helper()
		def := astSchema.Types[typeName]
		require.NotNil(t, def, "no type %s", typeName)
		field := def.Fields.ForName(fieldName)
		require.NotNil(t, field, "type %s has no field %s", typeName, fieldName)
		return field.Type.String()
	}

	require.Equal(t, "[WidgetEdge!]!", fieldType("WidgetConnection", "edges"))
	require.Equal(t, "PageInfo!", fieldType("WidgetConnection", "pageInfo"))

	require.Equal(t, "Widget!", fieldType("WidgetEdge", "node"),
		"node must be non-null exactly once")
	require.Equal(t, "String!", fieldType("WidgetEdge", "cursor"))

	// PageInfo, with the specification's names and nullability.
	require.Equal(t, "Boolean!", fieldType("PageInfo", "hasNextPage"))
	require.Equal(t, "Boolean!", fieldType("PageInfo", "hasPreviousPage"))
	require.Equal(t, "String", fieldType("PageInfo", "startCursor"))
	require.Equal(t, "String", fieldType("PageInfo", "endCursor"))
	require.Nil(t, astSchema.Types["PageInfo"].Fields.ForName("hasPrevPage"),
		"the non-standard hasPrevPage must be gone")

	// The pagination arguments are the specification's, and count arguments are
	// Int so relay-compiler's Int-typed variables fit.
	widgets := astSchema.Types["Query"].Fields.ForName("widgets")
	require.NotNil(t, widgets)
	argType := func(name string) string {
		t.Helper()
		arg := widgets.Arguments.ForName(name)
		require.NotNil(t, arg, "widgets has no %s argument", name)
		return arg.Type.String()
	}
	require.Equal(t, "Int", argType("first"))
	require.Equal(t, "Int", argType("last"))
	require.Equal(t, "String", argType("after"))
	require.Equal(t, "String", argType("before"))

	// A node type inside a connection implements Node, so Relay can refetch it.
	require.Contains(t, astSchema.Types["Widget"].Interfaces, "Node")
	require.Equal(t, ast.Object, astSchema.Types["Widget"].Kind)
}

// TestConnectionPaginationQueryValidates runs the shape of query
// relay-compiler generates for usePaginationFragment through the validator.
func TestConnectionPaginationQueryValidates(t *testing.T) {
	v, err := graphql.NewValidator(connectionSchema(t))
	require.NoError(t, err)

	_, err = v.Parse(`
		query WidgetsPaginationQuery($count: Int! = 10, $cursor: String) {
			widgets(first: $count, after: $cursor) {
				edges {
					cursor
					node {
						id
						name
					}
				}
				pageInfo {
					endCursor
					hasNextPage
					hasPreviousPage
					startCursor
				}
			}
		}`, nil, "")
	require.NoError(t, err)

	// And the refetch query for a node inside the connection.
	_, err = v.Parse(`
		query WidgetRefetchQuery($id: ID!) {
			node(id: $id) {
				... on Widget { id name }
			}
		}`, nil, "")
	require.NoError(t, err)
}

// TestConnectionEmptyPageHasNullCursors checks that an empty page reports null
// cursors rather than empty strings, which is what the nullable types mean.
func TestConnectionEmptyPageHasNullCursors(t *testing.T) {
	schema := schemabuilder.NewSchema()
	widget := schema.Object("Widget", Widget{})
	widget.Key("key")
	schema.Query().FieldFunc("widgets", func(ctx context.Context) []*Widget { return nil }, schemabuilder.Paginated)
	built := schema.MustBuild()

	got, err := runNodeQuery(t, built, `{ widgets { pageInfo { startCursor endCursor hasNextPage } edges { cursor } } }`)
	require.NoError(t, err)

	widgets := got.(map[string]interface{})["widgets"].(map[string]interface{})
	pageInfo := widgets["pageInfo"].(map[string]interface{})
	require.Nil(t, pageInfo["startCursor"])
	require.Nil(t, pageInfo["endCursor"])
	require.Equal(t, false, pageInfo["hasNextPage"])
	require.Empty(t, widgets["edges"])
}

// TestConnectionTypesAreSharedAcrossFields checks that two paginated fields over
// the same node type produce one connection type, not two objects that happen
// to share a name. A schema cannot hold two types with one name, and whichever
// the printer reached first would otherwise win — silently, and not necessarily
// the same one twice.
func TestConnectionTypesAreSharedAcrossFields(t *testing.T) {
	schema := schemabuilder.NewSchema()
	widget := schema.Object("Widget", Widget{})
	widget.Key("key")

	query := schema.Query()
	query.FieldFunc("widgets", func(ctx context.Context) []*Widget { return nil }, schemabuilder.Paginated)
	query.FieldFunc("otherWidgets", func(ctx context.Context) []*Widget { return nil }, schemabuilder.Paginated)
	// A value slice, which used to generate a second "NonNullWidgetConnection".
	query.FieldFunc("valueWidgets", func(ctx context.Context) []Widget { return nil }, schemabuilder.Paginated)

	built := schema.MustBuild()

	queryObject := built.Query.(*graphql.Object)
	unwrap := func(fieldName string) graphql.Type {
		t.Helper()
		field := queryObject.Fields[fieldName]
		require.NotNil(t, field, "no field %s", fieldName)
		typ := field.Type
		if nonNull, ok := typ.(*graphql.NonNull); ok {
			typ = nonNull.Type
		}
		return typ
	}

	first := unwrap("widgets")
	require.Same(t, first, unwrap("otherWidgets"), "two fields over the same node type must share one connection type")
	require.Same(t, first, unwrap("valueWidgets"), "a value slice and a pointer slice describe the same connection")

	// And the schema still prints and loads.
	sdl, err := graphql.PrintSchema(built)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(sdl, "type WidgetConnection"), "printed schema:\n%s", sdl)
	require.Equal(t, 1, strings.Count(sdl, "type WidgetEdge"), "printed schema:\n%s", sdl)

	_, err = graphql.ASTSchema(built)
	require.NoError(t, err)
}

func widgetConnectionSchema(t *testing.T) *graphql.Schema {
	t.Helper()
	schema := schemabuilder.NewSchema()
	widget := schema.Object("Widget", Widget{})
	widget.Key("key")
	schema.Query().FieldFunc("widgets", func(ctx context.Context) []*Widget {
		return []*Widget{
			{Key: "1", Name: "one"}, {Key: "2", Name: "two"}, {Key: "3", Name: "three"},
			{Key: "4", Name: "four"}, {Key: "5", Name: "five"},
		}
	}, schemabuilder.Paginated)
	return schema.MustBuild()
}

func widgetCursor(key string) string {
	return base64.StdEncoding.EncodeToString([]byte(key))
}

// TestConnectionHasNextPageWithBothCursors checks hasNextPage when after and
// before are used together.
//
// The count the "before" check compared against was taken before the "after"
// cursor had sliced the list, so the last edge never looked like the last one
// and the connection claimed there was another page. A Relay client paginating
// forward then asked for a page that came back empty, for ever.
func TestConnectionHasNextPageWithBothCursors(t *testing.T) {
	built := widgetConnectionSchema(t)

	hasNextPage := func(t *testing.T, args string) bool {
		t.Helper()
		got, err := runNodeQuery(t, built, `{ widgets(`+args+`) { pageInfo { hasNextPage } } }`)
		require.NoError(t, err)
		pageInfo := got.(map[string]interface{})["widgets"].(map[string]interface{})["pageInfo"].(map[string]interface{})
		return pageInfo["hasNextPage"].(bool)
	}

	require.False(t, hasNextPage(t, `before: "`+widgetCursor("5")+`"`),
		"nothing follows the last widget")
	require.False(t, hasNextPage(t, `after: "`+widgetCursor("1")+`", before: "`+widgetCursor("5")+`"`),
		"nothing follows the last widget, whichever cursor the page starts at")
	require.False(t, hasNextPage(t, `after: "`+widgetCursor("2")+`", before: "`+widgetCursor("5")+`"`))
	require.True(t, hasNextPage(t, `after: "`+widgetCursor("1")+`", before: "`+widgetCursor("4")+`"`),
		"widget 5 follows widget 4, so there is another page")
}

// TestConnectionFilterAndSortMisuseAreClientErrors checks that asking a
// connection to sort or filter by something it cannot produces a usable
// message.
//
// Sorting by an unregistered field raised a plain Go error, which sanitises to
// "Internal server error" and tells the client nothing. Filtering with no
// filterable fields dropped every row and reported a total of zero, which reads
// as "no matches" rather than "this cannot be filtered".
func TestConnectionFilterAndSortMisuseAreClientErrors(t *testing.T) {
	built := widgetConnectionSchema(t)

	_, err := runNodeQuery(t, built, `{ widgets(sortBy: "name") { totalCount } }`)
	require.Error(t, err)
	require.Contains(t, graphql.SanitizeError(err), `unknown sort field "name"`,
		"the message must survive sanitisation, or the client learns nothing")

	_, err = runNodeQuery(t, built, `{ widgets(filterText: "one") { totalCount } }`)
	require.Error(t, err)
	require.Contains(t, graphql.SanitizeError(err), "no filterable fields")
}
