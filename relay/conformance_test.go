package relay_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/relay"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

// Widget is the node a connection is built over.
type Widget struct {
	lightning.Meta `graphql:"Widget"`

	Key  string `graphql:"-"`
	Name string
}

func (w *Widget) NodeID() string { return w.Key }

func fetchWidget(ctx context.Context, id string) (*Widget, error) {
	return &Widget{Key: id, Name: id}, nil
}

func widgetSchema(t *testing.T, widgets ...*Widget) *graphql.Schema {
	t.Helper()

	b := lightning.New(relay.Plugin())
	lightning.Object[Widget](b)
	relay.Node(b, fetchWidget)

	relay.Connection(b.Query(), "widgets", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Widget, error) {
		return widgets, nil
	})

	return b.MustBuild()
}

// TestConnectionConformance checks that a generated connection has the shape
// the Relay Cursor Connections specification requires, which is what
// relay-compiler validates against.
func TestConnectionConformance(t *testing.T) {
	built := widgetSchema(t, &Widget{Key: "1", Name: "one"}, &Widget{Key: "2", Name: "two"})

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
	v, err := graphql.NewValidator(widgetSchema(t))
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
	got := run(t, widgetSchema(t), `{ widgets { pageInfo { startCursor endCursor hasNextPage } edges { cursor } } }`)

	widgets := got["widgets"].(map[string]any)
	pageInfo := widgets["pageInfo"].(map[string]any)
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
	b := lightning.New(relay.Plugin())
	lightning.Object[Widget](b)
	relay.Node(b, fetchWidget)

	q := b.Query()
	relay.Connection(q, "widgets", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Widget, error) {
		return nil, nil
	})
	relay.Connection(q, "otherWidgets", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Widget, error) {
		return nil, nil
	})
	// A value slice, which used to generate a second "NonNullWidgetConnection".
	relay.Connection(q, "valueWidgets", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]Widget, error) {
		return nil, nil
	})

	built := b.MustBuild()

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
	built := widgetSchema(t,
		&Widget{Key: "1", Name: "one"}, &Widget{Key: "2", Name: "two"}, &Widget{Key: "3", Name: "three"},
		&Widget{Key: "4", Name: "four"}, &Widget{Key: "5", Name: "five"},
	)

	hasNextPage := func(t *testing.T, args string) bool {
		t.Helper()
		got := run(t, built, `{ widgets(`+args+`) { pageInfo { hasNextPage } } }`)
		pageInfo := got["widgets"].(map[string]any)["pageInfo"].(map[string]any)
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

// TestConnectionSortAndFilterMisuse checks what happens when a client asks a
// connection to order or search by something it cannot.
//
// Where the old library answered a bad sortBy with a plain Go error, which
// sanitises to "Internal server error" and tells a client nothing, there is now
// nothing to get wrong: a type with no sortable field has no sortBy argument,
// so the mistake is caught by validation before anything runs.
func TestConnectionSortAndFilterMisuse(t *testing.T) {
	built := widgetSchema(t, &Widget{Key: "1", Name: "one"})

	v, err := graphql.NewValidator(built)
	require.NoError(t, err)

	_, err = v.Parse(`{ widgets(sortBy: "name") { totalCount } }`, nil, "")
	require.ErrorContains(t, err, "sortBy")

	_, err = v.Parse(`{ widgets(filterText: "one") { totalCount } }`, nil, "")
	require.ErrorContains(t, err, "filterText")

	// And where a type does declare sortable fields, a name that is not one of
	// them is a client error whose message survives sanitisation.
	_, err = runErr(t, plainNotes(t), `{ notes(sortBy: "nope") { totalCount } }`)
	require.Error(t, err)
	require.Contains(t, graphql.SanitizeError(err), `Note cannot be sorted by "nope"`)
}

// collidingEdge is a hand-written type whose name is the one a connection over
// Widget generates.
type collidingEdge struct {
	lightning.Meta `graphql:"WidgetEdge"`

	Totally string
}

// TestGeneratedNameCollisionIsReported checks that a type sharing a name with
// one a connection generates is an error rather than a coin toss.
//
// A GraphQL schema has one namespace. Keeping the first type and dropping the
// second lost every type reachable only through the loser, and which one won
// was decided by Go map iteration — so the exported schema was wrong, silently,
// and differently on each run.
func TestGeneratedNameCollisionIsReported(t *testing.T) {
	b := lightning.New(relay.Plugin())
	lightning.Object[Widget](b)
	lightning.Object[collidingEdge](b)
	relay.Node(b, fetchWidget)

	q := b.Query()
	relay.Connection(q, "widgets", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Widget, error) {
		return nil, nil
	})
	q.Field("impostor", func(ctx context.Context, _ *lightning.Root) (*collidingEdge, error) {
		return nil, nil
	})

	built, err := b.Build()
	if err != nil {
		require.ErrorContains(t, err, "WidgetEdge")
		return
	}

	// The builder cannot see a name a plugin generated for a type it never
	// declared, so the printer is what catches it — and it must catch it the
	// same way every time rather than on some runs.
	for range 8 {
		_, err := graphql.PrintSchema(built)
		require.ErrorContains(t, err, "both named WidgetEdge")
	}
}
