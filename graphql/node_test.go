package graphql_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"testing"

	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/graphql/schemabuilder"
	"github.com/hiett/lightning/internal"
	"github.com/hiett/lightning/internal/testgraphql"
	"github.com/stretchr/testify/require"
)

type Author struct {
	Key  string
	Name string
}

type Book struct {
	Key   string
	Title string
}

var (
	authors = map[string]*Author{"a1": {Key: "a1", Name: "Ursula"}}
	books   = map[string]*Book{"b1": {Key: "b1", Title: "The Dispossessed"}}
)

func nodeSchema(t *testing.T) *graphql.Schema {
	t.Helper()

	schema := schemabuilder.NewSchema()

	author := schema.Object("Author", Author{})
	author.Node(
		func(a *Author) string { return a.Key },
		func(ctx context.Context, id string) (*Author, error) { return authors[id], nil },
	)

	book := schema.Object("Book", Book{})
	book.Node(
		func(ctx context.Context, b *Book) (string, error) { return b.Key, nil },
		func(id string) (*Book, error) {
			if id == "explode" {
				return nil, errors.New("database on fire")
			}
			return books[id], nil
		},
	)

	query := schema.Query()
	query.FieldFunc("firstAuthor", func() *Author { return authors["a1"] })

	_ = schema.Mutation()
	return schema.MustBuild()
}

func runNodeQuery(t *testing.T, built *graphql.Schema, query string) (interface{}, error) {
	t.Helper()
	q, err := graphql.Parse(query, nil)
	if err != nil {
		return nil, err
	}
	if err := graphql.PrepareQuery(context.Background(), built.Query, q.SelectionSet); err != nil {
		return nil, err
	}
	e := testgraphql.NewExecutorWrapper(t)
	result, err := e.Execute(context.Background(), built.Query, nil, q)
	if err != nil {
		return nil, err
	}
	return internal.AsJSON(result), nil
}

func globalID(t *testing.T, typeName, localID string) string {
	t.Helper()
	encoded, err := schemabuilder.Base64GlobalIDCodec{}.Encode(typeName, localID)
	require.NoError(t, err)
	return encoded
}

// TestNodeRoundTrip covers the acceptance criterion: encode a global id, query
// node with it, and get the same object back.
func TestNodeRoundTrip(t *testing.T) {
	built := nodeSchema(t)

	// The id a type reports is the global one, and it decodes to the type and
	// its local key.
	got, err := runNodeQuery(t, built, `{ firstAuthor { id name } }`)
	require.NoError(t, err)

	author := got.(map[string]interface{})["firstAuthor"].(map[string]interface{})
	require.Equal(t, "Ursula", author["name"])

	encoded := author["id"].(string)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	require.Equal(t, "Author:a1", string(decoded))

	// And that id fetches the object back through node.
	got, err = runNodeQuery(t, built, fmt.Sprintf(`{
		node(id: %q) {
			__typename
			id
			... on Author { name }
			... on Book { title }
		}
	}`, encoded))
	require.NoError(t, err)

	require.Equal(t, internal.ParseJSON(fmt.Sprintf(
		`{"node": {"__typename": "Author", "id": %q, "name": "Ursula"}}`, encoded)), got)
}

// TestNodeAcrossTypes checks that node dispatches on the type in the id.
func TestNodeAcrossTypes(t *testing.T) {
	built := nodeSchema(t)

	got, err := runNodeQuery(t, built, fmt.Sprintf(`{
		node(id: %q) { __typename ... on Book { title } }
	}`, globalID(t, "Book", "b1")))
	require.NoError(t, err)

	require.Equal(t, internal.ParseJSON(
		`{"node": {"__typename": "Book", "title": "The Dispossessed"}}`), got)
}

// TestNodes checks the plural form, including a null for an id that resolves to
// nothing.
func TestNodes(t *testing.T) {
	built := nodeSchema(t)

	got, err := runNodeQuery(t, built, fmt.Sprintf(`{
		nodes(ids: [%q, %q, %q]) { __typename }
	}`, globalID(t, "Author", "a1"), globalID(t, "Book", "missing"), globalID(t, "Book", "b1")))
	require.NoError(t, err)

	require.Equal(t, internal.ParseJSON(
		`{"nodes": [{"__typename": "Author"}, null, {"__typename": "Book"}]}`), got)
}

// TestNodeUnknownTypeIsACleanError covers the acceptance criterion that an id
// for an unregistered type produces an error, not a panic.
func TestNodeUnknownTypeIsACleanError(t *testing.T) {
	built := nodeSchema(t)

	_, err := runNodeQuery(t, built, fmt.Sprintf(`{ node(id: %q) { id } }`, globalID(t, "Ghost", "1")))
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown node type "Ghost"`)

	// A string that is not a global id at all is also a clean error.
	_, err = runNodeQuery(t, built, `{ node(id: "not base64 at all!!") { id } }`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "malformed global id")

	// Both are client errors, so the message survives sanitisation.
	errs := graphql.AsResponseErrors(err)
	require.Len(t, errs, 1)
	require.Contains(t, errs[0].Message, "malformed global id")
}

// TestNodeMissingObjectIsNull checks that a well-formed id for an object that
// does not exist resolves to null rather than an error.
func TestNodeMissingObjectIsNull(t *testing.T) {
	got, err := runNodeQuery(t, nodeSchema(t),
		fmt.Sprintf(`{ node(id: %q) { id } }`, globalID(t, "Author", "nope")))
	require.NoError(t, err)
	require.Equal(t, internal.ParseJSON(`{"node": null}`), got)
}

// TestNodeFetchErrorPropagates checks that a fetcher's error reaches the client
// rather than being swallowed into a null.
func TestNodeFetchErrorPropagates(t *testing.T) {
	_, err := runNodeQuery(t, nodeSchema(t),
		fmt.Sprintf(`{ node(id: %q) { id } }`, globalID(t, "Book", "explode")))
	require.Error(t, err)
}

// TestNodeSchemaShape checks the schema Relay sees.
func TestNodeSchemaShape(t *testing.T) {
	built := nodeSchema(t)

	sdl, err := graphql.PrintSchema(built)
	require.NoError(t, err)

	require.Contains(t, sdl, "interface Node {\n  id: ID!\n}", "printed schema:\n%s", sdl)
	require.Contains(t, sdl, "type Author implements Node {")
	require.Contains(t, sdl, "type Book implements Node {")
	// Documented arguments are printed in the multi-line form.
	require.Contains(t, sdl, "    id: ID!\n  ): Node\n")
	require.Contains(t, sdl, "    ids: [ID!]!\n  ): [Node]!\n")

	astSchema, err := graphql.ASTSchema(built)
	require.NoError(t, err)
	require.Contains(t, astSchema.Types, "Node")

	// The query validates against the schema, which is what relay-compiler
	// will be doing.
	v, err := graphql.NewValidator(built)
	require.NoError(t, err)
	_, err = v.Parse(`query Refetch($id: ID!) { node(id: $id) { id ... on Author { name } } }`, nil, "")
	require.NoError(t, err)
}

// TestNodeCustomCodec checks that the global id format is swappable.
func TestNodeCustomCodec(t *testing.T) {
	schema := schemabuilder.NewSchema()
	schema.SetGlobalIDCodec(slashCodec{})

	author := schema.Object("Author", Author{})
	author.Node(
		func(a *Author) string { return a.Key },
		func(id string) (*Author, error) { return authors[id], nil },
	)
	schema.Query().FieldFunc("firstAuthor", func() *Author { return authors["a1"] })
	built := schema.MustBuild()

	got, err := runNodeQuery(t, built, `{ firstAuthor { id } }`)
	require.NoError(t, err)
	require.Equal(t, internal.ParseJSON(`{"firstAuthor": {"id": "Author/a1"}}`), got)

	got, err = runNodeQuery(t, built, `{ node(id: "Author/a1") { ... on Author { name } } }`)
	require.NoError(t, err)
	require.Equal(t, internal.ParseJSON(`{"node": {"name": "Ursula"}}`), got)
}

// slashCodec is a deliberately un-obfuscated codec, to prove the format is the
// caller's choice.
type slashCodec struct{}

func (slashCodec) Encode(typeName, localID string) (string, error) {
	return typeName + "/" + localID, nil
}

func (slashCodec) Decode(globalID string) (string, string, error) {
	for i := 0; i < len(globalID); i++ {
		if globalID[i] == '/' {
			return globalID[:i], globalID[i+1:], nil
		}
	}
	return "", "", errors.New("malformed global id")
}
