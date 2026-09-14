package graphql_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/graphql/schemabuilder"
	"github.com/hiett/lightning/internal"
	"github.com/hiett/lightning/internal/testgraphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildSchema() *graphql.Schema {
	schema := schemabuilder.NewSchema()
	type Inner struct {
	}

	query := schema.Query()
	query.FieldFunc("inner", func() Inner {
		return Inner{}
	})

	item := schema.Object("Item", Item{})
	item.Key("id")
	item.FieldFunc("name", func(ctx context.Context, item Item) (string, error) {
		return fmt.Sprint(item.Id), nil
	})
	item.FieldFunc("number", func(ctx context.Context, item Item) (string, error) {
		return fmt.Sprint(item.Number), nil
	})
	query.FieldFunc("items", func(ctx context.Context) ([]Item, error) {
		retList := make([]Item, 5)
		retList[0] = Item{Id: 1, Number: 11}
		retList[1] = Item{Id: 2, Number: 12}
		retList[2] = Item{Id: 3, Number: 13}
		retList[3] = Item{Id: 4, Number: 14}
		retList[4] = Item{Id: 5, Number: 15}
		return retList, nil
	})
	return schema.MustBuild()
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
			map[string]interface{}{
				"__key": "1",
				"name":  "1",
			},
			map[string]interface{}{
				"__key": "2",
				"name":  "2",
			},
			map[string]interface{}{
				"__key": "3",
				"name":  "3",
			},
			map[string]interface{}{
				"__key": "4",
				"name":  "4",
			},
			map[string]interface{}{
				"__key": "5",
				"name":  "5",
			},
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
	schema := schemabuilder.NewSchema()
	schema.Query().FieldFunc("inner", func() mergedInner { return mergedInner{X: "x", Y: "y"} })
	built := schema.MustBuild()

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
