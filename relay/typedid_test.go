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

type doneArgs struct {
	ID   relay.ID[Task] `graphql:"id" description:"The task's global id."`
	Done bool
}

type anyArgs struct {
	ID relay.GID `graphql:"id"`
}

func typedIDSchema(t *testing.T) *graphql.Schema {
	t.Helper()

	b := lightning.New(relay.Plugin())
	lightning.Object[Task](b)
	lightning.Object[User](b)
	relay.Node(b, fetchTask)
	relay.Node(b, fetchUser)

	b.Query().FieldArgs("taskByID", func(ctx context.Context, _ *lightning.Root, args doneArgs) (*Task, error) {
		// args.ID.Local is the type-local identifier, already decoded and
		// already known to name a Task.
		return tasks[args.ID.Local], nil
	})
	b.Query().FieldArgs("anythingByID", func(ctx context.Context, _ *lightning.Root, args anyArgs) (string, error) {
		return args.ID.Type + ":" + args.ID.Local, nil
	})

	return b.MustBuild()
}

// TestTypedIDIsAnIDOnTheWire checks that a typed identifier is an ordinary ID
// as far as a client is concerned.
func TestTypedIDIsAnIDOnTheWire(t *testing.T) {
	sdl, err := graphql.PrintSchema(typedIDSchema(t))
	require.NoError(t, err)

	require.Contains(t, sdl, "The task's global id.")
	require.Contains(t, sdl, "id: ID!")
	require.NotContains(t, sdl, "relay.ID")
	require.NotContains(t, sdl, "input ID")
}

// TestTypedIDArrivesDecoded is the point: a resolver is handed the local
// identifier, with no decoding of its own to write.
func TestTypedIDArrivesDecoded(t *testing.T) {
	got := run(t, typedIDSchema(t), fmt.Sprintf(`{ taskByID(id: %q, done: true) { title } }`, globalID(t, "Task", "t1")))
	require.Equal(t, "Write the schema", got["taskByID"].(map[string]any)["title"])
}

// TestTypedIDRejectsAnotherTypesID is the check a hand-written helper usually
// forgets: a user's identifier where a task's was asked for.
//
// It is caught while the query is prepared, which is earlier than a resolver
// could catch it — nothing has run by then — and it is a client error, so the
// message survives sanitisation.
func TestTypedIDRejectsAnotherTypesID(t *testing.T) {
	err := prepareErr(t, typedIDSchema(t), fmt.Sprintf(`{ taskByID(id: %q, done: true) { title } }`, globalID(t, "User", "u1")))
	require.Error(t, err)
	require.Contains(t, graphql.SanitizeError(err),
		"expected the global id of a Task, but this one names a User")
}

// prepareErr parses and prepares a query, returning whatever went wrong. An
// argument is parsed here, before anything executes.
func prepareErr(t *testing.T, schema *graphql.Schema, query string) error {
	t.Helper()
	q, err := graphql.Parse(query, nil)
	require.NoError(t, err)
	return graphql.PrepareQuery(context.Background(), schema.Query, q.SelectionSet)
}

// TestUntypedIDStillAcceptsAnything checks that GID is unchanged: it is what
// node(id:) needs, and what a field that genuinely takes any identifier wants.
func TestUntypedIDStillAcceptsAnything(t *testing.T) {
	schema := typedIDSchema(t)

	got := run(t, schema, fmt.Sprintf(`{ anythingByID(id: %q) }`, globalID(t, "User", "u1")))
	require.Equal(t, "User:u1", got["anythingByID"])

	got = run(t, schema, fmt.Sprintf(`{ anythingByID(id: %q) }`, globalID(t, "Task", "t1")))
	require.Equal(t, "Task:t1", got["anythingByID"])
}

// TestTypedIDOfANonNodeIsReported catches the mistake the type system cannot:
// a typed identifier for something that was never registered as a node.
func TestTypedIDOfANonNodeIsReported(t *testing.T) {
	type Stranger struct {
		lightning.Meta `graphql:"Stranger"`
		Name           string
	}
	type strangerArgs struct {
		ID relay.ID[Stranger] `graphql:"id"`
	}

	b := lightning.New(relay.Plugin())
	lightning.Object[Task](b)
	lightning.Object[Stranger](b)
	relay.Node(b, fetchTask)
	b.Query().Field("firstTask", func(ctx context.Context, _ *lightning.Root) (*Task, error) {
		return tasks["t1"], nil
	})
	b.Query().FieldArgs("stranger", func(ctx context.Context, _ *lightning.Root, args strangerArgs) (*Stranger, error) {
		return nil, nil
	})

	schema := b.MustBuild()
	err := prepareErr(t, schema, fmt.Sprintf(`{ stranger(id: %q) { name } }`, globalID(t, "Stranger", "s1")))
	require.ErrorContains(t, err, "is not registered as a node")
}

// TestTypedIDCanBeReturned checks the other direction: a field resolving to a
// typed identifier encodes it as the global one.
func TestTypedIDCanBeReturned(t *testing.T) {
	b := lightning.New(relay.Plugin())
	task := lightning.Object[Task](b)
	relay.Node(b, fetchTask)

	task.Attr("selfID", func(t *Task) relay.ID[Task] { return relay.ID[Task]{Local: t.Key} })
	b.Query().Field("firstTask", func(ctx context.Context, _ *lightning.Root) (*Task, error) {
		return tasks["t1"], nil
	})

	got := run(t, b.MustBuild(), `{ firstTask { id selfID } }`)
	first := got["firstTask"].(map[string]any)
	require.Equal(t, first["id"], first["selfID"], "a typed id encodes as the global one")
}
