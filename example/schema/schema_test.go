package schema_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hiett/lightning/example/schema"
	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/graphql/introspection"
	"github.com/hiett/lightning/invalidation"
	"github.com/stretchr/testify/require"
)

func newExample(t *testing.T) (*schema.Store, *graphql.Schema) {
	t.Helper()

	invalidator := invalidation.New(invalidation.NewMemorySource())

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ready := make(chan struct{})
	go func() {
		close(ready)
		_ = invalidator.Run(ctx)
	}()
	<-ready

	store := schema.NewStore(invalidator)
	return store, schema.Build(store)
}

func post(t *testing.T, built *graphql.Schema, query string, variables map[string]interface{}) map[string]interface{} {
	t.Helper()

	body, err := json.Marshal(map[string]interface{}{"query": query, "variables": variables})
	require.NoError(t, err)

	request := httptest.NewRequest("POST", "/graphql", strings.NewReader(string(body)))
	recorder := httptest.NewRecorder()
	graphql.HTTPHandler(built).ServeHTTP(recorder, request)

	var response struct {
		Data   map[string]interface{} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), "body: %s", recorder.Body.String())
	require.Empty(t, response.Errors, "body: %s", recorder.Body.String())
	return response.Data
}

// TestExampleSchemaIsValid checks the schema the example exports is legal and
// carries everything a Relay client needs.
func TestExampleSchemaIsValid(t *testing.T) {
	_, built := newExample(t)

	astSchema, err := graphql.ASTSchema(built)
	require.NoError(t, err)

	for _, name := range []string{"Node", "Actor", "Task", "User", "Team", "TaskConnection", "TaskEdge", "PageInfo"} {
		require.Contains(t, astSchema.Types, name)
	}
	require.NotNil(t, astSchema.Subscription, "the example declares a subscription root")

	// A Relay pagination query and a refetch query both validate.
	v, err := graphql.NewValidator(built)
	require.NoError(t, err)

	_, err = v.Parse(`
		query Tasks($count: Int! = 3, $cursor: String) {
			tasks(first: $count, after: $cursor) {
				edges { cursor node { id title done owner { __typename displayName } } }
				pageInfo { hasNextPage endCursor }
			}
		}`, nil, "")
	require.NoError(t, err)

	_, err = v.Parse(`
		query Refetch($id: ID!) {
			node(id: $id) { id ... on Task { title done } }
		}`, nil, "")
	require.NoError(t, err)
}

// TestExampleNodeRefetch checks that a global id from a connection refetches
// through the node field, which is what usePaginationFragment relies on.
func TestExampleNodeRefetch(t *testing.T) {
	_, built := newExample(t)

	data := post(t, built, `{ tasks(first: 1) { edges { node { id title } } } }`, nil)
	edges := data["tasks"].(map[string]interface{})["edges"].([]interface{})
	node := edges[0].(map[string]interface{})["node"].(map[string]interface{})

	id := node["id"].(string)
	title := node["title"].(string)

	refetched := post(t, built,
		`query Refetch($id: ID!) { node(id: $id) { __typename ... on Task { id title } } }`,
		map[string]interface{}{"id": id})

	task := refetched["node"].(map[string]interface{})
	require.Equal(t, "Task", task["__typename"])
	require.Equal(t, id, task["id"])
	require.Equal(t, title, task["title"])
}

// TestExampleInterfaceDispatch checks the Actor interface resolves to the right
// concrete type.
func TestExampleInterfaceDispatch(t *testing.T) {
	_, built := newExample(t)

	data := post(t, built, `{
		tasks(first: 5) {
			edges { node { title owner { __typename displayName ... on User { email } ... on Team { members } } } }
		}
	}`, nil)

	edges := data["tasks"].(map[string]interface{})["edges"].([]interface{})

	seen := map[string]bool{}
	for _, edge := range edges {
		owner := edge.(map[string]interface{})["node"].(map[string]interface{})["owner"].(map[string]interface{})
		typename := owner["__typename"].(string)
		seen[typename] = true

		switch typename {
		case "User":
			require.Contains(t, owner, "email")
			require.NotContains(t, owner, "members")
		case "Team":
			require.Contains(t, owner, "members")
			require.NotContains(t, owner, "email")
		default:
			t.Fatalf("unexpected owner type %q", typename)
		}
	}
	require.True(t, seen["User"] && seen["Team"], "the fixture should have both kinds of owner, saw %v", seen)
}

// TestExampleMutation checks a mutation takes a global id and changes data.
func TestExampleMutation(t *testing.T) {
	_, built := newExample(t)

	data := post(t, built, `{ tasks(first: 5) { edges { node { id done title } } } }`, nil)
	edges := data["tasks"].(map[string]interface{})["edges"].([]interface{})

	var id string
	for _, edge := range edges {
		node := edge.(map[string]interface{})["node"].(map[string]interface{})
		if node["done"] == false {
			id = node["id"].(string)
			break
		}
	}
	require.NotEmpty(t, id, "the fixture should have an undone task")

	changed := post(t, built,
		`mutation Done($id: ID!) { setTaskDone(id: $id, done: true) { id done } }`,
		map[string]interface{}{"id": id})
	require.Equal(t, true, changed["setTaskDone"].(map[string]interface{})["done"])

	refetched := post(t, built,
		`query Check($id: ID!) { node(id: $id) { ... on Task { done } } }`,
		map[string]interface{}{"id": id})
	require.Equal(t, true, refetched["node"].(map[string]interface{})["done"])
}

// TestExampleLiveQuery is the end-to-end proof that the whole thing works: a
// subscription over graphql-transport-ws, a mutation through the store, and a
// new payload pushed because the store invalidated the key the subscription
// depended on.
func TestExampleLiveQuery(t *testing.T) {
	store, built := newExample(t)

	server := httptest.NewServer(graphql.TransportWSHandler(built,
		graphql.WithTransportWSMinRerunInterval(0)))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	dialer := websocket.Dialer{Subprotocols: []string{graphql.TransportWSSubprotocol}}
	conn, _, err := dialer.Dial(url, nil)
	require.NoError(t, err)
	defer conn.Close()

	send := func(message map[string]interface{}) {
		t.Helper()
		require.NoError(t, conn.WriteJSON(message))
	}
	read := func() map[string]interface{} {
		t.Helper()
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
		var message map[string]interface{}
		require.NoError(t, conn.ReadJSON(&message))
		return message
	}

	send(map[string]interface{}{"type": "connection_init"})
	require.Equal(t, "connection_ack", read()["type"])

	send(map[string]interface{}{
		"id":   "live",
		"type": "subscribe",
		"payload": map[string]interface{}{
			"query": `subscription Live { tasks(first: 10) { totalCount edges { node { id title done } } } }`,
		},
	})

	titles := func(message map[string]interface{}) map[string]bool {
		t.Helper()
		require.Equal(t, "next", message["type"], "message: %v", message)
		payload := message["payload"].(map[string]interface{})
		tasks := payload["data"].(map[string]interface{})["tasks"].(map[string]interface{})

		out := map[string]bool{}
		for _, edge := range tasks["edges"].([]interface{}) {
			node := edge.(map[string]interface{})["node"].(map[string]interface{})
			out[node["title"].(string)] = node["done"].(bool)
		}
		return out
	}

	first := titles(read())
	require.Contains(t, first, "Write the schema")
	require.NotContains(t, first, "Feed the cat")

	// Change the data. Nothing here touches the websocket: the store announces
	// an invalidation, and the live query re-runs because it depended on it.
	_, err = store.AddTask(context.Background(), "Feed the cat", "u2")
	require.NoError(t, err)

	second := titles(read())
	require.Contains(t, second, "Feed the cat", "the live query should have been pushed the new task")
	require.Equal(t, false, second["Feed the cat"])

	// And again, for a change to an existing task.
	require.Eventually(t, func() bool {
		tasks, err := store.Tasks(context.Background())
		require.NoError(t, err)
		for _, task := range tasks {
			if task.Title == "Feed the cat" {
				_, err := store.SetTaskDone(context.Background(), task.Key, true)
				require.NoError(t, err)
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond)

	third := titles(read())
	require.Equal(t, true, third["Feed the cat"], "the live query should have been pushed the change")
}

// TestExampleIntrospectionWorks checks that the endpoint GraphiQL is pointed at
// can actually answer an introspection query, which requires the server to have
// registered the introspection schema.
func TestExampleIntrospectionWorks(t *testing.T) {
	_, built := newExample(t)
	introspection.AddIntrospectionToSchema(built)

	data := post(t, built, `{ __schema { queryType { name } subscriptionType { name } } }`, nil)

	schemaField := data["__schema"].(map[string]interface{})
	require.Equal(t, "Query", schemaField["queryType"].(map[string]interface{})["name"])
	require.Equal(t, "Subscription", schemaField["subscriptionType"].(map[string]interface{})["name"])

	// And __typename at the root, which Relay asks for.
	data = post(t, built, `{ __typename }`, nil)
	require.Equal(t, "Query", data["__typename"])
}

// TestExampleRejectsANonGlobalID checks that a mutation given something that is
// not a global id says so, rather than guessing.
//
// The check now happens while the argument is parsed, before the resolver runs,
// because the argument's Go type is relay.GID — so every mutation taking a
// global id gets it without writing anything.
func TestExampleRejectsANonGlobalID(t *testing.T) {
	_, built := newExample(t)

	body, err := json.Marshal(map[string]interface{}{
		"query":     `mutation Done($id: ID!) { setTaskDone(id: $id, done: true) { id } }`,
		"variables": map[string]interface{}{"id": "task1"},
	})
	require.NoError(t, err)

	request := httptest.NewRequest("POST", "/graphql", strings.NewReader(string(body)))
	recorder := httptest.NewRecorder()
	graphql.HTTPHandler(built).ServeHTTP(recorder, request)

	require.Contains(t, recorder.Body.String(), "malformed global id")
}
