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
type Task struct {
    lightning.Meta `description:"A unit of work."`

    Key   string `graphql:"-"`
    Title string `description:"What needs doing."`
    Done  bool   `description:"Whether it has been done."`
}

func (t *Task) NodeID() string { return t.Key }

b := lightning.New(relay.Plugin())
lightning.Object[Task](b)
relay.Node(b, store.Task)

relay.Connection(b.Query(), "tasks", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Task, error) {
    return store.Tasks(ctx) // store.Tasks records a dependency
})

http.Handle("/graphql", graphql.HTTPHandler(b.MustBuild()))
```

That is a complete server with a Relay connection, global identifiers, `node(id:)`
and a live query. There is no type argument at any call site, no nullability
flag, and nothing that names a field twice: the Go types say it all.

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
| Authoring | reflection over `interface{}` resolvers | generics; a wrong resolver is a **compile** error |

`DECISIONS.md` records why each of those went the way it did.

---

## Getting started

```
go get github.com/hiett/lightning
```

Requires **Go 1.27**. Two packages do almost everything:

```go
import (
    "github.com/hiett/lightning"        // the schema-authoring API
    "github.com/hiett/lightning/relay"  // the Node interface and connections
    "github.com/hiett/lightning/graphql" // the runtime: handlers, SDL, errors
)
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

A struct is already a complete description of itself: its exported fields become
GraphQL fields, and its tags carry everything else. Nothing is named twice.

```go
type Task struct {
    lightning.Meta `graphql:"Task" description:"A unit of work."`

    Key   string  `graphql:"-"`                                  // not exposed
    Title string  `description:"What needs doing."`
    Done  bool    `description:"Whether it has been done."`
    Notes *string `description:"Anything else." deprecated:"Use comments."`
}

b := lightning.New()
task := lightning.Object[Task](b)

task.Field("owner", store.Owner).Describe("Whoever the task belongs to.")

b.Query().Field("tasks", func(ctx context.Context, _ *lightning.Root) ([]*Task, error) {
    return store.Tasks(ctx)
})

built := b.MustBuild()
```

`store.Owner` is an ordinary method — `func(ctx context.Context, t *Task) (Actor, error)` —
handed over as it is. Its return type names the GraphQL type, so there is no
`Ref` to pass and nothing to keep in step.

The tag vocabulary is `graphql` (a name, or `-` to hide), `description`,
`deprecated`, `default`, `sortable` and `filterable`. A key that looks like one
of these but is not — `describe:`, `desc:`, `sort:` — is a build error naming the
field and what was meant, because a misspelled documentation tag documents
nothing and says so nowhere.

### Fields

| | |
|---|---|
| `t.Field(name, fn)` | `func(ctx, *T) (R, error)` — the usual one |
| `t.Attr(name, fn)` | `func(*T) R` — a pure accessor, no ceremony |
| `t.FieldArgs(name, fn)` | `func(ctx, *T, A) (R, error)` — with arguments |
| `t.Batch(name, fn)` | `func(ctx, []*T) ([]R, error)` — every parent at once |
| `t.BatchArgs(name, fn)` | the same, with arguments |
| `t.Load(name, key, load)` | fetch by key, deduplicated |

Each returns a `*Field` for chaining: `.Describe`, `.Deprecate`, `.NonNull`,
`.Nullable`, `.Expensive`, `.Sortable`, `.Filterable`, `.UseBatch`, `.Split`.

A resolver of the wrong shape does not compile. There is no reflection over
`interface{}` and no build-time signature check, because the compiler has
already done it.

### Nullability comes from the Go type

| Go | GraphQL |
|---|---|
| `string` | `String!` |
| `*string` | `String` |
| `[]string` | `[String!]!` |
| `[]*string` | `[String]!` |
| `*[]string` | `[String!]` |
| `*Task` | `Task` |
| `Task` | `Task!` |
| `Actor` (a Go interface) | `Actor` |

An interface value is nullable for the same reason a pointer is: it can be nil.

`.NonNull()` and `.Nullable()` override this, and are rarely the right tool: when
the Go type is wrong, changing the Go type says the same thing to every reader
rather than to one field.

### Scalars

| Go | GraphQL | Notes |
|---|---|---|
| `string` | `String` | |
| `bool` | `Boolean` | |
| `int8`, `int16`, `int32`, `uint8`, `uint16` | `Int` | everything that fits in 32 signed bits |
| `int`, `int64`, `uint`, `uint32`, `uint64` | `Int64` | **serialised as a decimal string** |
| `float32`, `float64` | `Float` | |
| `lightning.ID` | `ID` | |
| `time.Time` | `Time` | RFC 3339 |
| `[]byte` | `Bytes` | base64 |
| anything with `MarshalText` | `String` | |

`Int64` is a string on the wire because a GraphQL `Int` is 32-bit and a JSON
number loses precision above 2⁵³ once a JavaScript client parses it. Go's `int`
is 64 bits, so it maps to `Int64` too; declare a field `int32` if it genuinely
is a small number and you want a JSON number.

A Go type can be registered as a scalar of its own:

```go
lightning.Scalar[Money](b, "Money", encode, decode)
```

or under an existing scalar's name, which is how `relay.GID` travels as an `ID`
while arriving at a resolver already decoded:

```go
lightning.ScalarAs[GID](b, "ID", encode, decode)
```

---

## Arguments

An argument struct is an ordinary Go struct. Its tags carry the names,
documentation and defaults, and the type is inferred from the resolver — it is
never named at the call site.

```go
type AddTaskArgs struct {
    Title   string    `description:"What needs doing."`
    OwnerID relay.GID `graphql:"ownerId" description:"Who it belongs to."`
    Limit   *int32    `description:"How many to return." default:"20"`
}

b.Mutation().FieldArgs("addTask", func(ctx context.Context, _ *lightning.Root, args AddTaskArgs) (*Task, error) {
    return store.AddTask(ctx, args.Title, args.OwnerID.Local)
})
```

A pointer field is optional and a value field is required; a `default` makes a
value field optional too. A struct reached through an argument becomes an input
object automatically, named after the Go type.

---

## Enums

```go
type Status int32

const (
    StatusTodo Status = iota
    StatusDone
)

lightning.Enum(b, "TaskStatus", map[string]Status{
    "TODO": StatusTodo,
    "DONE": StatusDone,
}).Describe("How far along a task is.")
```

A field returning `Status` then has that enum type, with nothing further to say.
Individual values are documented with `.Value("TODO").Describe(...)` and
deprecated with `.Deprecate(...)`.

---

## Interfaces and unions

**A GraphQL interface is a Go interface.**

```go
type Actor interface{ DisplayName() string }

actor := lightning.Interface[Actor](b).Describe("Whoever a task belongs to.")
actor.Field("displayName", func(ctx context.Context, a Actor) (string, error) {
    return a.DisplayName(), nil
})

lightning.Implements(actor, user, func(u *User) Actor { return u })
lightning.Implements(actor, team, func(t *Team) Actor { return t })
```

The witness function is the point: `func(u *User) Actor { return u }` compiles
only if `*User` satisfies `Actor`, so membership is checked by the compiler and
a missing method is named by it. A resolver returns the interface value:

```go
task.Field("owner", func(ctx context.Context, t *Task) (Actor, error) {
    return store.Owner(ctx, t.OwnerID)
})
```

The concrete Go type decides `__typename`. There is no marker struct and no
one-hot wrapper, so there is no way to build an invalid one.

Membership is explicit rather than inferred from which types happen to satisfy
the interface. Satisfying an interface by accident is ordinary Go; joining a
GraphQL interface by accident is not.

A member that does not declare an interface field inherits it — the interface's
resolver takes the interface value, which every member satisfies. A member that
declares it with a different type is a build error naming both.

A **union** is the same over a Go interface with no methods:

```go
type Gateway interface{ isGateway() }

gateway := lightning.Union[Gateway](b)
lightning.Implements(gateway, vehicle, func(v *Vehicle) Gateway { return v })
```

---

## Relay

Relay lives in `lightning/relay`, a plugin with no privileged access to the
core: everything it does, anything else can do.

```go
b := lightning.New(relay.Plugin())
```

### The Node interface

```go
relay.Node(b, store.Task)   // uses (*Task).NodeID()
```

One line gives the type an `id` field carrying its **global** identifier, makes
it a member of the `Node` interface, adds `node(id: ID!): Node` and
`nodes(ids: [ID!]!): [Node]!` to the query root, supplies the key every cursor
over the type is built from, and supplies the `__key` the live-query diff lines
list elements up by.

For a type with no `NodeID` method, name the identifier explicitly:

```go
relay.NodeFunc(b, func(t *Task) string { return t.Key }, store.Task)
```

The default identifier is base64 of `TypeName:localID`. That is obfuscation, not
secrecy — anyone can decode it. Replace the codec if identifiers must not be
guessable or forgeable:

```go
lightning.New(relay.Plugin(relay.WithCodec(mySignedCodec{})))
```

An argument typed `relay.ID[Task]` is an `ID` on the wire and a decoded local
identifier in Go, and an identifier naming anything but a `Task` is refused
while the query is prepared — before anything runs:

```go
type SetDoneArgs struct {
    ID   relay.ID[Task] `graphql:"id"`
    Done bool
}

// args.ID.Local is the local identifier, already known to name a Task.
```

That is the `localID` helper every project writes by hand, with the check the
hand-written one usually forgets. Where a field genuinely takes any
identifier — `node(id:)` does, and so does a field whose owner may be a `User`
or a `Team` — `relay.GID` accepts one of any type and reports both halves:

```go
type AddTaskArgs struct {
    OwnerID relay.GID `graphql:"ownerId"`
}
// args.OwnerID.Type is "User" or "Team"; args.OwnerID.Local is the identifier
```

### Connections

```go
relay.Connection(b.Query(), "tasks", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Task, error) {
    return store.Tasks(ctx)
})
```

The resolver returns the whole list and the plugin pages it: `TaskConnection`,
`TaskEdge`, cursors, `pageInfo`, `totalCount`, and the `first` / `last` /
`before` / `after` arguments. The element type must be a registered node,
because its identifier is what the cursors are built from.

Cursors are **key-based**, not offsets: a cursor names the item it points at, so
inserting earlier in the list does not move it. In a library whose headline
feature is live queries over changing lists, an offset cursor would be wrong in
a way it would not be elsewhere.

`relay.ConnectionArgs` adds arguments of your own alongside the pagination ones.

Two extensions beyond the specification, both of which Relay ignores:
`totalCount`, and `pageInfo.pages` for page-number pagination.

### Sorting and searching

Whether a title can be searched is a fact about the title, so it is written on
the title:

```go
Title string `description:"What needs doing." sortable:"true" filterable:"true"`
```

or, for a computed field, `.Sortable()` and `.Filterable()`. Every connection
over the type then takes `sortBy`, `sortOrder`, `filterText` and
`filterTextFields`, and applies them.

A connection over a type with nothing sortable **has no `sortBy` argument at
all**. An argument that cannot do anything is not offered.

### Paging it yourself

When the list is paged in the database, say so and the plugin believes you:

```go
relay.ManualConnection(q, "tasks", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Task, relay.PageResult, error) {
    rows, total, more := store.PageTasks(ctx, p)
    return rows, relay.PageResult{TotalCount: total, HasNextPage: more}, nil
})
```

The resolver is given the page the client asked for — including the sort and
filter arguments, so it can push them down — and returns exactly the items in
it. The plugin adds cursors and nothing else.

---

## Batching

A batch field is handed every parent the executor is about to ask, which is how
N+1 queries are avoided:

```go
task.Batch("owner", func(ctx context.Context, tasks []*Task) ([]*User, error) {
    ids := make([]string, len(tasks))
    for i, task := range tasks {
        ids[i] = task.OwnerID
    }
    return store.UsersByID(ctx, ids) // one round trip, in order
})
```

Results line up with parents by position. `R` is the type of one result, so the
field's GraphQL type and nullability come from the Go type as they do anywhere
else.

For the case batching is nearly always for — a lookup by a key on the parent —
`Load` does the whole thing, deduplicating the keys:

```go
task.Load("owner", func(t *Task) string { return t.OwnerID }, store.UsersByID)
```

Three tasks with the same owner cost one lookup.

`.UseBatch(fn)` decides per request whether to batch; with batching off the same
resolver runs once per parent, so there is nothing written twice and nothing
that can drift apart. `.Expensive()` marks a field worth running in parallel
with its siblings, and `.Split(fn)` says how many ways to divide a batch.

---

## Plugins

A plugin is a value installed on a builder. It implements only the capabilities
it needs:

```go
type Plugin interface{ PluginName() string }

type InstallPlugin interface     { Plugin; Install(*lightning.Builder) error }
type FieldPlugin interface       { Plugin; Field(*lightning.Builder, string, lightning.FieldInfo, *graphql.Field) error }
type BeforeBuildPlugin interface { Plugin; BeforeBuild(*lightning.Builder) error }
type AfterBuildPlugin interface  { Plugin; AfterBuild(*lightning.Builder, *graphql.Schema) error }
```

`FieldPlugin` sees every field as it is built and may wrap its resolver, which
is how authorisation, tracing or metrics are added without the field's author
naming them. `relay` is an ordinary plugin written against this seam and nothing
else.

---

## Subscriptions and live queries

There are two ways to push data, and they are not alternatives so much as
different audiences.

### `graphql-transport-ws`

The interoperable one. Register subscription roots and serve the protocol any
standard client speaks:

```go
relay.Connection(b.Subscription(), "tasks", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Task, error) {
    return store.Tasks(ctx)
})

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

## Development

```
go build ./...
go vet ./...
go test ./...
go test -race ./...
```

Snapshot tests regenerate with `go test ./... -rewriteSnapshots`; read the diff
rather than trusting it.

The module requires **Go 1.27**, for generic methods: they are what lets a
field's type be inferred from its resolver rather than named again at the call
site.

CI runs build, vet, gofmt, test, the race detector and a `go mod tidy` check on
every push.

---

## Licence

MIT, inherited from thunder. See `LICENSE`.
