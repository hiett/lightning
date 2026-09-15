package graphql_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/internal"
	"github.com/hiett/lightning/internal/testgraphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Item is the list element the directive tests select fields of.
type Item struct {
	lightning.Meta `graphql:"Item"`

	Id     int64
	Number int64 `graphql:"-"`
}

func buildSchema() *graphql.Schema {
	b := lightning.New()

	item := lightning.Object[Item](b)
	item.Field("name", func(ctx context.Context, item *Item) (string, error) {
		return fmt.Sprint(item.Id), nil
	})
	item.Field("number", func(ctx context.Context, item *Item) (string, error) {
		return fmt.Sprint(item.Number), nil
	})

	b.Query().Field("items", func(ctx context.Context, _ *lightning.Root) ([]Item, error) {
		return []Item{
			{Id: 1, Number: 11},
			{Id: 2, Number: 12},
			{Id: 3, Number: 13},
			{Id: 4, Number: 14},
			{Id: 5, Number: 15},
		}, nil
	})

	return b.MustBuild()
}

func TestSkipDirectives(t *testing.T) {
	builtSchema := buildSchema()
	snap := testgraphql.NewSnapshotter(t, builtSchema)
	defer snap.Verify()

	snap.SnapshotQuery("Directive skip top level selection", `{
		items @skip(if: true){
			id
			name
		}
	}`)

	snap.SnapshotQuery("Directive don't skip top level selection", `{
		items @skip(if: false){
			id
			name
		}
	}`)

	snap.SnapshotQuery("Directive skip nested selection", `{
		items {
			id    @skip(if: true)
			name  @skip(if: false)
		}
	}`)

}

func TestIncludeDirectives(t *testing.T) {
	builtSchema := buildSchema()
	snap := testgraphql.NewSnapshotter(t, builtSchema)
	defer snap.Verify()

	snap.SnapshotQuery("Directive include top level selection", `{
		items @include(if: true){
			id
			name
		}
	}`)

	snap.SnapshotQuery("Directive don't include top level selection", `{
		items @include(if: false){
			id
			name
		}
	}`)

	snap.SnapshotQuery("Directive include nested selection", `{
		items {
			id    @include(if: true)
			name  @include(if: false)
		}
	}`)

}

func TestDirectivesWithFragments(t *testing.T) {
	builtSchema := buildSchema()
	snap := testgraphql.NewSnapshotter(t, builtSchema)
	defer snap.Verify()

	snap.SnapshotQuery("Directive with fragment on top level, skip true", `query x {
			...X @skip(if: true)
		}
		fragment X on Query {
			items {
				name
			}
		}`)

	snap.SnapshotQuery("Directive with fragment nested, skip true", `query x {
			items {
				id
				...X @skip(if: true)
			}
		}
		fragment X on Item {
			name
		}`)

	snap.SnapshotQuery("Directive on fragment selection, skip true", `query x {
		items {
			id
			...X
		}
	}
	fragment X on Item {
		name @skip(if: true)
	}`)

	snap.SnapshotQuery("Directive on both fragment(include true) and fragment selection(include false)", `query x {
		items {
			id
			...X @include(if: true)
		}
	}
	fragment X on Item {
		name @include(if: false)
		number
	}`)

	snap.SnapshotQuery("Directive on both fragment(include false) and fragment selection(include true)", `query x {
		items {
			id
			...X @include(if: false)
		}
	}
	fragment X on Item {
		name @include(if: true)
		number
	}`)

}

func TestDirectivesWithVariables(t *testing.T) {
	builtSchema := buildSchema()

	q := graphql.MustParse(`
		{
			...X @skip(if: $something)
		}
		fragment X on Query {
			items {
				name
			}
		}
	`, map[string]interface{}{"something": true})

	if err := graphql.PrepareQuery(context.Background(), builtSchema.Query, q.SelectionSet); err != nil {
		t.Error(err)
	}
	e := testgraphql.NewExecutorWrapper(t)

	val, err := e.Execute(context.Background(), builtSchema.Query, nil, q)
	assert.Nil(t, err)
	assert.Equal(t, map[string]interface{}{}, val)

	q = graphql.MustParse(`
		{
			...X @skip(if: $something)
		}
		fragment X on Query {
			items {
				name
			}
		}
	`, map[string]interface{}{"something": false})

	if err := graphql.PrepareQuery(context.Background(), builtSchema.Query, q.SelectionSet); err != nil {
		t.Error(err)
	}

	val, err = e.Execute(context.Background(), builtSchema.Query, nil, q)
	assert.Nil(t, err)
	assert.Equal(t, map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"name": "1"},
			map[string]interface{}{"name": "2"},
			map[string]interface{}{"name": "3"},
			map[string]interface{}{"name": "4"},
			map[string]interface{}{"name": "5"},
		},
	}, val)
}

func TestDirectivesWithErrors(t *testing.T) {
	builtSchema := buildSchema()
	e := testgraphql.NewExecutorWrapper(t)

	q := graphql.MustParse(`
		{
			...X @skip(notif: $something)
		}
		fragment X on Query {
			items {
				name
			}
		}
	`, map[string]interface{}{"something": false})
	_, err := e.Execute(context.Background(), builtSchema.Query, nil, q)
	assert.NotNil(t, err)
	assert.Equal(t, err.Error(), "required argument in directive not provided: if")

	q = graphql.MustParse(`
	{
		...X @skip(if: $something)
	}
	fragment X on Query {
		items {
			name
		}
	}
`, map[string]interface{}{"something": "wrong type"})
	_, err = e.Execute(context.Background(), builtSchema.Query, nil, q)
	assert.NotNil(t, err)
	assert.Equal(t, err.Error(), "expected type boolean, found type string in \"if\" argument")

}

type mergedInner struct {
	lightning.Meta `graphql:"mergedInner"`

	X string
	Y string
}

// TestDirectivesOnRepeatedSelections checks that @skip and @include are applied
// to each occurrence of a field before occurrences are merged.
//
// Merging two selections of one alias built a fresh Selection without copying
// the directives, and the executor evaluated directives on the merged result —
// so a skipped occurrence not only stopped being skipped, it contributed its
// sub-selections to a sibling occurrence. Whether that happened depended on
// whether the field was selected twice, which is to say on whether some
// unrelated fragment elsewhere in the document also asked for it.
func TestDirectivesOnRepeatedSelections(t *testing.T) {
	b := lightning.New()
	lightning.Object[mergedInner](b)
	b.Query().Field("inner", func(ctx context.Context, _ *lightning.Root) (mergedInner, error) {
		return mergedInner{X: "x", Y: "y"}, nil
	})
	built := b.MustBuild()

	run := func(t *testing.T, query string) interface{} {
		t.Helper()
		q := graphql.MustParse(query, nil)
		require.NoError(t, graphql.PrepareQuery(context.Background(), built.Query, q.SelectionSet))
		e := graphql.NewExecutor(graphql.NewImmediateGoroutineScheduler())
		result, err := e.Execute(context.Background(), built.Query, nil, q)
		require.NoError(t, err)
		return internal.AsJSON(result)
	}

	require.Equal(t, internal.ParseJSON(`{}`),
		run(t, `{ inner @skip(if: true) { x } inner @skip(if: true) { y } }`),
		"both occurrences skipped means the field is absent")

	require.Equal(t, internal.ParseJSON(`{"inner": {"x": "x"}}`),
		run(t, `{ inner { x } inner @skip(if: true) { y } }`),
		"a skipped occurrence must not contribute its sub-selections to the one that survived")

	require.Equal(t, internal.ParseJSON(`{}`),
		run(t, `{ a: inner @include(if: false) { x } a: inner @include(if: false) { y } }`))

	require.Equal(t, internal.ParseJSON(`{"inner": {"x": "x", "y": "y"}}`),
		run(t, `{ inner { x } inner { y } }`),
		"without directives the occurrences still merge")
}
