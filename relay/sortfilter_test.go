package relay_test

import (
	"context"
	"testing"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/relay"
	"github.com/stretchr/testify/require"
)

// Note is a node whose fields say for themselves what a list of them can be
// ordered by and searched for.
type Note struct {
	lightning.Meta `description:"Something written down."`

	Key   string `graphql:"-"`
	Title string `description:"What it is called." sortable:"true" filterable:"true"`
	Body  string `description:"What it says." filterable:"true"`
	Rank  int32  `description:"Where it sits." sortable:"true"`
}

func (n *Note) NodeID() string { return n.Key }

var notes = []*Note{
	{Key: "n1", Title: "Bravo", Body: "about ships", Rank: 2},
	{Key: "n2", Title: "alpha", Body: "about trains", Rank: 3},
	{Key: "n3", Title: "Charlie", Body: "about ships and trains", Rank: 1},
}

func noteSchema(t *testing.T, declare func(q *lightning.Type[lightning.Root])) *graphql.Schema {
	t.Helper()

	b := lightning.New(relay.Plugin())
	lightning.Object[Note](b)
	relay.Node(b, func(ctx context.Context, id string) (*Note, error) {
		for _, n := range notes {
			if n.Key == id {
				return n, nil
			}
		}
		return nil, nil
	})
	declare(b.Query())
	return b.MustBuild()
}

func plainNotes(t *testing.T) *graphql.Schema {
	return noteSchema(t, func(q *lightning.Type[lightning.Root]) {
		relay.Connection(q, "notes", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Note, error) {
			return notes, nil
		})
	})
}

func titles(t *testing.T, conn map[string]any) []string {
	t.Helper()
	var out []string
	for _, e := range conn["edges"].([]any) {
		out = append(out, e.(map[string]any)["node"].(map[string]any)["title"].(string))
	}
	return out
}

// TestSortAndFilterArgumentsComeFromTheType is the rule: the arguments exist
// because a field said it could be sorted or searched, and not otherwise.
func TestSortAndFilterArgumentsComeFromTheType(t *testing.T) {
	sdl, err := graphql.PrintSchema(plainNotes(t))
	require.NoError(t, err)

	require.Contains(t, sdl, "sortBy: String")
	require.Contains(t, sdl, "sortOrder: SortOrder")
	require.Contains(t, sdl, "filterText: String")
	require.Contains(t, sdl, "filterTextFields: [String!]")
	require.Contains(t, sdl, "Order by one of: rank, title.")
	require.Contains(t, sdl, "searching body, title.")
	require.Contains(t, sdl, "enum SortOrder {")
}

// TestNoSortableFieldsNoArguments is the other half of that rule, and the fix
// for the old library, where every connection advertised arguments that did
// nothing.
func TestNoSortableFieldsNoArguments(t *testing.T) {
	b := lightning.New(relay.Plugin())
	lightning.Object[Task](b)
	relay.Node(b, fetchTask)
	relay.Connection(b.Query(), "tasks", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Task, error) {
		return nil, nil
	})

	sdl, err := graphql.PrintSchema(b.MustBuild())
	require.NoError(t, err)

	require.NotContains(t, sdl, "sortBy")
	require.NotContains(t, sdl, "sortOrder")
	require.NotContains(t, sdl, "filterText")
	require.NotContains(t, sdl, "enum SortOrder")
}

// TestSortOrdersTheList covers both directions and both kinds of value.
func TestSortOrdersTheList(t *testing.T) {
	schema := plainNotes(t)

	got := run(t, schema, `{ notes(sortBy: "title") { edges { node { title } } } }`)
	require.Equal(t, []string{"Bravo", "Charlie", "alpha"}, titles(t, got["notes"].(map[string]any)))

	got = run(t, schema, `{ notes(sortBy: "rank") { edges { node { title } } } }`)
	require.Equal(t, []string{"Charlie", "Bravo", "alpha"}, titles(t, got["notes"].(map[string]any)))

	got = run(t, schema, `{ notes(sortBy: "rank", sortOrder: desc) { edges { node { title } } } }`)
	require.Equal(t, []string{"alpha", "Bravo", "Charlie"}, titles(t, got["notes"].(map[string]any)))
}

// TestFilterSearchesEveryFilterableField checks the default: any searchable
// field may match.
func TestFilterSearchesEveryFilterableField(t *testing.T) {
	schema := plainNotes(t)

	got := run(t, schema, `{ notes(filterText: "ships") { totalCount edges { node { title } } } }`)
	conn := got["notes"].(map[string]any)
	require.Equal(t, "2", conn["totalCount"], "totalCount counts the filtered list")
	require.ElementsMatch(t, []string{"Bravo", "Charlie"}, titles(t, conn))

	// Matching is case-insensitive, as it was before.
	got = run(t, schema, `{ notes(filterText: "BRAVO") { edges { node { title } } } }`)
	require.Equal(t, []string{"Bravo"}, titles(t, got["notes"].(map[string]any)))
}

// TestFilterTextFieldsNarrowsTheSearch checks that a client can say where to
// look.
func TestFilterTextFieldsNarrowsTheSearch(t *testing.T) {
	schema := plainNotes(t)

	got := run(t, schema, `{ notes(filterText: "trains", filterTextFields: ["title"]) { edges { node { title } } } }`)
	require.Empty(t, titles(t, got["notes"].(map[string]any)))

	got = run(t, schema, `{ notes(filterText: "trains", filterTextFields: ["body"]) { edges { node { title } } } }`)
	require.ElementsMatch(t, []string{"alpha", "Charlie"}, titles(t, got["notes"].(map[string]any)))
}

// TestFilterAndSortCompose checks the order they are applied in: narrow first,
// then order what is left.
func TestFilterAndSortCompose(t *testing.T) {
	got := run(t, plainNotes(t), `{
		notes(filterText: "about", sortBy: "title", sortOrder: desc) { edges { node { title } } }
	}`)
	require.Equal(t, []string{"alpha", "Charlie", "Bravo"}, titles(t, got["notes"].(map[string]any)))
}

// TestUnknownSortFieldIsAClientError checks that a mistake says what is
// allowed.
func TestUnknownSortFieldIsAClientError(t *testing.T) {
	_, err := runErr(t, plainNotes(t), `{ notes(sortBy: "body") { totalCount } }`)
	require.ErrorContains(t, err, `Note cannot be sorted by "body"; it must be one of "rank", "title"`)

	_, err = runErr(t, plainNotes(t), `{ notes(filterText: "x", filterTextFields: ["rank"]) { totalCount } }`)
	require.ErrorContains(t, err, `Note cannot be filtered by "rank"; it must be one of "body", "title"`)
}

// TestPagesListsThePageStarts covers the page-number extension.
func TestPagesListsThePageStarts(t *testing.T) {
	got := run(t, plainNotes(t), `{ notes(first: 1) { pageInfo { pages } } }`)
	pages := got["notes"].(map[string]any)["pageInfo"].(map[string]any)["pages"].([]any)
	require.Len(t, pages, 3)
	require.Equal(t, "", pages[0], "the first page starts at the beginning of the list")

	// Without a page size there is one page, and it is the whole list.
	got = run(t, plainNotes(t), `{ notes { pageInfo { pages } } }`)
	pages = got["notes"].(map[string]any)["pageInfo"].(map[string]any)["pages"].([]any)
	require.Equal(t, []any{""}, pages)
}

// TestManualConnectionTrustsItsResolver checks that a resolver which pages the
// list itself is believed, rather than having its page paged again.
func TestManualConnectionTrustsItsResolver(t *testing.T) {
	var asked relay.Page

	schema := noteSchema(t, func(q *lightning.Type[lightning.Root]) {
		relay.ManualConnection(q, "notes", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Note, relay.PageResult, error) {
			asked = p
			// The resolver returns one item and says there are a hundred.
			return notes[1:2], relay.PageResult{TotalCount: 100, HasNextPage: true, HasPreviousPage: true}, nil
		})
	})

	got := run(t, schema, `{
		notes(first: 2, filterText: "ships", sortBy: "rank", sortOrder: desc) {
			totalCount
			edges { cursor node { title } }
			pageInfo { hasNextPage hasPreviousPage }
		}
	}`)

	conn := got["notes"].(map[string]any)
	require.Equal(t, "100", conn["totalCount"])
	require.Equal(t, []string{"alpha"}, titles(t, conn), "the plugin did not filter the page again")
	require.NotEmpty(t, conn["edges"].([]any)[0].(map[string]any)["cursor"], "the plugin still supplies cursors")

	info := conn["pageInfo"].(map[string]any)
	require.Equal(t, true, info["hasNextPage"])
	require.Equal(t, true, info["hasPreviousPage"])

	// Everything the client asked for reached the resolver.
	require.Equal(t, int32(2), *asked.First)
	require.Equal(t, "ships", *asked.FilterText)
	require.Equal(t, "rank", *asked.SortBy)
	require.Equal(t, relay.Descending, asked.SortOrder)
}

// TestSortableFieldMayBeDeclaredRatherThanTagged checks the chain form, for a
// field that is computed rather than stored.
func TestSortableFieldMayBeDeclaredRatherThanTagged(t *testing.T) {
	b := lightning.New(relay.Plugin())
	note := lightning.Object[Note](b)
	note.Attr("shout", func(n *Note) string { return n.Title + "!" }).Sortable().Filterable()
	relay.Node(b, func(ctx context.Context, id string) (*Note, error) { return nil, nil })
	relay.Connection(b.Query(), "notes", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Note, error) {
		return notes, nil
	})

	schema := b.MustBuild()

	sdl, err := graphql.PrintSchema(schema)
	require.NoError(t, err)
	require.Contains(t, sdl, "Order by one of: rank, shout, title.")

	got := run(t, schema, `{ notes(sortBy: "shout", sortOrder: desc) { edges { node { title } } } }`)
	require.Equal(t, []string{"alpha", "Charlie", "Bravo"}, titles(t, got["notes"].(map[string]any)))
}

// TestSortableFieldWithArgumentsIsReported catches a field that cannot be asked
// for once per item because it needs a question first.
func TestSortableFieldWithArgumentsIsReported(t *testing.T) {
	type PadArgs struct {
		Width int32
	}

	b := lightning.New(relay.Plugin())
	note := lightning.Object[Note](b)
	note.FieldArgs("padded", func(ctx context.Context, n *Note, args PadArgs) (string, error) {
		return n.Title, nil
	}).Sortable()
	relay.Node(b, func(ctx context.Context, id string) (*Note, error) { return nil, nil })
	relay.Connection(b.Query(), "notes", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Note, error) {
		return nil, nil
	})

	_, err := b.Build()
	require.ErrorContains(t, err, "Note.padded takes arguments, so a connection cannot sort or filter by it")
}

// TestFilterableFieldMustBeText catches a field that has no text to search.
func TestFilterableFieldMustBeText(t *testing.T) {
	b := lightning.New(relay.Plugin())
	note := lightning.Object[Note](b)
	note.Attr("size", func(n *Note) int32 { return n.Rank }).Filterable()
	relay.Node(b, func(ctx context.Context, id string) (*Note, error) { return nil, nil })
	relay.Connection(b.Query(), "notes", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Note, error) {
		return notes, nil
	})

	_, err := runErr(t, b.MustBuild(), `{ notes(filterText: "x") { totalCount } }`)
	require.ErrorContains(t, err, "only a string field can be filtered by text")
}

// TestFilterTextFieldsEmptyMatchesNothing pins the literal reading of an empty
// list: it asks for a search of no fields, and no field matches.
func TestFilterTextFieldsEmptyMatchesNothing(t *testing.T) {
	got := run(t, plainNotes(t), `{ notes(filterText: "about", filterTextFields: []) { totalCount edges { node { title } } } }`)
	conn := got["notes"].(map[string]any)
	require.Equal(t, "0", conn["totalCount"])
	require.Empty(t, conn["edges"])
}

// TestFilterTextEmptyMatchesEverything is the other end: no search is not a
// search that fails.
func TestFilterTextEmptyMatchesEverything(t *testing.T) {
	got := run(t, plainNotes(t), `{ notes(filterText: "") { totalCount } }`)
	require.Equal(t, "3", got["notes"].(map[string]any)["totalCount"])
}
