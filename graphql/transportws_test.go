package graphql_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/graphql/schemabuilder"
	"github.com/hiett/lightning/reactive"
	"github.com/stretchr/testify/require"
)

// liveCounter is a resolver-visible piece of state whose changes invalidate a
// reactive resource, which is what drives a live subscription.
type liveCounter struct {
	mu       sync.Mutex
	value    int32
	resource *reactive.Resource
}

func (c *liveCounter) read(ctx context.Context) int32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	reactive.AddDependency(ctx, c.resource, nil)
	return c.value
}

func (c *liveCounter) set(v int32) {
	c.mu.Lock()
	c.value = v
	resource := c.resource
	c.resource = reactive.NewResource()
	c.mu.Unlock()
	resource.Invalidate()
}

func transportWSServer(t *testing.T) (*httptest.Server, *liveCounter) {
	t.Helper()

	state := &liveCounter{resource: reactive.NewResource()}

	schema := schemabuilder.NewSchema()
	schema.Query().FieldFunc("value", func(ctx context.Context) int32 { return state.read(ctx) })
	schema.Mutation().FieldFunc("setValue", func(args struct{ Value int32 }) bool {
		state.set(args.Value)
		return true
	})
	schema.Subscription().FieldFunc("value", func(ctx context.Context) int32 { return state.read(ctx) })
	schema.Query().FieldFunc("boom", func() (string, error) {
		return "", graphql.NewClientError("it broke")
	})

	built := schema.MustBuild()

	server := httptest.NewServer(graphql.TransportWSHandler(built,
		graphql.WithTransportWSMinRerunInterval(0)))
	t.Cleanup(server.Close)
	return server, state
}

// wsClient is a minimal graphql-transport-ws client, so the test exercises the
// protocol rather than an abstraction over it.
type wsClient struct {
	t    *testing.T
	conn *websocket.Conn
}

func dialTransportWS(t *testing.T, server *httptest.Server) *wsClient {
	t.Helper()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	dialer := websocket.Dialer{Subprotocols: []string{graphql.TransportWSSubprotocol}}
	conn, resp, err := dialer.Dial(url, nil)
	require.NoError(t, err)
	if resp != nil {
		require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	}
	t.Cleanup(func() { conn.Close() })

	return &wsClient{t: t, conn: conn}
}

type wsMessage struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func (c *wsClient) send(message wsMessage) {
	c.t.Helper()
	require.NoError(c.t, c.conn.WriteJSON(message))
}

func (c *wsClient) sendPayload(id, typ string, payload interface{}) {
	c.t.Helper()
	encoded, err := json.Marshal(payload)
	require.NoError(c.t, err)
	c.send(wsMessage{ID: id, Type: typ, Payload: encoded})
}

func (c *wsClient) read() wsMessage {
	c.t.Helper()
	require.NoError(c.t, c.conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	var message wsMessage
	require.NoError(c.t, c.conn.ReadJSON(&message))
	return message
}

func (c *wsClient) init() {
	c.t.Helper()
	c.send(wsMessage{Type: "connection_init"})
	require.Equal(c.t, "connection_ack", c.read().Type)
}

// dataOf pulls the data object out of a next message's payload.
func dataOf(t *testing.T, message wsMessage) map[string]interface{} {
	t.Helper()
	require.Equal(t, "next", message.Type, "payload: %s", message.Payload)

	var response struct {
		Data map[string]interface{} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(message.Payload, &response))
	return response.Data
}

// TestTransportWSQuery checks that a query runs once and completes.
func TestTransportWSQuery(t *testing.T) {
	server, _ := transportWSServer(t)
	client := dialTransportWS(t, server)
	client.init()

	client.sendPayload("1", "subscribe", map[string]interface{}{"query": "{ value }"})

	require.Equal(t, float64(0), dataOf(t, client.read())["value"])
	require.Equal(t, "complete", client.read().Type)
}

// TestTransportWSSubscriptionIsLive is the acceptance criterion: a subscription
// works from a standard graphql-ws client, and pushes a new payload when the
// underlying data is invalidated.
func TestTransportWSSubscriptionIsLive(t *testing.T) {
	server, state := transportWSServer(t)
	client := dialTransportWS(t, server)
	client.init()

	client.sendPayload("sub", "subscribe", map[string]interface{}{
		"query":         "subscription Watch { value }",
		"operationName": "Watch",
	})

	// The first payload is the current value.
	require.Equal(t, float64(0), dataOf(t, client.read())["value"])

	// Invalidating the dependency pushes a new full payload.
	state.set(7)
	require.Equal(t, float64(7), dataOf(t, client.read())["value"])

	state.set(9)
	require.Equal(t, float64(9), dataOf(t, client.read())["value"])

	// The client can stop it.
	client.send(wsMessage{ID: "sub", Type: "complete"})
}

// TestTransportWSMutation checks that a mutation runs against the mutation root.
func TestTransportWSMutation(t *testing.T) {
	server, state := transportWSServer(t)
	client := dialTransportWS(t, server)
	client.init()

	client.sendPayload("m", "subscribe", map[string]interface{}{
		"query":     "mutation Set($v: Int!) { setValue(value: $v) }",
		"variables": map[string]interface{}{"v": 42},
	})

	require.Equal(t, true, dataOf(t, client.read())["setValue"])
	require.Equal(t, "complete", client.read().Type)

	require.Equal(t, int32(42), state.value)
}

// TestTransportWSPing checks the keepalive messages.
func TestTransportWSPing(t *testing.T) {
	server, _ := transportWSServer(t)
	client := dialTransportWS(t, server)
	client.init()

	client.send(wsMessage{Type: "ping"})
	require.Equal(t, "pong", client.read().Type)
}

// TestTransportWSValidationError checks that an illegal query ends the stream
// with an error rather than killing the connection.
func TestTransportWSValidationError(t *testing.T) {
	server, _ := transportWSServer(t)
	client := dialTransportWS(t, server)
	client.init()

	client.sendPayload("bad", "subscribe", map[string]interface{}{"query": "{ nosuchfield }"})

	message := client.read()
	require.Equal(t, "error", message.Type)
	require.Contains(t, string(message.Payload), "Cannot query field")

	// The connection still works.
	client.send(wsMessage{Type: "ping"})
	require.Equal(t, "pong", client.read().Type)
}

// TestTransportWSFieldError checks a runtime error is reported on the stream.
func TestTransportWSFieldError(t *testing.T) {
	server, _ := transportWSServer(t)
	client := dialTransportWS(t, server)
	client.init()

	client.sendPayload("e", "subscribe", map[string]interface{}{"query": "{ boom }"})

	message := client.read()
	require.Equal(t, "error", message.Type)
	require.Contains(t, string(message.Payload), "it broke")
}

// TestTransportWSRequiresInit checks that a subscribe before connection_init is
// refused with the protocol's unauthorized close code.
func TestTransportWSRequiresInit(t *testing.T) {
	server, _ := transportWSServer(t)
	client := dialTransportWS(t, server)

	client.sendPayload("1", "subscribe", map[string]interface{}{"query": "{ value }"})

	require.NoError(t, client.conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, _, err := client.conn.ReadMessage()
	require.Error(t, err)

	closeErr, ok := err.(*websocket.CloseError)
	require.True(t, ok, "expected a close frame, got %v", err)
	require.Equal(t, 4401, closeErr.Code)
}

// TestTransportWSDuplicateSubscriber checks the protocol's duplicate-id rule.
func TestTransportWSDuplicateSubscriber(t *testing.T) {
	server, _ := transportWSServer(t)
	client := dialTransportWS(t, server)
	client.init()

	client.sendPayload("dup", "subscribe", map[string]interface{}{"query": "subscription { value }"})
	require.Equal(t, "next", client.read().Type)

	client.sendPayload("dup", "subscribe", map[string]interface{}{"query": "subscription { value }"})

	require.NoError(t, client.conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, _, err := client.conn.ReadMessage()
	require.Error(t, err)

	closeErr, ok := err.(*websocket.CloseError)
	require.True(t, ok, "expected a close frame, got %v", err)
	require.Equal(t, 4409, closeErr.Code)
}

// TestTransportWSConnectionInitCallback checks that authentication can refuse a
// connection.
func TestTransportWSConnectionInitCallback(t *testing.T) {
	schema := schemabuilder.NewSchema()
	schema.Query().FieldFunc("ok", func() bool { return true })
	built := schema.MustBuild()

	server := httptest.NewServer(graphql.TransportWSHandler(built,
		graphql.WithTransportWSConnectionInit(func(ctx context.Context, payload json.RawMessage) (context.Context, error) {
			var creds struct {
				Token string `json:"token"`
			}
			_ = json.Unmarshal(payload, &creds)
			if creds.Token != "secret" {
				return nil, graphql.NewClientError("bad token")
			}
			return ctx, nil
		})))
	defer server.Close()

	refused := dialTransportWS(t, server)
	refused.sendPayload("", "connection_init", map[string]interface{}{"token": "wrong"})

	require.NoError(t, refused.conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, _, err := refused.conn.ReadMessage()
	require.Error(t, err)
	closeErr, ok := err.(*websocket.CloseError)
	require.True(t, ok, "expected a close frame, got %v", err)
	require.Equal(t, 4401, closeErr.Code)

	accepted := dialTransportWS(t, server)
	accepted.sendPayload("", "connection_init", map[string]interface{}{"token": "secret"})
	require.Equal(t, "connection_ack", accepted.read().Type)
}
