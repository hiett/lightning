package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hiett/lightning/batch"
	"github.com/hiett/lightning/reactive"
)

// This file implements the graphql-transport-ws protocol, the subprotocol the
// graphql-ws client library speaks and the one any standard GraphQL client can
// be pointed at.
//
// https://github.com/enisdenjo/graphql-ws/blob/master/PROTOCOL.md
//
// It is separate from server.go, which speaks thunder's bespoke diff-pushing
// protocol. That protocol is what makes live queries cheap and is what the
// Relay network layer in js/ uses; this one is the interoperable fallback, and
// sends a full payload every time.

// TransportWSSubprotocol is the websocket subprotocol this handler speaks.
const TransportWSSubprotocol = "graphql-transport-ws"

// Message types, client to server.
const (
	msgConnectionInit = "connection_init"
	msgSubscribe      = "subscribe"
	msgComplete       = "complete"
	msgPing           = "ping"
	msgPong           = "pong"
)

// Message types, server to client.
const (
	msgConnectionAck = "connection_ack"
	msgNext          = "next"
	msgError         = "error"
)

// Close codes defined by the protocol.
const (
	closeBadRequest              = 4400
	closeUnauthorized            = 4401
	closeConnectionInitTimeout   = 4408
	closeSubscriberAlreadyExists = 4409
	closeTooManyInitRequests     = 4429
)

// DefaultConnectionInitTimeout is how long a client has to send
// connection_init before the connection is closed.
const DefaultConnectionInitTimeout = 10 * time.Second

// DefaultWriteTimeout bounds a single write. Without one, a client that stops
// reading fills the kernel buffer and pins the writing goroutine for ever,
// which for a live query means one stalled browser tab holding a server
// goroutine and everything behind the write lock.
const DefaultWriteTimeout = 10 * time.Second

// transportWSMessage is one message in either direction. Payload is left raw so
// that it can be decoded according to the message type.
type transportWSMessage struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// subscribePayload is the payload of a subscribe message: an ordinary GraphQL
// request.
type subscribePayload struct {
	Query         string                 `json:"query"`
	OperationName string                 `json:"operationName"`
	Variables     map[string]interface{} `json:"variables"`
	Extensions    map[string]interface{} `json:"extensions"`
}

// TransportWSOption configures a TransportWSHandler.
type TransportWSOption func(*transportWSHandler)

// WithTransportWSExecutor sets the executor used to run operations.
func WithTransportWSExecutor(executor ExecutorRunner) TransportWSOption {
	return func(h *transportWSHandler) { h.executor = executor }
}

// WithTransportWSMiddleware adds middleware run around every operation.
func WithTransportWSMiddleware(middlewares ...MiddlewareFunc) TransportWSOption {
	return func(h *transportWSHandler) { h.middlewares = append(h.middlewares, middlewares...) }
}

// WithTransportWSConnectionInit installs a callback run when a client sends
// connection_init. Returning an error refuses the connection with the
// protocol's "unauthorized" close code, which is how authentication is done.
//
// The context it returns is used for every operation on the connection, so it
// is also how a connection's identity is carried into resolvers.
func WithTransportWSConnectionInit(fn func(ctx context.Context, payload json.RawMessage) (context.Context, error)) TransportWSOption {
	return func(h *transportWSHandler) { h.onConnectionInit = fn }
}

// WithTransportWSConnectionInitTimeout sets how long a client has to send
// connection_init.
func WithTransportWSConnectionInitTimeout(d time.Duration) TransportWSOption {
	return func(h *transportWSHandler) { h.connectionInitTimeout = d }
}

// WithTransportWSWriteTimeout bounds how long a single write may take before
// the connection is considered dead.
func WithTransportWSWriteTimeout(d time.Duration) TransportWSOption {
	return func(h *transportWSHandler) { h.writeTimeout = d }
}

// WithTransportWSMinRerunInterval sets the minimum interval between
// re-executions of a live subscription, which debounces a rapidly changing
// dependency.
func WithTransportWSMinRerunInterval(d time.Duration) TransportWSOption {
	return func(h *transportWSHandler) { h.minRerunInterval = d }
}

// WithTransportWSUpgrader replaces the websocket upgrader, which is how origin
// checking and buffer sizes are configured.
func WithTransportWSUpgrader(upgrader websocket.Upgrader) TransportWSOption {
	return func(h *transportWSHandler) { h.upgrader = upgrader }
}

// TransportWSHandler serves GraphQL over the graphql-transport-ws subprotocol.
//
// Queries and mutations are executed once and completed. A subscription is
// executed like a query and re-executed whenever a resource it depended on is
// invalidated, with the complete result sent each time — a live query, in the
// shape any standard subscription client understands.
func TransportWSHandler(schema *Schema, options ...TransportWSOption) http.Handler {
	h := &transportWSHandler{
		schema:                schema,
		executor:              NewExecutor(NewImmediateGoroutineScheduler()),
		connectionInitTimeout: DefaultConnectionInitTimeout,
		minRerunInterval:      DefaultMinRerunInterval,
		writeTimeout:          DefaultWriteTimeout,
		upgrader: websocket.Upgrader{
			Subprotocols: []string{TransportWSSubprotocol},
		},
	}
	h.validator, h.validatorError = NewValidator(schema)

	for _, option := range options {
		option(h)
	}
	return h
}

type transportWSHandler struct {
	schema      *Schema
	executor    ExecutorRunner
	middlewares []MiddlewareFunc
	upgrader    websocket.Upgrader

	validator      *Validator
	validatorError error

	onConnectionInit      func(context.Context, json.RawMessage) (context.Context, error)
	connectionInitTimeout time.Duration
	minRerunInterval      time.Duration
	writeTimeout          time.Duration
}

func (h *transportWSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.validatorError != nil {
		http.Error(w, h.validatorError.Error(), http.StatusInternalServerError)
		return
	}

	socket, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has already written a response.
		return
	}

	conn := &transportWSConn{
		handler: h,
		socket:  socket,
		streams: map[string]context.CancelFunc{},
	}
	conn.serve(r.Context())
}

// transportWSConn is one websocket connection.
type transportWSConn struct {
	handler *transportWSHandler
	socket  *websocket.Conn

	// writeMu serialises writes; gorilla permits only one writer at a time and
	// subscriptions push from their own goroutines.
	writeMu sync.Mutex

	mu          sync.Mutex
	streams     map[string]context.CancelFunc
	initialised bool
	closed      bool

	// initTimer closes a connection that never initialises; it is stopped as
	// soon as one does.
	initTimer *time.Timer

	// ctx is the context every operation on this connection runs under. It
	// starts as the request's context and is replaced by whatever
	// WithTransportWSConnectionInit returns, which is how a connection's
	// identity reaches resolvers.
	ctx context.Context
}

// operationContext returns the context operations on this connection run under.
func (c *transportWSConn) operationContext() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ctx
}

func (c *transportWSConn) serve(parent context.Context) {
	defer c.socket.Close()

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	c.mu.Lock()
	c.ctx = ctx
	c.mu.Unlock()

	// A client that connects and says nothing holds a connection open forever.
	initTimer := time.AfterFunc(c.handler.connectionInitTimeout, func() {
		c.mu.Lock()
		initialised := c.initialised
		c.mu.Unlock()
		if !initialised {
			c.closeWith(closeConnectionInitTimeout, "Connection initialisation timeout")
		}
	})
	defer initTimer.Stop()

	c.mu.Lock()
	c.initTimer = initTimer
	c.mu.Unlock()

	for {
		var message transportWSMessage
		if err := c.socket.ReadJSON(&message); err != nil {
			// A read error means the connection is gone, one way or another.
			cancel()
			c.cancelAllStreams()
			return
		}

		if err := c.handleMessage(ctx, &message); err != nil {
			// handleMessage has already closed the connection.
			cancel()
			c.cancelAllStreams()
			return
		}
	}
}

func (c *transportWSConn) handleMessage(ctx context.Context, message *transportWSMessage) error {
	switch message.Type {
	case msgConnectionInit:
		return c.handleConnectionInit(ctx, message)

	case msgPing:
		c.write(transportWSMessage{Type: msgPong, Payload: message.Payload})
		return nil

	case msgPong:
		// A pong needs no reply.
		return nil

	case msgSubscribe:
		return c.handleSubscribe(ctx, message)

	case msgComplete:
		c.cancelStream(message.ID)
		return nil

	default:
		return c.closeWith(closeBadRequest, fmt.Sprintf("Unknown message type %q", message.Type))
	}
}

func (c *transportWSConn) handleConnectionInit(ctx context.Context, message *transportWSMessage) error {
	c.mu.Lock()
	if c.initialised {
		c.mu.Unlock()
		return c.closeWith(closeTooManyInitRequests, "Too many initialisation requests")
	}
	c.initialised = true
	if c.initTimer != nil {
		c.initTimer.Stop()
	}
	c.mu.Unlock()

	if c.handler.onConnectionInit != nil {
		initialised, err := c.handler.onConnectionInit(ctx, message.Payload)
		if err != nil {
			return c.closeWith(closeUnauthorized, err.Error())
		}
		if initialised != nil {
			// Operations run under this from now on. It is expected to derive
			// from the context passed in, so that closing the connection still
			// cancels everything running on it.
			c.mu.Lock()
			c.ctx = initialised
			c.mu.Unlock()
		}
	}

	c.write(transportWSMessage{Type: msgConnectionAck})
	return nil
}

func (c *transportWSConn) handleSubscribe(ctx context.Context, message *transportWSMessage) error {
	c.mu.Lock()
	initialised := c.initialised
	_, exists := c.streams[message.ID]
	c.mu.Unlock()

	if !initialised {
		return c.closeWith(closeUnauthorized, "Unauthorized")
	}
	if message.ID == "" {
		return c.closeWith(closeBadRequest, "Subscribe message must carry an id")
	}
	if exists {
		return c.closeWith(closeSubscriberAlreadyExists, fmt.Sprintf("Subscriber for %s already exists", message.ID))
	}

	var payload subscribePayload
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		return c.closeWith(closeBadRequest, fmt.Sprintf("Invalid subscribe payload: %s", err))
	}

	ctx = c.operationContext()

	query, err := c.handler.validator.Parse(payload.Query, payload.Variables, payload.OperationName)
	if err != nil {
		// A request that never began executing is reported on the stream and
		// the stream ends; the connection stays open.
		c.writeErrors(message.ID, AsResponseErrors(err))
		return nil
	}

	root, err := c.rootType(query.Kind)
	if err != nil {
		c.writeErrors(message.ID, AsResponseErrors(err))
		return nil
	}

	if err := PrepareQuery(ctx, root, query.SelectionSet); err != nil {
		c.writeErrors(message.ID, AsResponseErrors(err))
		return nil
	}

	streamCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		cancel()
		return nil
	}
	c.streams[message.ID] = cancel
	c.mu.Unlock()

	go func() {
		// Always cancel, even for an operation that finished on its own: the
		// context is derived from the connection's, which outlives it, and an
		// uncancelled child stays attached to its parent.
		defer cancel()
		c.runStream(streamCtx, message.ID, query, root, payload)
	}()
	return nil
}

// rootType picks the schema root an operation runs against.
func (c *transportWSConn) rootType(kind string) (Type, error) {
	switch kind {
	case "query":
		return c.handler.schema.Query, nil
	case "mutation":
		return c.handler.schema.Mutation, nil
	case "subscription":
		if c.handler.schema.Subscription == nil {
			return nil, NewClientError("this schema has no subscription root")
		}
		return c.handler.schema.Subscription, nil
	default:
		return nil, NewClientError("unsupported operation %q", kind)
	}
}

// runStream executes one operation and, for a subscription, keeps re-executing
// it until the client unsubscribes or the connection goes away.
func (c *transportWSConn) runStream(ctx context.Context, id string, query *Query, root Type, payload subscribePayload) {
	if query.Kind == "subscription" {
		c.runLive(ctx, id, query, root, payload)
	} else {
		c.runOnce(ctx, id, query, root, payload)
	}

	// A client that cancelled the stream already removed it and expects
	// nothing further; a stream that finished on its own owes a complete.
	if c.removeStream(id) {
		c.write(transportWSMessage{ID: id, Type: msgComplete})
	}
}

// execute runs the operation once and returns the result.
func (c *transportWSConn) execute(ctx context.Context, id string, query *Query, root Type, payload subscribePayload, initial bool) (interface{}, error) {
	ctx = batch.WithBatching(ctx)

	middlewares := append([]MiddlewareFunc(nil), c.handler.middlewares...)
	middlewares = append(middlewares, func(input *ComputationInput, next MiddlewareNextFunc) *ComputationOutput {
		output := next(input)
		output.Current, output.Error = c.handler.executor.Execute(input.Ctx, root, nil, input.ParsedQuery)
		return output
	})

	output := RunMiddlewares(middlewares, &ComputationInput{
		Ctx:                  ctx,
		Id:                   id,
		ParsedQuery:          query,
		IsInitialComputation: initial,
		Query:                payload.Query,
		Variables:            payload.Variables,
		Extensions:           payload.Extensions,
	})
	return output.Current, output.Error
}

// runOnce serves a query or a mutation: execute, send the result, done.
//
// It deliberately does not use a reactive rerunner. Nothing here wants
// re-execution, and reactive.AddDependency and reactive.Cache both work outside
// one, so running directly avoids holding a computation open for an operation
// that will never run again.
func (c *transportWSConn) runOnce(ctx context.Context, id string, query *Query, root Type, payload subscribePayload) {
	result, err := c.execute(ctx, id, query, root, payload, true)
	if err != nil {
		if ErrorCause(err) != context.Canceled {
			c.writeErrors(id, AsResponseErrors(err))
		}
		return
	}

	c.write(transportWSMessage{
		ID:      id,
		Type:    msgNext,
		Payload: mustMarshalPayload(&Response{Data: result, HasData: true}),
	})
}

// runLive serves a subscription: execute, send the result, and do it again
// every time a resource the execution read is invalidated.
//
// An error ends the stream with an error message, which the protocol treats as
// terminal for that operation. A subscription that wants to survive a transient
// failure should retry inside its resolver.
func (c *transportWSConn) runLive(ctx context.Context, id string, query *Query, root Type, payload subscribePayload) {
	var once sync.Once
	finished := make(chan struct{})
	done := func() { once.Do(func() { close(finished) }) }

	initial := true
	runner := reactive.NewRerunner(ctx, func(ctx context.Context) (interface{}, error) {
		result, err := c.execute(ctx, id, query, root, payload, initial)
		initial = false

		if err != nil {
			if ErrorCause(err) != context.Canceled {
				c.writeErrors(id, AsResponseErrors(err))
			}
			done()
			return nil, err
		}

		c.write(transportWSMessage{
			ID:      id,
			Type:    msgNext,
			Payload: mustMarshalPayload(&Response{Data: result, HasData: true}),
		})
		return nil, nil
	}, c.handler.minRerunInterval, false)

	select {
	case <-finished:
	case <-ctx.Done():
	}
	runner.Stop()
}

func (c *transportWSConn) write(message transportWSMessage) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return
	}

	if c.handler.writeTimeout > 0 {
		if err := c.socket.SetWriteDeadline(time.Now().Add(c.handler.writeTimeout)); err != nil {
			c.socket.Close()
			return
		}
	}

	if err := c.socket.WriteJSON(message); err != nil {
		if !isCloseError(err) {
			log.Printf("graphql-transport-ws: write: %s", err)
		}
		c.socket.Close()
	}
}

func (c *transportWSConn) writeErrors(id string, errs []*ResponseError) {
	c.write(transportWSMessage{
		ID:      id,
		Type:    msgError,
		Payload: mustMarshalPayload(errs),
	})
	c.removeStream(id)
}

// closeWith sends a protocol close frame and marks the connection closed. It
// always returns a non-nil error so callers can return it to end their loop.
func (c *transportWSConn) closeWith(code int, reason string) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("connection already closed")
	}
	c.closed = true
	c.mu.Unlock()

	c.writeMu.Lock()
	// The protocol's close reasons can exceed the 123-byte limit a close frame
	// allows, so they are truncated rather than dropped.
	if len(reason) > 123 {
		reason = reason[:123]
	}
	deadline := time.Now().Add(time.Second)
	_ = c.socket.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), deadline)
	c.writeMu.Unlock()

	c.socket.Close()
	return fmt.Errorf("closed connection: %s", reason)
}

// removeStream forgets a stream, reporting whether it was still registered.
func (c *transportWSConn) removeStream(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.streams[id]; !ok {
		return false
	}
	delete(c.streams, id)
	return true
}

func (c *transportWSConn) cancelStream(id string) {
	c.mu.Lock()
	cancel, ok := c.streams[id]
	if ok {
		delete(c.streams, id)
	}
	c.mu.Unlock()

	if ok {
		cancel()
	}
}

func (c *transportWSConn) cancelAllStreams() {
	c.mu.Lock()
	c.closed = true
	cancels := make([]context.CancelFunc, 0, len(c.streams))
	for id, cancel := range c.streams {
		cancels = append(cancels, cancel)
		delete(c.streams, id)
	}
	c.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
}

// mustMarshalPayload marshals a message payload. The values passed to it are
// always marshallable, and a failure here would mean a bug rather than bad
// input, so it is reported as an error payload rather than panicking a
// connection.
func mustMarshalPayload(value interface{}) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		fallback, _ := json.Marshal([]*ResponseError{{Message: "Internal server error"}})
		return fallback
	}
	return encoded
}
