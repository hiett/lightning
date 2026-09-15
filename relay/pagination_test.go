package relay_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/relay"
	"github.com/stretchr/testify/require"
)

// fiveWidgets is the list every pagination case below is a window onto.
func fiveWidgets() []*Widget {
	return []*Widget{
		{Key: "1", Name: "one"}, {Key: "2", Name: "two"}, {Key: "3", Name: "three"},
		{Key: "4", Name: "four"}, {Key: "5", Name: "five"},
	}
}

// TestPaginationWindows walks every combination of the four pagination
// arguments, because between them they are the whole of what a client can ask.
func TestPaginationWindows(t *testing.T) {
	built := widgetSchema(t, fiveWidgets()...)

	for _, tc := range []struct {
		name        string
		args        string
		keys        []string
		hasNext     bool
		hasPrevious bool
	}{
		{
			name: "first and after", args: `first: 2, after: "` + widgetCursor("3") + `"`,
			keys: []string{"4", "5"}, hasNext: false, hasPrevious: true,
		},
		{
			// An unknown cursor names no item, so it narrows nothing and the
			// page is read from the start.
			name: "after a cursor that names nothing", args: `first: 2, after: "BAD"`,
			keys: []string{"1", "2"}, hasNext: true, hasPrevious: false,
		},
		{
			name: "first alone", args: `first: 2`,
			keys: []string{"1", "2"}, hasNext: true, hasPrevious: false,
		},
		{
			name: "last and before", args: `last: 2, before: "` + widgetCursor("3") + `"`,
			keys: []string{"1", "2"}, hasNext: true, hasPrevious: false,
		},
		{
			name: "before a cursor that names nothing", args: `last: 2, before: "BAD"`,
			keys: []string{"4", "5"}, hasNext: false, hasPrevious: true,
		},
		{
			name: "last alone", args: `last: 2`,
			keys: []string{"4", "5"}, hasNext: false, hasPrevious: true,
		},
		{
			name: "no arguments at all", args: ``,
			keys: []string{"1", "2", "3", "4", "5"}, hasNext: false, hasPrevious: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := `{ widgets`
			if tc.args != "" {
				query += `(` + tc.args + `)`
			}
			query += ` {
				totalCount
				edges { cursor node { name } }
				pageInfo { hasNextPage hasPreviousPage startCursor endCursor }
			} }`

			conn := run(t, built, query)["widgets"].(map[string]any)
			require.Equal(t, "5", conn["totalCount"], "totalCount is the whole list, not the page")

			edges := conn["edges"].([]any)
			cursors := make([]string, 0, len(edges))
			for _, e := range edges {
				cursors = append(cursors, e.(map[string]any)["cursor"].(string))
			}
			want := make([]string, 0, len(tc.keys))
			for _, key := range tc.keys {
				want = append(want, widgetCursor(key))
			}
			require.Equal(t, want, cursors)

			info := conn["pageInfo"].(map[string]any)
			require.Equal(t, tc.hasNext, info["hasNextPage"])
			require.Equal(t, tc.hasPrevious, info["hasPreviousPage"])
			require.Equal(t, want[0], info["startCursor"])
			require.Equal(t, want[len(want)-1], info["endCursor"])
		})
	}
}

// TestPageSizeIsCapped checks the plugin's own limit on how much a single query
// can ask for.
func TestPageSizeIsCapped(t *testing.T) {
	b := lightning.New(relay.Plugin(relay.WithMaxPageSize(3)))
	lightning.Object[Widget](b)
	relay.Node(b, fetchWidget)
	relay.Connection(b.Query(), "widgets", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Widget, error) {
		return fiveWidgets(), nil
	})

	conn := run(t, b.MustBuild(), `{ widgets(first: 100) { edges { node { name } } pageInfo { hasNextPage } } }`)["widgets"].(map[string]any)
	require.Len(t, conn["edges"].([]any), 3)
	require.Equal(t, true, conn["pageInfo"].(map[string]any)["hasNextPage"])
}

// TestConnectionOverANonNodeIsReported checks the one thing a connection needs
// of its element type, since the cursors are built from the node identifier.
func TestConnectionOverANonNodeIsReported(t *testing.T) {
	type Plain struct {
		lightning.Meta `graphql:"Plain"`
		Name           string
	}

	b := lightning.New(relay.Plugin())
	lightning.Object[Plain](b)
	relay.Connection(b.Query(), "plains", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Plain, error) {
		return []*Plain{{Name: "one"}}, nil
	})

	_, err := runErr(t, b.MustBuild(), `{ plains { totalCount } }`)
	require.ErrorContains(t, err, "needs it to be a node; register it with relay.Node")
}

// TestConnectionOverAnUndeclaredTypeIsReported catches the other mistake, at
// build time.
func TestConnectionOverAnUndeclaredTypeIsReported(t *testing.T) {
	type Undeclared struct {
		lightning.Meta `graphql:"Undeclared"`
		Name           string
	}

	b := lightning.New(relay.Plugin())
	lightning.Object[Widget](b)
	relay.Node(b, fetchWidget)
	relay.Connection(b.Query(), "things", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Undeclared, error) {
		return nil, nil
	})

	_, err := b.Build()
	require.ErrorContains(t, err, "Undeclared")
	require.ErrorContains(t, err, "lightning.Object")
}

// Measured is a node whose sortable fields cover every kind of value a sort has
// to be able to order.
type Measured struct {
	lightning.Meta `graphql:"Measured"`

	Key      string  `graphql:"-"`
	Name     string  `sortable:"true"`
	Count    int64   `sortable:"true"`
	Size     float64 `sortable:"true"`
	Done     bool    `sortable:"true"`
	Nickname *string `sortable:"true"`
}

func (m *Measured) NodeID() string { return m.Key }

// TestSortHandlesEveryOrderableKind checks each Go kind a sortable field can
// have, including a pointer that is sometimes nil.
func TestSortHandlesEveryOrderableKind(t *testing.T) {
	nick := func(s string) *string { return &s }
	all := []*Measured{
		{Key: "a", Name: "gamma", Count: 2, Size: 2.5, Done: true, Nickname: nick("z")},
		{Key: "b", Name: "alpha", Count: 3, Size: 0.5, Done: false, Nickname: nil},
		{Key: "c", Name: "beta", Count: 1, Size: 1.5, Done: false, Nickname: nick("a")},
	}

	b := lightning.New(relay.Plugin())
	lightning.Object[Measured](b)
	relay.Node(b, func(ctx context.Context, id string) (*Measured, error) { return nil, nil })
	relay.Connection(b.Query(), "measured", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Measured, error) {
		return all, nil
	})
	built := b.MustBuild()

	keys := func(query string) []string {
		t.Helper()
		conn := run(t, built, query)["measured"].(map[string]any)
		var out []string
		for _, e := range conn["edges"].([]any) {
			cursor := e.(map[string]any)["cursor"].(string)
			decoded, err := base64.StdEncoding.DecodeString(cursor)
			require.NoError(t, err)
			out = append(out, string(decoded))
		}
		return out
	}

	for _, tc := range []struct {
		field string
		asc   []string
		desc  []string
	}{
		{field: "name", asc: []string{"b", "c", "a"}, desc: []string{"a", "c", "b"}},
		{field: "count", asc: []string{"c", "a", "b"}, desc: []string{"b", "a", "c"}},
		{field: "size", asc: []string{"b", "c", "a"}, desc: []string{"a", "c", "b"}},
		// Descending is not the ascending order reversed: b and c are both
		// false, and a stable sort leaves equal items in the order they came.
		{field: "done", asc: []string{"b", "c", "a"}, desc: []string{"a", "b", "c"}},
		// A nil pointer orders before anything present.
		{field: "nickname", asc: []string{"b", "c", "a"}, desc: []string{"a", "c", "b"}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			require.Equal(t, tc.asc, keys(fmt.Sprintf(`{ measured(sortBy: %q) { edges { cursor } } }`, tc.field)))
			require.Equal(t, tc.desc, keys(fmt.Sprintf(`{ measured(sortBy: %q, sortOrder: desc) { edges { cursor } } }`, tc.field)))
		})
	}
}

// TestSortIsStable checks that items the sort cannot tell apart keep the order
// the resolver gave them, which is what lets a cursor into a sorted list mean
// the same thing twice.
func TestSortIsStable(t *testing.T) {
	all := []*Measured{
		{Key: "a", Count: 1}, {Key: "b", Count: 1}, {Key: "c", Count: 1}, {Key: "d", Count: 0},
	}

	b := lightning.New(relay.Plugin())
	lightning.Object[Measured](b)
	relay.Node(b, func(ctx context.Context, id string) (*Measured, error) { return nil, nil })
	relay.Connection(b.Query(), "measured", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Measured, error) {
		return all, nil
	})

	conn := run(t, b.MustBuild(), `{ measured(sortBy: "count") { edges { cursor } } }`)["measured"].(map[string]any)
	var keys []string
	for _, e := range conn["edges"].([]any) {
		decoded, err := base64.StdEncoding.DecodeString(e.(map[string]any)["cursor"].(string))
		require.NoError(t, err)
		keys = append(keys, string(decoded))
	}
	require.Equal(t, []string{"d", "a", "b", "c"}, keys)
}

// TestConnectionArgsReachTheResolver checks that a connection's own arguments
// sit alongside the pagination ones rather than replacing them.
func TestConnectionArgsReachTheResolver(t *testing.T) {
	type Filter struct {
		Prefix string `description:"Only names starting with this."`
	}

	b := lightning.New(relay.Plugin())
	lightning.Object[Widget](b)
	relay.Node(b, fetchWidget)
	relay.ConnectionArgs(b.Query(), "widgets", func(ctx context.Context, _ *lightning.Root, p relay.Page, args Filter) ([]*Widget, error) {
		var kept []*Widget
		for _, w := range fiveWidgets() {
			if len(args.Prefix) == 0 || (len(w.Name) >= len(args.Prefix) && w.Name[:len(args.Prefix)] == args.Prefix) {
				kept = append(kept, w)
			}
		}
		return kept, nil
	})

	built := b.MustBuild()

	sdl, err := graphql.PrintSchema(built)
	require.NoError(t, err)
	require.Contains(t, sdl, "Only names starting with this.")

	conn := run(t, built, `{ widgets(prefix: "t", first: 1) { totalCount edges { node { name } } } }`)["widgets"].(map[string]any)
	require.Equal(t, "2", conn["totalCount"], "two widgets start with t")
	require.Equal(t, "two", conn["edges"].([]any)[0].(map[string]any)["node"].(map[string]any)["name"])
}
