package graphql_test

import (
	"context"
	"testing"

	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/graphql/schemabuilder"
	"github.com/hiett/lightning/internal"
	"github.com/hiett/lightning/internal/testgraphql"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

type Photo struct {
	Id      schemabuilder.ID
	Caption string
	Width   int32
}

type Article struct {
	Id    schemabuilder.ID
	Title string
	Body  string
}

// Content is an interface implemented by Photo and Article, declared the same
// way a union is: a marker struct whose embedded pointers name the members.
type Content struct {
	schemabuilder.Interface

	*Photo
	*Article
}

func interfaceSchema(t *testing.T) *graphql.Schema {
	t.Helper()

	schema := schemabuilder.NewSchema()

	schema.Interface("Content", Content{}).
		Fields("id", "summary").
		Describe("Something that can appear in a feed.")

	photo := schema.Object("Photo", Photo{})
	photo.FieldFunc("summary", func(p *Photo) string { return "photo: " + p.Caption })

	article := schema.Object("Article", Article{})
	article.FieldFunc("summary", func(a *Article) string { return "article: " + a.Title })

	query := schema.Query()
	query.FieldFunc("feed", func() []*Content {
		return []*Content{
			{Photo: &Photo{Id: schemabuilder.NewID("p1"), Caption: "a cat", Width: 640}},
			{Article: &Article{Id: schemabuilder.NewID("a1"), Title: "on cats", Body: "..."}},
		}
	})
	query.FieldFunc("nothing", func() *Content { return &Content{} })

	_ = schema.Mutation()
	return schema.MustBuild()
}

func runInterfaceQuery(t *testing.T, built *graphql.Schema, query string) interface{} {
	t.Helper()
	q := graphql.MustParse(query, nil)
	require.NoError(t, graphql.PrepareQuery(context.Background(), built.Query, q.SelectionSet))

	e := testgraphql.NewExecutorWrapper(t)
	result, err := e.Execute(context.Background(), built.Query, nil, q)
	require.NoError(t, err)
	return internal.AsJSON(result)
}

// TestInterfaceInlineFragment covers the acceptance criterion: an interface
// implemented by two object types, queried with an inline fragment, with
// __typename reporting the concrete type.
func TestInterfaceInlineFragment(t *testing.T) {
	got := runInterfaceQuery(t, interfaceSchema(t), `{
		feed {
			__typename
			id
			summary
			... on Photo { caption width }
			... on Article { title }
		}
	}`)

	require.Equal(t, internal.ParseJSON(`{"feed": [
		{"__typename": "Photo", "id": "p1", "summary": "photo: a cat", "caption": "a cat", "width": 640},
		{"__typename": "Article", "id": "a1", "summary": "article: on cats", "title": "on cats"}
	]}`), got)
}

// TestInterfaceNamedFragment is the same query written with named fragments.
func TestInterfaceNamedFragment(t *testing.T) {
	got := runInterfaceQuery(t, interfaceSchema(t), `
	{
		feed {
			__typename
			id
			...PhotoBits
			...ArticleBits
		}
	}
	fragment PhotoBits on Photo { caption width }
	fragment ArticleBits on Article { title }`)

	require.Equal(t, internal.ParseJSON(`{"feed": [
		{"__typename": "Photo", "id": "p1", "caption": "a cat", "width": 640},
		{"__typename": "Article", "id": "a1", "title": "on cats"}
	]}`), got)
}

// TestInterfaceFragmentOnTheInterface checks a fragment whose type condition is
// the interface itself, which applies to every member.
func TestInterfaceFragmentOnTheInterface(t *testing.T) {
	got := runInterfaceQuery(t, interfaceSchema(t), `
	{
		feed { ...Common }
	}
	fragment Common on Content { __typename summary }`)

	require.Equal(t, internal.ParseJSON(`{"feed": [
		{"__typename": "Photo", "summary": "photo: a cat"},
		{"__typename": "Article", "summary": "article: on cats"}
	]}`), got)
}

// TestInterfaceEmptyResolvesToNull checks that an interface value carrying no
// member is written as null rather than crashing.
func TestInterfaceEmptyResolvesToNull(t *testing.T) {
	got := runInterfaceQuery(t, interfaceSchema(t), `{ nothing { id } }`)
	require.Equal(t, internal.ParseJSON(`{"nothing": null}`), got)
}

// TestInterfaceIntrospection covers the other half of the acceptance criterion:
// the interface round-trips through introspection and SDL.
func TestInterfaceIntrospection(t *testing.T) {
	built := interfaceSchema(t)

	sdl, err := graphql.PrintSchema(built)
	require.NoError(t, err)

	require.Contains(t, sdl, "interface Content {")
	require.Contains(t, sdl, "type Article implements Content {")
	require.Contains(t, sdl, "type Photo implements Content {")
	require.Contains(t, sdl, `Something that can appear in a feed.`)

	astSchema, err := graphql.ASTSchema(built)
	require.NoError(t, err)

	content := astSchema.Types["Content"]
	require.NotNil(t, content)
	require.Equal(t, ast.Interface, content.Kind)

	// Only the declared fields, not everything the members happen to share.
	fieldNames := []string{}
	for _, f := range content.Fields {
		fieldNames = append(fieldNames, f.Name)
	}
	require.ElementsMatch(t, []string{"id", "summary"}, fieldNames)

	possible := []string{}
	for _, def := range astSchema.PossibleTypes["Content"] {
		possible = append(possible, def.Name)
	}
	require.ElementsMatch(t, []string{"Photo", "Article"}, possible)
}

// TestInterfaceDefaultFieldsAreTheSharedOnes checks the default field set when
// Fields is not called: everything all members agree on.
func TestInterfaceDefaultFieldsAreTheSharedOnes(t *testing.T) {
	schema := schemabuilder.NewSchema()
	schema.Object("Photo", Photo{}).FieldFunc("summary", func(p *Photo) string { return "" })
	schema.Object("Article", Article{}).FieldFunc("summary", func(a *Article) string { return "" })
	schema.Query().FieldFunc("feed", func() []*Content { return nil })
	built := schema.MustBuild()

	astSchema, err := graphql.ASTSchema(built)
	require.NoError(t, err)

	fieldNames := []string{}
	for _, f := range astSchema.Types["Content"].Fields {
		fieldNames = append(fieldNames, f.Name)
	}
	// id and summary are shared; caption/width/title/body are not.
	require.ElementsMatch(t, []string{"id", "summary"}, fieldNames)
}

// TestInterfaceDeclaredFieldMustBeShared checks that declaring a field no
// implementing type provides is a schema error rather than a runtime surprise.
func TestInterfaceDeclaredFieldMustBeShared(t *testing.T) {
	schema := schemabuilder.NewSchema()
	schema.Interface("Content", Content{}).Fields("caption")
	schema.Query().FieldFunc("feed", func() []*Content { return nil })

	_, err := schema.Build()
	require.Error(t, err)
	require.Contains(t, err.Error(), `field "caption" must exist on every implementing type`)
}

// TestInterfaceValidation checks that the validator, which now knows about
// interfaces, rejects a fragment that can never match.
func TestInterfaceValidation(t *testing.T) {
	v, err := graphql.NewValidator(interfaceSchema(t))
	require.NoError(t, err)

	_, err = v.Parse(`{ feed { ... on Photo { caption } } }`, nil, "")
	require.NoError(t, err)

	_, err = v.Parse(`{ feed { caption } }`, nil, "")
	require.Error(t, err, "caption is on Photo, not on the Content interface")
	require.Contains(t, err.Error(), `Cannot query field "caption" on type "Content"`)

	_, err = v.Parse(`{ feed { ... on Query { feed { id } } } }`, nil, "")
	require.Error(t, err, "a fragment on an unrelated type cannot be spread here")
}
