package relay_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/relay"
	"github.com/stretchr/testify/require"
)

// Row is a node whose searchable and orderable text can be resolved either one
// item at a time or for the whole list at once, which is what these benchmarks
// compare.
type Row struct {
	lightning.Meta `graphql:"Row"`

	Key   string `graphql:"-"`
	Text  string `graphql:"-"`
	Other string `graphql:"-"`
}

func (r *Row) NodeID() string { return r.Key }

func rows(n int) []*Row {
	texts := [5]string{"can", "man", "cannot", "soban", "socan"}
	out := make([]*Row, n)
	for i := range out {
		out[i] = &Row{Key: fmt.Sprint(i), Text: texts[i%5], Other: "a"}
	}
	return out
}

// rowSchema builds a connection over n rows whose two searchable fields resolve
// either singly or in a batch.
func rowSchema(b *testing.B, n int, batched bool) *graphql.Schema {
	b.Helper()

	all := rows(n)

	builder := lightning.New(relay.Plugin())
	row := lightning.Object[Row](builder)

	if batched {
		row.Batch("text", func(ctx context.Context, parents []*Row) ([]string, error) {
			out := make([]string, len(parents))
			for i, r := range parents {
				out[i] = r.Text
			}
			return out, nil
		}).Filterable().Sortable()
		row.Batch("other", func(ctx context.Context, parents []*Row) ([]string, error) {
			out := make([]string, len(parents))
			for i, r := range parents {
				out[i] = r.Other
			}
			return out, nil
		}).Filterable()
	} else {
		row.Attr("text", func(r *Row) string { return r.Text }).Filterable().Sortable()
		row.Attr("other", func(r *Row) string { return r.Other }).Filterable()
	}

	relay.Node(builder, func(ctx context.Context, id string) (*Row, error) { return nil, nil })
	relay.Connection(builder.Query(), "rows", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Row, error) {
		return all, nil
	})

	return builder.MustBuild()
}

func benchmarkRows(b *testing.B, n int, batched bool, args string) {
	built := rowSchema(b, n, batched)

	q := graphql.MustParse(`{
		rows(`+args+`) {
			totalCount
			edges { cursor node { text } }
		}
	}`, nil)
	require.NoError(b, graphql.PrepareQuery(context.Background(), built.Query, q.SelectionSet))

	executor := graphql.NewExecutor(graphql.NewImmediateGoroutineScheduler())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := executor.Execute(context.Background(), built.Query, nil, q)
		require.NoError(b, err)
	}
}

const filterArgs = `filterText: "can", first: 4`
const sortArgs = `sortBy: "text", first: 4`

func BenchmarkFilter10Items(b *testing.B)          { benchmarkRows(b, 10, false, filterArgs) }
func BenchmarkFilter100Items(b *testing.B)         { benchmarkRows(b, 100, false, filterArgs) }
func BenchmarkFilter1000Items(b *testing.B)        { benchmarkRows(b, 1000, false, filterArgs) }
func BenchmarkFilterBatched10Items(b *testing.B)   { benchmarkRows(b, 10, true, filterArgs) }
func BenchmarkFilterBatched100Items(b *testing.B)  { benchmarkRows(b, 100, true, filterArgs) }
func BenchmarkFilterBatched1000Items(b *testing.B) { benchmarkRows(b, 1000, true, filterArgs) }

func BenchmarkSort10Items(b *testing.B)          { benchmarkRows(b, 10, false, sortArgs) }
func BenchmarkSort100Items(b *testing.B)         { benchmarkRows(b, 100, false, sortArgs) }
func BenchmarkSort1000Items(b *testing.B)        { benchmarkRows(b, 1000, false, sortArgs) }
func BenchmarkSortBatched10Items(b *testing.B)   { benchmarkRows(b, 10, true, sortArgs) }
func BenchmarkSortBatched100Items(b *testing.B)  { benchmarkRows(b, 100, true, sortArgs) }
func BenchmarkSortBatched1000Items(b *testing.B) { benchmarkRows(b, 1000, true, sortArgs) }
