package graphql_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/internal"
	"github.com/hiett/lightning/internal/testgraphql"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

// Content is a GraphQL interface, which is to say a Go interface. Photo and
// Article are its members because a witness function says so, not because they
// happen to satisfy it.
type Content interface {
	ContentID() lightning.ID
	Summary() string
}

type Photo struct {
	lightning.Meta `graphql:"Photo"`

	Id      lightning.ID
	Caption string
	Width   int32
}

func (p *Photo) ContentID() lightning.ID { return p.Id }
func (p *Photo) Summary() string         { return "photo: " + p.Caption }

type Article struct {
	lightning.Meta `graphql:"Article"`

	Id    lightning.ID
	Title string
	Body  string
}

func (a *Article) ContentID() lightning.ID { return a.Id }
func (a *Article) Summary() string         { return "article: " + a.Title }

func interfaceSchema(t *testing.T) *graphql.Schema {
	t.Helper()

	b := lightning.New()

	content := lightning.Interface[Content](b).Describe("Something that can appear in a feed.")
	content.Field("id", func(_ context.Context, c Content) (lightning.ID, error) {
		return c.ContentID(), nil
	})
	content.Field("summary", func(_ context.Context, c Content) (string, error) {
		return c.Summary(), nil
	})

	photo := lightning.Object[Photo](b)
	article := lightning.Object[Article](b)
	lightning.Implements(content, photo, func(p *Photo) Content { return p })
	lightning.Implements(content, article, func(a *Article) Content { return a })

	query := b.Query()
	query.Field("feed", func(ctx context.Context, _ *lightning.Root) ([]Content, error) {
		return []Content{
			&Photo{Id: lightning.NewID("p1"), Caption: "a cat", Width: 640},
			&Article{Id: lightning.NewID("a1"), Title: "on cats", Body: "..."},
		}, nil
	})
	query.Field("nothing", func(ctx context.Context, _ *lightning.Root) (Content, error) {
		return nil, nil
	})

	return b.MustBuild()
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

// TestInterfaceEmptyResolvesToNull checks that a nil interface value is written
// as null rather than crashing.
//
// In the old library this was a marker struct with none of its member pointers
// set, an invalid state that only a convention kept out of the schema; here it
// is a nil interface, which is the only way to say nothing.
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

// Marker is an interface with no methods and no declared fields, which is what
// the members' shared fields are for.
type Marker interface{ isMarker() }

func (p *Photo) isMarker()   {}
func (a *Article) isMarker() {}

// TestInterfaceDefaultFieldsAreTheSharedOnes checks the default field set when
// no field is declared: everything all members agree on.
func TestInterfaceDefaultFieldsAreTheSharedOnes(t *testing.T) {
	b := lightning.New()

	marker := lightning.Interface[Marker](b)
	photo := lightning.Object[Photo](b)
	article := lightning.Object[Article](b)
	lightning.Implements(marker, photo, func(p *Photo) Marker { return p })
	lightning.Implements(marker, article, func(a *Article) Marker { return a })

	b.Query().Field("feed", func(ctx context.Context, _ *lightning.Root) ([]Marker, error) {
		return nil, nil
	})

	astSchema, err := graphql.ASTSchema(b.MustBuild())
	require.NoError(t, err)

	fieldNames := []string{}
	for _, f := range astSchema.Types["Marker"].Fields {
		fieldNames = append(fieldNames, f.Name)
	}
	// id is shared; caption/width/title/body are not.
	require.ElementsMatch(t, []string{"id"}, fieldNames)
}

// TestInterfaceMembersInheritDeclaredFields checks what happens to a member
// that does not provide an interface field of its own.
//
// The old library refused the schema: every implementing type had to supply the
// field itself. Here the interface's own resolver takes the interface value, so
// every member already satisfies it, and the member inherits it.
func TestInterfaceMembersInheritDeclaredFields(t *testing.T) {
	built := interfaceSchema(t)

	sdl, err := graphql.PrintSchema(built)
	require.NoError(t, err)
	require.Contains(t, sdl, "summary: String!")

	got := runInterfaceQuery(t, built, `{ feed { ... on Photo { summary } } }`)
	require.Equal(t, internal.ParseJSON(`{"feed": [{"summary": "photo: a cat"}, {}]}`), got)
}

// TestInterfaceFieldTypeMismatchIsReported checks the contract that is left: a
// member may provide an interface field itself, but not with a different type.
func TestInterfaceFieldTypeMismatchIsReported(t *testing.T) {
	b := lightning.New()

	content := lightning.Interface[Content](b)
	content.Field("summary", func(_ context.Context, c Content) (string, error) {
		return c.Summary(), nil
	})

	photo := lightning.Object[Photo](b)
	photo.Attr("summary", func(p *Photo) int32 { return p.Width })
	article := lightning.Object[Article](b)

	lightning.Implements(content, photo, func(p *Photo) Content { return p })
	lightning.Implements(content, article, func(a *Article) Content { return a })

	b.Query().Field("feed", func(ctx context.Context, _ *lightning.Root) ([]Content, error) {
		return nil, nil
	})

	_, err := b.Build()
	require.ErrorContains(t, err, "Photo.summary is Int!, but the interface Content declares it as String!")
}

// TestInterfaceValidation checks that the validator, which knows about
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

// Shouty is an interface whose one field takes arguments. ShoutA and ShoutB
// implement it with different Go argument structs, which is what the schema
// cannot see and the executor has to cope with.
type Shouty interface{ shout() }

type ShoutA struct {
	lightning.Meta `graphql:"ShoutA"`
	Name           string
}

type ShoutB struct {
	lightning.Meta `graphql:"ShoutB"`
	Name           string
}

func (a *ShoutA) shout() {}
func (b *ShoutB) shout() {}

type ShoutArgsA struct{ Times int32 }
type ShoutArgsB struct{ Times int32 }

// TestInterfaceFieldArgumentsArePerImplementation checks that a field selected
// directly on an interface hands each implementation the arguments *it* parsed.
//
// Implementations agree about a field's GraphQL signature — the schema enforces
// that — but nothing makes them share a Go argument struct. Parsing once and
// reusing the result fed one implementation's struct to another's resolver,
// which is a type error.
func TestInterfaceFieldArgumentsArePerImplementation(t *testing.T) {
	b := lightning.New()

	shouty := lightning.Interface[Shouty](b)
	shouty.FieldArgs("shout", func(_ context.Context, s Shouty, args ShoutArgsA) (string, error) {
		return "", nil
	})

	a := lightning.Object[ShoutA](b)
	a.FieldArgs("shout", func(_ context.Context, v *ShoutA, args ShoutArgsA) (string, error) {
		return fmt.Sprintf("A%d", args.Times), nil
	})

	other := lightning.Object[ShoutB](b)
	other.FieldArgs("shout", func(_ context.Context, v *ShoutB, args ShoutArgsB) (string, error) {
		return fmt.Sprintf("B%d", args.Times), nil
	})

	lightning.Implements(shouty, a, func(v *ShoutA) Shouty { return v })
	lightning.Implements(shouty, other, func(v *ShoutB) Shouty { return v })

	b.Query().Field("both", func(ctx context.Context, _ *lightning.Root) ([]Shouty, error) {
		return []Shouty{&ShoutA{}, &ShoutB{}}, nil
	})

	got := runInterfaceQuery(t, b.MustBuild(), `{ both { shout(times: 3) } }`)
	require.Equal(t, internal.ParseJSON(`{"both": [{"shout": "A3"}, {"shout": "B3"}]}`), got)
}
