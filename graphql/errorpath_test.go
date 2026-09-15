package graphql_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/internal/testgraphql"
	"github.com/stretchr/testify/require"
)

type pathRow struct {
	lightning.Meta `graphql:"PathRow"`

	Name string
}

// TestErrorPath checks the acceptance criterion for Phase 4b: an error raised
// deep inside a query reports the response path that leads to it, with list
// indices as numbers.
func TestErrorPath(t *testing.T) {
	b := lightning.New()

	row := lightning.Object[pathRow](b)
	row.Field("shout", func(ctx context.Context, r *pathRow) (string, error) {
		if r.Name == "boom" {
			return "", errors.New("it broke")
		}
		return r.Name, nil
	})

	b.Query().Field("rows", func(ctx context.Context, _ *lightning.Root) ([]*pathRow, error) {
		return []*pathRow{{Name: "ok"}, {Name: "boom"}, {Name: "ok"}}, nil
	})

	built := b.MustBuild()

	q := graphql.MustParse(`query Named { rows { loud: shout } }`, nil)
	require.NoError(t, graphql.PrepareQuery(context.Background(), built.Query, q.SelectionSet))

	e := testgraphql.NewExecutorWrapper(t)
	_, err := e.Execute(context.Background(), built.Query, nil, q)
	require.Error(t, err)

	errs := graphql.AsResponseErrors(err)
	require.Len(t, errs, 1)
	require.Equal(t, []interface{}{"rows", 1, "loud"}, errs[0].Path,
		"path must be outermost-first, use the field's alias, and carry the list index as a number")

	// And it must serialise that way too.
	encoded, jsonErr := json.Marshal(graphql.NewResponse(nil, err))
	require.NoError(t, jsonErr)
	require.JSONEq(t,
		`{"data":null,"errors":[{"message":"Internal server error","path":["rows",1,"loud"]}]}`,
		string(encoded))
}

// TestErrorPathIsClientSafe checks that a client-safe error keeps its message
// while an ordinary Go error is replaced with a generic one.
func TestErrorPathIsClientSafe(t *testing.T) {
	b := lightning.New()
	b.Query().Field("safe", func(ctx context.Context, _ *lightning.Root) (string, error) {
		return "", graphql.NewClientError("you asked for the wrong thing")
	})
	built := b.MustBuild()

	q := graphql.MustParse(`{ safe }`, nil)
	require.NoError(t, graphql.PrepareQuery(context.Background(), built.Query, q.SelectionSet))

	e := graphql.NewExecutor(graphql.NewImmediateGoroutineScheduler())
	_, err := e.Execute(context.Background(), built.Query, nil, q)
	require.Error(t, err)

	errs := graphql.AsResponseErrors(err)
	require.Len(t, errs, 1)
	require.Equal(t, "you asked for the wrong thing", errs[0].Message)
}
