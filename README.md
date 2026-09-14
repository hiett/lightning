# lightning

A Go GraphQL server library with **reactive live queries**, built to talk to a
Relay client without adapters or apologies.

lightning is a fork of [samsarahq/thunder](https://github.com/samsarahq/thunder),
which Samsara deprecated in February 2023. Thunder had one genuinely rare
capability — automatic dependency tracking during execution, re-execution on
invalidation, and JSON diffs pushed to clients over a websocket — and that is
the reason for the fork. Everything else has been brought up to the modern
specification.

```go
schema := schemabuilder.NewSchema()

schema.Query().FieldFunc("tasks", func(ctx context.Context) []*Task {
    return store.Tasks(ctx) // store.Tasks records a dependency
}, schemabuilder.Paginated)

http.Handle("/graphql", graphql.HTTPHandler(schema.MustBuild()))
```

When `store` later announces that tasks changed, every live query that read
them re-executes and its client is sent the difference. No polling, no manual
subscription plumbing, no invalidation logic in your resolvers beyond saying
what they read.

---

## What changed from thunder

| | thunder | lightning |
|---|---|---|
| Parser | `graphql-go/graphql` pinned to a 2016 commit | `vektah/gqlparser/v2` |
| Validation | none; queries failed lazily during execution | full, before execution begins |
| Scalars | `string`, `bool`, `int64`, `float64`, `bytes` | `String`, `Boolean`, `Int`, `Float`, `ID`, plus `Int64`, `Time`, `Bytes` |
| Errors | `"errors": ["..."]` | specification objects with `message`, `locations`, `path` |
| Interfaces | none | full, including `__typename` and type conditions |
| Relay `Node` | none | `node(id:)`, `nodes(ids:)`, global identifiers |
| Connections | close, with `hasPrevPage` | conformant, verified against relay-compiler |
| SDL export | none | `graphql.WriteSchemaFile` |
| Subscriptions | a bespoke websocket protocol | that, plus `graphql-transport-ws` |
| `operationName` | ignored | honoured |
| Dependencies | ~20, several abandoned | 6, all current |
| Databases | `livesql` + `sqlgen` built in | none; bring your own invalidation source |

`DECISIONS.md` records why each of those went the way it did.

---

## Getting started

```
go get github.com/hiett/lightning
```

The `example/` directory is a complete working server — an interface, the Node
interface, a connection, a mutation and a live query — with a Relay app in
`example/web/` that compiles against the exported schema. Run it:

```
cd example && go run ./cmd/server
```

and open <http://localhost:8080> for GraphiQL.

---

## Building a schema

Schemas are built by reflection over Go types. A struct becomes an object type,
its exported fields become fields, and `FieldFunc` adds resolvers.

```go
type Task struct {
    Title string `description:"What needs doing."`
    Done  bool
    Added time.Time `graphql:"-"` // not exposed
}

schema := schemabuilder.NewSchema()

task := schema.Object("Task", Task{})
task.Describe("A unit of work.")
task.FieldFunc("owner", func(ctx context.Context, t *Task) *User {
    return store.User(ctx, t.OwnerID)
}, schemabuilder.Description("Whoever the task belongs to."))

schema.Query().FieldFunc("tasks", func(ctx context.Context) []*Task {
    return store.Tasks(ctx)
})

built := schema.MustBuild()
```

### Scalars

| Go | GraphQL | Notes |
|---|---|---|
| `string` | `String` | |
| `bool` | `Boolean` | |
| `int8`, `int16`, `int32`, `uint8`, `uint16` | `Int` | everything that fits in 32 signed bits |
| `int`, `int64`, `uint`, `uint32`, `uint64` | `Int64` | **serialised as a decimal string** |
| `float32`, `float64` | `Float` | |
| `schemabuilder.ID` | `ID` | |
| `time.Time` | `Time` | RFC 3339 |
| `[]byte` | `Bytes` | base64 |

`Int64` is a string on the wire because a GraphQL `Int` is 32-bit and a JSON
number loses precision above 2⁵³ once a JavaScript client parses it. Go's `int`
is 64 bits, so it maps to `Int64` too; declare a field `int32` if it genuinely
is a small number and you want a JSON number.

### Documentation

Struct fields take tags; registered fields take options.

```go
type User struct {
    Name  string `description:"The user's display name."`
    Email string `description:"Where to reach them." deprecated:"Use emails instead."`
}

user.FieldFunc("friends", resolve,
    schemabuilder.Description("Everyone this user follows."),
    schemabuilder.ArgDescription("limit", "How many to return."),
    schemabuilder.Deprecated("Use following instead."))
```

Both reach introspection and the exported SDL.

---

## Interfaces

An interface is declared by a marker struct whose embedded pointers name its
implementing types — the same shape as `schemabuilder.Union`.

```go
type Actor struct {
    schemabuilder.Interface

    *User
    *Team
}

schema.Interface("Actor", Actor{}).
    Fields("id", "displayName").
    Describe("Whoever a task belongs to.")
```

A field returning an interface returns the struct with exactly one member set:

```go
task.FieldFunc("owner", func(ctx context.Context, t *Task) *Actor {
    if user := store.User(ctx, t.OwnerID); user != nil {
        return &Actor{User: user}
    }
    return &Actor{Team: store.Team(ctx, t.OwnerID)}
})
```

`Fields(...)` declares the interface's field set explicitly. Every named field
must exist on every implementing type with the same type and arguments, or the
schema fails to build. Leave it out and the interface exposes everything its
implementing types agree on.

---

## The Node interface and global identifiers

Relay's store keys off a globally unique `id`. Without one, `@refetchable`,
`usePaginationFragment` refetch and store normalisation all break.

A type declares itself a node by saying how to read its identifier and how to
fetch it back:

```go
task := schema.Object("Task", Task{})
task.Node(
    func(t *Task) string { return t.Key },
    func(ctx context.Context, id string) (*Task, error) { return store.Task(ctx, id), nil },
)
```

That gives the type an `id` field carrying its **global** identifier, makes it a
member of the `Node` interface, and adds `node(id: ID!): Node` and
`nodes(ids: [ID!]!): [Node]!` to the query root.

The default global identifier is base64 of `TypeName:localID`. That is
obfuscation, not secrecy — anyone can decode it. Replace the codec if your
identifiers must not be guessable or forgeable:

```go
schema.SetGlobalIDCodec(mySignedCodec{}) // implements schemabuilder.GlobalIDCodec
```

A mutation that takes a global identifier decodes it with the same codec:

```go
_, localID, err := schemabuilder.Base64GlobalIDCodec{}.Decode(args.Id.Value)
```

---

## Connections

Adding `schemabuilder.Paginated` to a field that returns a slice generates a
Relay connection: `TaskConnection`, `TaskEdge`, base64 cursors, `pageInfo` with
`hasNextPage` / `hasPreviousPage` / `startCursor` / `endCursor`, a `totalCount`,
and the `first` / `last` / `before` / `after` arguments.

```go
task.Key("key") // a paginated type needs a key field; cursors are built from it

schema.Query().FieldFunc("tasks", func(ctx context.Context) []*Task {
    return store.Tasks(ctx)
}, schemabuilder.Paginated)
```

Two extensions beyond the specification, both of which Relay ignores:
`totalCount`, and `pageInfo.pages` for page-number pagination.

---

## Subscriptions and live queries

There are two ways to push data, and they are not alternatives so much as
different audiences.

### `graphql-transport-ws`

The interoperable one. Register subscription roots and serve the protocol any
standard client speaks:

```go
schema.Subscription().FieldFunc("tasks", func(ctx context.Context) []*Task {
    return store.Tasks(ctx)
}, schemabuilder.Paginated)

http.Handle("/graphql/ws", graphql.TransportWSHandler(built))
```

A subscription here is a **live query**: the operation is executed like a
query, re-executed whenever a resource it read is invalidated, and the complete
result is sent each time. Queries and mutations work over the same socket.

Authentication hooks into `connection_init`:

```go
graphql.TransportWSHandler(built,
    graphql.WithTransportWSConnectionInit(func(ctx context.Context, payload json.RawMessage) (context.Context, error) {
        // returning an error closes the connection with code 4401
        return context.WithValue(ctx, userKey{}, user), nil
    }))
```

### lightning's diff protocol

The efficient one, and the reason for the fork. `graphql.Handler(built)` serves
a websocket that pushes a **JSON diff** of what changed rather than the whole
payload. A list of a thousand rows where one field changed is a few dozen
bytes.

The TypeScript package in `js/` is a Relay network layer for it: it applies each
diff to the payload it is holding and hands Relay the complete result, so the
Relay store sees ordinary responses.

```ts
import { createLightningNetwork } from "@hiett/lightning-relay";

const environment = new Environment({
  network: createLightningNetwork({ url: "ws://localhost:8080/graphql/live" }),
  store: new Store(new RecordSource()),
});
```

---

## How live queries work

This is the part worth understanding, because everything else is ordinary
GraphQL.

**1. Execution records what it read.** While a resolver runs, it can call
`reactive.AddDependency(ctx, resource, key)` to say "this value came from
there". The `reactive` package keeps a dependency graph of which computations
read which resources.

**2. Something invalidates a resource.** `resource.Strobe()` marks every
computation that read it as stale.

**3. The rerunner re-executes.** A `reactive.Rerunner` wrapping a query
re-runs it, debounced by a minimum interval, and produces a new result.

**4. The difference is pushed.** For the diff protocol, the new result is
diffed against the previous one and only the difference is sent.

The piece that is yours to supply is step 2 — where change events come from.
The `invalidation` package is the adapter:

```go
invalidator := invalidation.New(invalidation.NewMemorySource())
go invalidator.Run(ctx)

// in a resolver, say what you are about to read — before reading it
func (s *Store) Task(ctx context.Context, id string) *Task {
    invalidator.Depend(ctx, "task:"+id)
    return s.tasks[id]
}

// wherever data changes, say what changed
func (s *Store) SetTaskDone(ctx context.Context, id string, done bool) error {
    s.tasks[id].Done = done
    return invalidator.Invalidate(ctx, "task:"+id, "tasks")
}
```

A key is an opaque string and its granularity is entirely your choice: a row, a
table, a tenant.

`Depend` goes **before** the read, not after. Invalidating a key only reaches
computations already registered against it, so a resolver that reads first and
registers second misses anything that changed in between, and the live query
serves a stale value until some later change to the same key.

`MemorySource` keeps events inside one process, which is all a single-process
server needs. A fleet needs a `Source` that crosses process boundaries, because
a live query on one machine must re-run when another machine changes the data.
The interface is two methods:

```go
type Source interface {
    Publish(ctx context.Context, keys []string) error
    Subscribe(ctx context.Context, deliver func(keys []string)) error
}
```

A Postgres implementation is a thin wrapper over `LISTEN` / `NOTIFY`: `Publish`
issues `NOTIFY` with the keys as payload; `Subscribe` holds a dedicated
connection issuing `LISTEN` and calls `deliver` for each notification. It is not
included here because it would put a database driver in a GraphQL library's
dependency list, which is exactly the over-reach this fork exists to undo.

---

## Exporting the schema

relay-compiler, and most other schema-driven tooling, needs a schema file.

```go
//go:generate go run ./cmd/schema

func main() {
    if err := graphql.WriteSchemaFile(myschema.Build(), "schema.graphql"); err != nil {
        log.Fatal(err)
    }
}
```

Output is deterministic — types, fields, arguments and enum values are all
sorted — so the file can be committed and diffed. It is written atomically, so
a failed build step leaves the previous schema in place rather than a truncated
one.

`example/` does exactly this, and `example/web/` runs relay-compiler against the
result.

---

## Errors

Errors serialise as the specification requires:

```json
{
  "data": null,
  "errors": [
    { "message": "Internal server error", "path": ["tasks", 2, "owner"] }
  ]
}
```

Only errors that are explicitly client-safe keep their message; anything else
becomes "Internal server error", so internal detail cannot leak:

```go
return nil, graphql.NewClientError("no task %q", id)  // the client sees this
return nil, fmt.Errorf("pq: connection refused")      // the client does not
```

A **request error** — a malformed query, a validation failure — omits `data`
entirely. A **field error** keeps `data` present, so a client can use whatever
did resolve.

---

## Batching

`BatchFieldFunc` receives every source object at once, which is how N+1 queries
are avoided:

```go
task.BatchFieldFunc("owner", func(ctx context.Context, tasks map[batch.Index]*Task) (map[batch.Index]*User, error) {
    ids := make([]string, 0, len(tasks))
    for _, task := range tasks {
        ids = append(ids, task.OwnerID)
    }
    users := store.UsersByID(ctx, ids) // one round trip
    ...
})
```

`schemabuilder.Expensive` marks a field whose resolution should be
parallelised.

---

## Development

```
go build ./...
go vet ./...
go test ./...
go test -race ./...
```

Snapshot tests regenerate with `go test ./... -rewriteSnapshots`; read the diff
rather than trusting it.

CI runs build, vet, gofmt, test, the race detector and a `go mod tidy` check on
every push.

---

## Licence

MIT, inherited from thunder. See `LICENSE`.
