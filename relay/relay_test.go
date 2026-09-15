package relay_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/relay"
	"github.com/stretchr/testify/require"
)

// Task knows its own identifier, which is all relay.Node needs beyond a
// fetcher.
type Task struct {
	lightning.Meta `graphql:"Task" description:"A unit of work."`

	Key   string `graphql:"-"`
	Title string `description:"What needs doing."`
	Done  bool   `description:"Whether it has been done."`
}

func (t *Task) NodeID() string { return t.Key }

type User struct {
	lightning.Meta `description:"A person."`

	Key  string `graphql:"-"`
	Name string `description:"Their name."`
}

func (u *User) NodeID() string { return u.Key }

var tasks = map[string]*Task{
	"t1": {Key: "t1", Title: "Write the schema", Done: true},
	"t2": {Key: "t2", Title: "Make it live"},
}

var users = map[string]*User{"u1": {Key: "u1", Name: "Ada"}}

func fetchTask(ctx context.Context, id string) (*Task, error) { return tasks[id], nil }
func fetchUser(ctx context.Context, id string) (*User, error) { return users[id], nil }

func nodeSchema(t *testing.T) *graphql.Schema {
	t.Helper()

	b := lightning.New(relay.Plugin())

	lightning.Object[Task](b)
	lightning.Object[User](b)

	// Two lines make a type a node: one to declare it, one to register it.
	relay.Node(b, fetchTask)
	relay.Node(b, fetchUser)

	b.Query().Field("firstTask", func(ctx context.Context, _ *lightning.Root) (*Task, error) {
		return tasks["t1"], nil
	})
	b.Query().Field("viewer", func(ctx context.Context, _ *lightning.Root) (*User, error) {
		return users["u1"], nil
	})

	return b.MustBuild()
}

func run(t *testing.T, schema *graphql.Schema, query string) map[string]any {
	t.Helper()

	q, err := graphql.Parse(query, nil)
	require.NoError(t, err)
	require.NoError(t, graphql.PrepareQuery(context.Background(), schema.Query, q.SelectionSet))

	e := graphql.NewExecutor(graphql.NewImmediateGoroutineScheduler())
	result, err := e.Execute(context.Background(), schema.Query, nil, q)
	require.NoError(t, err)

	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(encoded, &out))
	return out
}

func globalID(t *testing.T, typeName, localID string) string {
	t.Helper()
	encoded, err := relay.Base64Codec{}.Encode(typeName, localID)
	require.NoError(t, err)
	return encoded
}

// TestNodeAddsTheIDField covers the shape Relay needs.
func TestNodeAddsTheIDField(t *testing.T) {
	schema := nodeSchema(t)

	sdl, err := graphql.PrintSchema(schema)
	require.NoError(t, err)

	require.Contains(t, sdl, "interface Node {")
	require.Contains(t, sdl, "type Task implements Node {")
	require.Contains(t, sdl, "type User implements Node {")
	require.Contains(t, sdl, "id: ID!")

	got := run(t, schema, `{ firstTask { id title } }`)
	task := got["firstTask"].(map[string]any)

	decoded, err := base64.StdEncoding.DecodeString(task["id"].(string))
	require.NoError(t, err)
	require.Equal(t, "Task:t1", string(decoded))
}

// TestNodeRoundTrip is the acceptance criterion: an id from a query fetches the
// same object back.
func TestNodeRoundTrip(t *testing.T) {
	schema := nodeSchema(t)

	got := run(t, schema, `{ firstTask { id title } }`)
	id := got["firstTask"].(map[string]any)["id"].(string)

	got = run(t, schema, fmt.Sprintf(`{
		node(id: %q) { __typename id ... on Task { title done } }
	}`, id))

	require.Equal(t, map[string]any{"node": map[string]any{
		"__typename": "Task",
		"id":         id,
		"title":      "Write the schema",
		"done":       true,
	}}, got)
}

// TestNodeDispatchesOnType checks that the id decides which fetcher runs.
func TestNodeDispatchesOnType(t *testing.T) {
	schema := nodeSchema(t)

	got := run(t, schema, fmt.Sprintf(`{
		node(id: %q) { __typename ... on User { name } }
	}`, globalID(t, "User", "u1")))

	require.Equal(t, map[string]any{"node": map[string]any{
		"__typename": "User",
		"name":       "Ada",
	}}, got)
}

// TestNodes covers the plural form, including a null for an id that resolves to
// nothing.
func TestNodes(t *testing.T) {
	schema := nodeSchema(t)

	got := run(t, schema, fmt.Sprintf(`{
		nodes(ids: [%q, %q, %q]) { __typename }
	}`, globalID(t, "Task", "t1"), globalID(t, "Task", "missing"), globalID(t, "User", "u1")))

	require.Equal(t, map[string]any{"nodes": []any{
		map[string]any{"__typename": "Task"},
		nil,
		map[string]any{"__typename": "User"},
	}}, got)
}

// TestMalformedIDIsAClientError checks that a bad identifier says so.
func TestMalformedIDIsAClientError(t *testing.T) {
	schema := nodeSchema(t)

	q := graphql.MustParse(`{ node(id: "not base64 at all!!") { id } }`, nil)
	require.NoError(t, graphql.PrepareQuery(context.Background(), schema.Query, q.SelectionSet))

	e := graphql.NewExecutor(graphql.NewImmediateGoroutineScheduler())
	_, err := e.Execute(context.Background(), schema.Query, nil, q)
	require.Error(t, err)
	require.Contains(t, graphql.SanitizeError(err), "malformed global id")

	q = graphql.MustParse(fmt.Sprintf(`{ node(id: %q) { id } }`, globalID(t, "Ghost", "1")), nil)
	require.NoError(t, graphql.PrepareQuery(context.Background(), schema.Query, q.SelectionSet))
	_, err = e.Execute(context.Background(), schema.Query, nil, q)
	require.Error(t, err)
	require.Contains(t, graphql.SanitizeError(err), `unknown node type "Ghost"`)
}

// TestCustomCodec checks that the identifier format is the caller's choice.
func TestCustomCodec(t *testing.T) {
	b := lightning.New(relay.Plugin(relay.WithCodec(slashCodec{})))
	lightning.Object[Task](b)
	relay.Node(b, fetchTask)
	b.Query().Field("firstTask", func(ctx context.Context, _ *lightning.Root) (*Task, error) {
		return tasks["t1"], nil
	})

	schema := b.MustBuild()

	got := run(t, schema, `{ firstTask { id } }`)
	require.Equal(t, "Task/t1", got["firstTask"].(map[string]any)["id"])

	got = run(t, schema, `{ node(id: "Task/t2") { ... on Task { title } } }`)
	require.Equal(t, map[string]any{"node": map[string]any{"title": "Make it live"}}, got)
}

type slashCodec struct{}

func (slashCodec) Encode(typeName, localID string) (string, error) {
	return typeName + "/" + localID, nil
}

func (slashCodec) Decode(globalID string) (string, string, error) {
	for i := range globalID {
		if globalID[i] == '/' {
			return globalID[:i], globalID[i+1:], nil
		}
	}
	return "", "", fmt.Errorf("malformed global id")
}

// TestNodeWithoutTheRelayPluginSaysSo checks the error when the plugin is
// missing.
func TestNodeWithoutTheRelayPluginSaysSo(t *testing.T) {
	b := lightning.New()
	lightning.Object[Task](b)
	relay.Node(b, fetchTask)
	b.Query().Field("firstTask", func(ctx context.Context, _ *lightning.Root) (*Task, error) { return nil, nil })

	_, err := b.Build()
	require.Error(t, err)
	require.Contains(t, err.Error(), "relay.Plugin()")
}

// TestUndeclaredNodeTypeSaysSo checks the error when a node type was never
// declared as an object.
func TestUndeclaredNodeTypeSaysSo(t *testing.T) {
	b := lightning.New(relay.Plugin())
	lightning.Object[User](b)
	relay.Node(b, fetchUser)
	// Task is registered as a node but never declared.
	relay.Node(b, fetchTask)
	b.Query().Field("viewer", func(ctx context.Context, _ *lightning.Root) (*User, error) { return nil, nil })

	_, err := b.Build()
	require.Error(t, err)
	require.Contains(t, err.Error(), "Task")
	require.Contains(t, err.Error(), "lightning.Object")
}

// TestConnection covers the Relay connection shape and its pagination.
func TestConnection(t *testing.T) {
	all := []*Task{
		{Key: "t1", Title: "one"}, {Key: "t2", Title: "two"}, {Key: "t3", Title: "three"},
		{Key: "t4", Title: "four"}, {Key: "t5", Title: "five"},
	}

	b := lightning.New(relay.Plugin())
	lightning.Object[Task](b)
	relay.Node(b, fetchTask)

	relay.Connection(b.Query(), "tasks", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Task, error) {
		return all, nil
	}).Describe("Every task, oldest first.")

	schema := b.MustBuild()

	sdl, err := graphql.PrintSchema(schema)
	require.NoError(t, err)
	require.Contains(t, sdl, "type TaskConnection {")
	require.Contains(t, sdl, "type TaskEdge {")
	require.Contains(t, sdl, "node: Task!")
	require.Contains(t, sdl, "cursor: String!")
	require.Contains(t, sdl, "hasPreviousPage: Boolean!")
	require.Contains(t, sdl, "startCursor: String\n")
	require.NotContains(t, sdl, "hasPrevPage")

	got := run(t, schema, `{
		tasks(first: 2) {
			totalCount
			edges { cursor node { title } }
			pageInfo { hasNextPage hasPreviousPage startCursor endCursor }
		}
	}`)

	conn := got["tasks"].(map[string]any)
	require.Equal(t, "5", conn["totalCount"])

	edges := conn["edges"].([]any)
	require.Len(t, edges, 2)
	require.Equal(t, map[string]any{"title": "one"}, edges[0].(map[string]any)["node"])

	info := conn["pageInfo"].(map[string]any)
	require.Equal(t, true, info["hasNextPage"])
	require.Equal(t, false, info["hasPreviousPage"])
	require.NotEmpty(t, info["endCursor"])
}

// TestCursorsAreInsertionStable is the property that matters for a live-query
// library: a cursor names the item it points at, so inserting earlier in the
// list does not move it.
func TestCursorsAreInsertionStable(t *testing.T) {
	all := []*Task{{Key: "t1", Title: "one"}, {Key: "t2", Title: "two"}, {Key: "t3", Title: "three"}}

	b := lightning.New(relay.Plugin())
	lightning.Object[Task](b)
	relay.Node(b, fetchTask)
	relay.Connection(b.Query(), "tasks", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Task, error) {
		return all, nil
	})
	schema := b.MustBuild()

	// Take a cursor pointing at the second item.
	got := run(t, schema, `{ tasks(first: 3) { edges { cursor node { title } } } }`)
	edges := got["tasks"].(map[string]any)["edges"].([]any)
	secondCursor := edges[1].(map[string]any)["cursor"].(string)

	// Insert an item at the front, as a live query's underlying data might.
	all = append([]*Task{{Key: "t0", Title: "zero"}}, all...)

	// The cursor still points at "two", not at whatever is now in slot 1.
	got = run(t, schema, fmt.Sprintf(`{ tasks(after: %q, first: 1) { edges { node { title } } } }`, secondCursor))
	edges = got["tasks"].(map[string]any)["edges"].([]any)
	require.Len(t, edges, 1)
	require.Equal(t, map[string]any{"title": "three"}, edges[0].(map[string]any)["node"],
		"an offset cursor would have shifted; a key-based one does not")
}

// TestConnectionWithArguments checks a connection that takes its own arguments
// alongside the pagination ones.
func TestConnectionWithArguments(t *testing.T) {
	all := []*Task{
		{Key: "t1", Title: "one", Done: true},
		{Key: "t2", Title: "two"},
		{Key: "t3", Title: "three", Done: true},
	}

	b := lightning.New(relay.Plugin())
	lightning.Object[Task](b)
	relay.Node(b, fetchTask)

	relay.ConnectionArgs(b.Query(), "tasks", func(ctx context.Context, _ *lightning.Root, p relay.Page, args struct {
		Done *bool `description:"Only tasks in this state."`
	}) ([]*Task, error) {
		if args.Done == nil {
			return all, nil
		}
		var out []*Task
		for _, task := range all {
			if task.Done == *args.Done {
				out = append(out, task)
			}
		}
		return out, nil
	})

	schema := b.MustBuild()

	sdl, err := graphql.PrintSchema(schema)
	require.NoError(t, err)
	require.Contains(t, sdl, "Only tasks in this state.")
	require.Contains(t, sdl, "done: Boolean")
	require.Contains(t, sdl, "first: Int")

	got := run(t, schema, `{ tasks(done: true, first: 10) { totalCount edges { node { title } } } }`)
	conn := got["tasks"].(map[string]any)
	require.Equal(t, "2", conn["totalCount"])
	require.Len(t, conn["edges"], 2)
}

// TestEmptyConnectionHasNullCursors checks the nullability of an empty page.
func TestEmptyConnectionHasNullCursors(t *testing.T) {
	b := lightning.New(relay.Plugin())
	lightning.Object[Task](b)
	relay.Node(b, fetchTask)
	relay.Connection(b.Query(), "tasks", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Task, error) {
		return nil, nil
	})
	schema := b.MustBuild()

	got := run(t, schema, `{ tasks { totalCount edges { cursor } pageInfo { startCursor endCursor hasNextPage } } }`)
	conn := got["tasks"].(map[string]any)
	require.Equal(t, "0", conn["totalCount"])
	require.Empty(t, conn["edges"])

	info := conn["pageInfo"].(map[string]any)
	require.Nil(t, info["startCursor"])
	require.Nil(t, info["endCursor"])
	require.Equal(t, false, info["hasNextPage"])
}

// TestKeysTravelOnlyWhenAsked pins the contract of __key: being a node names a
// value for the live-query diff, but an ordinary response carries no trace of
// it. The diff protocol opts in; a plain HTTP query does not.
func TestKeysTravelOnlyWhenAsked(t *testing.T) {
	schema := nodeSchema(t)

	q, err := graphql.Parse(`{ firstTask { title } }`, nil)
	require.NoError(t, err)
	require.NoError(t, graphql.PrepareQuery(context.Background(), schema.Query, q.SelectionSet))

	execute := func(ctx context.Context) map[string]any {
		e := graphql.NewExecutor(graphql.NewImmediateGoroutineScheduler())
		result, err := e.Execute(ctx, schema.Query, nil, q)
		require.NoError(t, err)

		encoded, err := json.Marshal(result)
		require.NoError(t, err)
		var out map[string]any
		require.NoError(t, json.Unmarshal(encoded, &out))
		return out["firstTask"].(map[string]any)
	}

	plain := execute(context.Background())
	require.NotContains(t, plain, "__key")
	require.Equal(t, "Write the schema", plain["title"])

	keyed := execute(graphql.WithKeys(context.Background()))
	require.Equal(t, "t1", keyed["__key"])
}
