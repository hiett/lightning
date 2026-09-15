# lightning

A Go GraphQL server library with **reactive live queries**, built to talk to a
Relay client without adapters.

lightning is a fork of [samsarahq/thunder](https://github.com/samsarahq/thunder),
which Samsara deprecated in February 2023. Thunder had one rare capability:
automatic dependency tracking during execution, re-execution on invalidation,
and JSON diffs pushed to clients over a websocket. That capability is the reason
for the fork. Everything else has been rebuilt against the current GraphQL
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
relay.Node(b, func(ctx context.Context, id string) (*Task, error) {
    return &Task{Key: id, Title: "Write the schema", Done: true}, nil
})

relay.Connection(b.Query(), "tasks", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Task, error) {
    return []*Task{
        {Key: "1", Title: "Write the schema", Done: true},
        {Key: "2", Title: "Make it live"},
    }, nil
})

built := b.MustBuild()
http.Handle("/graphql", graphql.HTTPHandler(built))  // queries and mutations
http.Handle("/graphql/live", graphql.Handler(built)) // the same schema, as live queries
```

The code above is a complete server with a Relay connection, global identifiers,
`node(id:)` and a live query. A field's type and nullability come from the Go
types, so no call site names a type again and no field needs a nullability flag.

The resolvers return literals here; a real one reads from a store that records
what it read, and that record is all the invalidation logic a resolver needs.
When the store later announces that tasks changed, every live query that read
them re-executes and its client is sent the difference. You do not write
subscription wiring.

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

Requires **Go 1.27**. Three packages do almost everything:

```go
import (
    "github.com/hiett/lightning"        // the schema-authoring API
    "github.com/hiett/lightning/relay"  // the Node interface and connections
    "github.com/hiett/lightning/graphql" // the runtime: handlers, SDL, errors
)
```

The `example/` directory is a complete working server: an interface, the Node
interface, a connection, a mutation and a live query. It ships with a Relay app
in `example/web/` that compiles against the exported schema. Run it:

```
cd example && go run ./cmd/server
```

and open <http://localhost:8080> for GraphiQL.

---

## Building a schema

A struct already describes itself: its exported fields become GraphQL fields,
and its tags carry everything else.

```go
type Task struct {
    lightning.Meta `graphql:"Task" description:"A unit of work."`

    Key     string  `graphql:"-"`                                  // not exposed
    Title   string  `description:"What needs doing."`
    Done    bool    `description:"Whether it has been done."`
    Notes   *string `description:"Anything else." deprecated:"Use comments."`
    OwnerID string  `graphql:"-"`
}

type User struct {
    lightning.Meta `description:"A person."`

    Key  string `graphql:"-"`
    Name string `description:"The user's display name."`
}

type Team struct {
    lightning.Meta `description:"A group of people."`

    Key  string `graphql:"-"`
    Name string `description:"The team's display name."`
}

b := lightning.New()
task := lightning.Object[Task](b)
user := lightning.Object[User](b)
team := lightning.Object[Team](b)

task.Field("owner", func(ctx context.Context, t *Task) (*User, error) {
    if t.OwnerID == "u1" {
        return &User{Key: "u1", Name: "Ada"}, nil
    }
    return &User{Key: "u2", Name: "Grace"}, nil
}).Describe("Whoever the task belongs to.")

b.Query().Field("tasks", func(ctx context.Context, _ *lightning.Root) ([]*Task, error) {
    return []*Task{
        {Key: "1", Title: "Write the schema", Done: true, OwnerID: "u1"},
        {Key: "2", Title: "Make it live", OwnerID: "u2"},
    }, nil
})

built := b.MustBuild()
```

The resolver is a Go function:
`func(ctx context.Context, t *Task) (*User, error)`.
Write it inline, as here, or pass an existing method. Its return type names the
GraphQL type, so there is no `Ref` to pass. A root field's parent is
`*lightning.Root`, an empty struct with nothing to read, so root resolvers
discard it as `_`.

The embedded `lightning.Meta` carries the type's tags: `graphql` names the
GraphQL type and `description` documents it. It is optional, and a struct
without one takes its Go name.

The tag vocabulary is `graphql` (a name, or `-` to hide), `description`,
`deprecated`, `default`, `sortable` and `filterable`. A key that looks like one
of these but is not, such as `describe:`, `desc:` or `sort:`, is a build error
naming the field and what was meant.

### Fields

| | |
|---|---|
| `t.Field(name, fn)` | `func(ctx, *T) (R, error)` — the usual one |
| `t.Attr(name, fn)` | `func(*T) R` — a pure accessor |
| `t.FieldArgs(name, fn)` | `func(ctx, *T, A) (R, error)` — with arguments |
| `t.Batch(name, fn)` | `func(ctx, []*T) ([]R, error)` — every parent at once |
| `t.BatchArgs(name, fn)` | the same, with arguments |
| `t.Load(name, key, load)` | fetch by key, deduplicated |

Each returns a `*Field` for chaining: `.Describe`, `.Deprecate`, `.NonNull`,
`.Nullable`, `.Expensive`, `.Sortable`, `.Filterable`, `.UseBatch`, `.Split`,
and `.Meta(key, value)`, which attaches a value for a plugin to read.

The compiler checks the shape of a resolver, so there is no reflection over
`interface{}` and no signature check at `MustBuild` time.

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

`.NonNull()` and `.Nullable()` override this, and are rarely the right tool. An
override changes one field and leaves the Go type alone, so every other field of
that type is still wrong. Change the Go type instead.

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
is 64 bits, so it maps to `Int64` too. Declare a field `int32` if it is a small
number and you want a JSON number.

A Go type can be registered as a scalar of its own:

```go
type Money struct{ Pence int64 }

lightning.Scalar[Money](b, "Money",
    func(m Money) (any, error) { return strconv.FormatInt(m.Pence, 10), nil },
    func(v any) (Money, error) {
        pence, err := strconv.ParseInt(fmt.Sprint(v), 10, 64)
        return Money{Pence: pence}, err
    })
```

A type can also be registered under an existing scalar's name. `relay.GID` is
registered that way, so it travels as an `ID` and arrives at a resolver already
decoded:

```go
// relay.GID is this type, and the plugin registers it with this call.
type GID struct{ Type, Local string }

lightning.ScalarAs[GID](b, "ID",
    func(id GID) (any, error) { return id.Type + ":" + id.Local, nil },
    func(v any) (GID, error) {
        name, local, ok := strings.Cut(fmt.Sprint(v), ":")
        if !ok {
            return GID{}, fmt.Errorf("%v is not a global id", v)
        }
        return GID{Type: name, Local: local}, nil
    })
```

---

## Arguments

An argument struct is a Go struct. Its tags carry the names, documentation and
defaults. The type is inferred from the resolver and is never named at the call
site.

```go
type AddTaskArgs struct {
    Title   string  `description:"What needs doing."`
    OwnerID string  `graphql:"ownerId" description:"Who it belongs to."`
    Rank    int32   `description:"Where it sits in the list." default:"20"`
    Notes   *string `description:"Anything else."`
}

b.Mutation().FieldArgs("addTask", func(ctx context.Context, _ *lightning.Root, args AddTaskArgs) (*Task, error) {
    return &Task{Key: "6", Title: args.Title, Notes: args.Notes, OwnerID: args.OwnerID}, nil
})
```

A pointer field is optional and a value field is required. A `default` makes a
value field optional too. A struct reached through an argument becomes an input
object automatically, named after the Go type.

---

## Enums

```go
type Status int32

const (
    StatusTodo Status = iota
    StatusDone
    StatusArchived
)

status := lightning.Enum(b, "TaskStatus", map[string]Status{
    "TODO":     StatusTodo,
    "DONE":     StatusDone,
    "ARCHIVED": StatusArchived,
}).Describe("How far along a task is.")

status.Value("TODO").Describe("Nobody has started it.")
status.Value("ARCHIVED").Deprecate("Tasks are deleted rather than archived.")

task.Attr("status", func(t *Task) Status {
    if t.Done {
        return StatusDone
    }
    return StatusTodo
})
```

A field returning `Status` then has that enum type. No further declaration is
needed.

---

## Interfaces and unions

**A GraphQL interface is a Go interface.**

```go
type Actor interface{ DisplayName() string }

func (u *User) DisplayName() string { return u.Name }
func (t *Team) DisplayName() string { return t.Name }

actor := lightning.Interface[Actor](b).Describe("Whoever a task belongs to.")
actor.Field("displayName", func(ctx context.Context, a Actor) (string, error) {
    return a.DisplayName(), nil
})

lightning.Implements(actor, user, func(u *User) Actor { return u })
lightning.Implements(actor, team, func(t *Team) Actor { return t })
```

The witness function `func(u *User) Actor { return u }` compiles only if
`*User` satisfies `Actor`, so the compiler checks membership and names any
missing method. A resolver returns the interface value, as in the `owner` field
from **Building a schema**, declared over `Actor` instead of `*User`:

```go
task.Field("owner", func(ctx context.Context, t *Task) (Actor, error) {
    if t.OwnerID == "t1" {
        return &Team{Key: t.OwnerID, Name: "Platform"}, nil
    }
    return &User{Key: t.OwnerID, Name: "Ada"}, nil
})
```

The concrete Go type decides `__typename`. There is no marker struct or wrapper
type to fill in, so the type name cannot disagree with the value a resolver
returned.

A Go type that satisfies `Actor` becomes a member of the GraphQL interface only
through a `lightning.Implements` call.

A member that does not declare an interface field inherits it: the interface's
resolver takes the interface value, which every member satisfies. A member that
declares it with a different type is a build error naming both.

A **union** is the same over a Go interface with no methods:

```go
type Gateway interface{ isGateway() }

type Vehicle struct {
    lightning.Meta `description:"Something that moves."`

    Name string `description:"What it is called."`
}

func (v *Vehicle) isGateway() {}

vehicle := lightning.Object[Vehicle](b)
gateway := lightning.Union[Gateway](b)
lightning.Implements(gateway, vehicle, func(v *Vehicle) Gateway { return v })
```

---

## Relay

Relay lives in `lightning/relay`. It is a plugin with no privileged access to
the core.

```go
b := lightning.New(relay.Plugin())
lightning.Object[Task](b)
```

### The Node interface

```go
relay.Node(b, func(ctx context.Context, id string) (*Task, error) {
    return &Task{Key: id, Title: "Write the schema", Done: true, OwnerID: "u1"}, nil
}) // uses (*Task).NodeID()
```

One call gives the type an `id` field carrying its **global** identifier, makes
it a member of the `Node` interface, and adds `node(id: ID!): Node` and
`nodes(ids: [ID!]!): [Node]!` to the query root. It supplies the key every
cursor over the type is built from, and the `__key` the live-query diff uses to
line up elements of a list between executions. The type must have been declared
with `lightning.Object` first, and at least one node type must be reachable from
the query root for the `Node` interface to appear at all. The `tasks` connection
below is what reaches `Task`.

For a type with no `NodeID` method, name the identifier explicitly:

```go
relay.NodeFunc(b, func(t *Task) string { return t.Key }, func(ctx context.Context, id string) (*Task, error) {
    return &Task{Key: id, Title: "Write the schema", OwnerID: "u1"}, nil
})
```

The default identifier is base64 of `TypeName:localID`. It is obfuscation
rather than secrecy: anyone can decode it. Replace the codec if identifiers must
not be guessable or forgeable. A `relay.Codec` is a pair of methods you write,
`Encode(typeName, localID)` and `Decode(globalID)`. `mySignedCodec` here is
yours to supply:

```go
lightning.New(relay.Plugin(relay.WithCodec(mySignedCodec{})))
```

An argument typed `relay.ID[Task]` is an `ID` on the wire and a decoded local
identifier in Go, and an identifier naming anything but a `Task` is refused
while the query is prepared, before anything runs:

```go
type SetDoneArgs struct {
    ID   relay.ID[Task] `graphql:"id"`
    Done bool
}

b.Mutation().FieldArgs("setDone", func(ctx context.Context, _ *lightning.Root, args SetDoneArgs) (*Task, error) {
    // args.ID.Local is the local identifier, already known to name a Task.
    return &Task{Key: args.ID.Local, Title: "Write the schema", Done: args.Done, OwnerID: "u1"}, nil
})
```

`relay.ID[T]` is the normal case. Use `relay.GID` only where a field takes an
identifier of any type: `node(id:)` is one, and so is a field whose owner may
be a `User` or a `Team`. A `relay.GID` argument arrives decoded and reports
both halves:

```go
type AddTaskArgs struct {
    OwnerID relay.GID `graphql:"ownerId"`
}

b.Mutation().FieldArgs("addTask", func(ctx context.Context, _ *lightning.Root, args AddTaskArgs) (*Task, error) {
    // args.OwnerID.Type is "User" or "Team"; Local is the identifier within it.
    return &Task{Key: "6", Title: "Write the schema", OwnerID: args.OwnerID.Local}, nil
})
```

### Connections

```go
relay.Connection(b.Query(), "tasks", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Task, error) {
    return []*Task{
        {Key: "1", Title: "Write the schema", Done: true, OwnerID: "u1"},
        {Key: "2", Title: "Make it live", OwnerID: "u2"},
    }, nil
})
```

The resolver returns the whole list and the plugin pages it: `TaskConnection`,
`TaskEdge`, cursors, `pageInfo`, `totalCount`, and the `first` / `last` /
`before` / `after` arguments. The element type must be a registered node,
because its identifier is what the cursors are built from.

Cursors are **key-based**. A cursor is base64 of the key of the item it points
at, so inserting earlier in the list does not move it. This library's headline
feature is live queries over changing lists, and an offset cursor would be
wrong for that.

`relay.ConnectionArgs` adds arguments of your own alongside the pagination ones.

Two extensions beyond the specification, both of which Relay ignores:
`totalCount`, and `pageInfo.pages` for page-number pagination.

### Sorting and searching

Sortability and filterability are declared on the field:

```go
Title string `description:"What needs doing." sortable:"true" filterable:"true"`
```

or, for a computed field, `.Sortable()` and `.Filterable()`. Every connection
over the type then takes `sortBy`, `sortOrder`, `filterText` and
`filterTextFields`, and applies them.

A connection over a type with nothing sortable **has no `sortBy` argument at
all**, and a type with nothing filterable has no `filterText`.

### Paging it yourself

Use `relay.ManualConnection` when the list is already paged in the database.
The plugin does not check what the resolver returns, so the size of the page and
what is in it are yours to get right. `relay.Page` is the page the client asked
for: `First` and `Last` (`*int32`), `After` and `Before` (`*string`), and
`SortBy`, `SortOrder`, `FilterText` and `FilterTextFields`.

```go
relay.ManualConnection(b.Query(), "tasks", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Task, relay.PageResult, error) {
    // A database would push p.SortBy and p.FilterText down into the query; this
    // pages a slice, and forwards only: p.Last and p.Before are left out.
    all := []*Task{
        {Key: "1", Title: "Write the schema", Done: true, OwnerID: "u1"},
        {Key: "2", Title: "Make it live", OwnerID: "u2"},
        {Key: "3", Title: "Write the README", OwnerID: "u1"},
    }
    from := 0
    if p.After != nil {
        key, _ := base64.StdEncoding.DecodeString(*p.After) // base64 of the item's key
        for i, t := range all {
            if t.Key == string(key) {
                from = i + 1
            }
        }
    }
    to := len(all)
    if p.First != nil && from+int(*p.First) < to {
        to = from + int(*p.First)
    }
    return all[from:to], relay.PageResult{
        TotalCount:      int64(len(all)),
        HasNextPage:     to < len(all),
        HasPreviousPage: from > 0,
    }, nil
})
```

The resolver returns exactly the items in the page. The plugin adds cursors and
nothing else.

---

## Batching

A batch field receives every parent the executor is about to ask about. This
avoids N+1 queries:

```go
// usersByID stands in for the one query a store would make: the users come back
// in the order the identifiers went in.
usersByID := func(ctx context.Context, ids []string) ([]*User, error) {
    names := map[string]string{"u1": "Ada", "u2": "Grace"}
    users := make([]*User, len(ids))
    for i, id := range ids {
        users[i] = &User{Key: id, Name: names[id]}
    }
    return users, nil
}

task.Batch("owner", func(ctx context.Context, tasks []*Task) ([]*User, error) {
    ids := make([]string, len(tasks))
    for i, t := range tasks {
        ids[i] = t.OwnerID
    }
    return usersByID(ctx, ids) // one round trip, in order
})
```

Results line up with parents by position. The field's GraphQL type comes from
one element of the slice, so the `[]*User` above gives a nullable `User`, the
same as any other field returning `*User`.

Batching is nearly always a lookup by a key on the parent, and `Load` covers
that case on its own, deduplicating the keys and taking the same `usersByID`
unchanged:

```go
owner := task.Load("owner", func(t *Task) string { return t.OwnerID }, usersByID)
```

Three tasks with the same owner cost one lookup.

`.UseBatch(fn)` decides per request whether to batch. With batching off, the
same resolver runs once per parent, so you write it once either way.
`.Expensive()` marks a field worth running in parallel with its siblings, and
`.Split(fn)` sets how many ways to divide a batch:

```go
owner.UseBatch(func(ctx context.Context) bool { return true }).
    Split(func(ctx context.Context, parents int) int { return 1 + parents/50 })
```

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

`FieldPlugin` sees every field as it is built and may wrap its resolver.
Authorisation, tracing and metrics are added that way, without the field's
author naming them. `relay` is itself a plugin, and it uses nothing beyond
these interfaces.

`lightning.FieldInfo` is the declared field as a plugin sees it, read-only:
`Name()`, `GoResult()`, `Sortable()`, `Filterable()` and `Meta(key)`. `Meta(key)`
returns whatever `.Meta(key, value)` attached, and reports whether anything
did.

Here is one written out. It wraps the fields that opt in and leaves the rest
alone:

```go
type adminKey struct{}

type adminOnly struct{}

func (adminOnly) PluginName() string { return "adminOnly" }

func (adminOnly) Field(_ *lightning.Builder, typeName string, info lightning.FieldInfo, field *graphql.Field) error {
    opted, ok := info.Meta("admin.only")
    if !ok || opted != true {
        return nil
    }
    admit := func(ctx context.Context) error {
        if ctx.Value(adminKey{}) == true {
            return nil
        }
        return fmt.Errorf("%s.%s is for administrators", typeName, info.Name())
    }
    one, many := field.Resolve, field.BatchResolver
    field.Resolve = func(ctx context.Context, source, args any, sel *graphql.SelectionSet) (any, error) {
        if err := admit(ctx); err != nil {
            return nil, err
        }
        return one(ctx, source, args, sel)
    }
    if many == nil {
        return nil
    }
    // A batch field resolves one parent or many, decided per request, so
    // wrapping one resolver and not the other leaves a way in.
    field.BatchResolver = func(ctx context.Context, sources []any, args any, sel *graphql.SelectionSet) ([]any, error) {
        if err := admit(ctx); err != nil {
            return nil, err
        }
        return many(ctx, sources, args, sel)
    }
    return nil
}
```

Install it on the builder. A field then opts in under the key the plugin looks
for, without naming the plugin itself:

```go
b := lightning.New(adminOnly{})

user := lightning.Object[User](b)
user.Attr("email", func(u *User) string { return u.Key + "@example.org" }).
    Meta("admin.only", true)
```

---

## Subscriptions and live queries

There are two ways to push data, and which one you want depends on who is
connecting.

### `graphql-transport-ws`

The interoperable one. Register subscription roots and serve the protocol any
standard client speaks:

```go
relay.Connection(b.Subscription(), "tasks", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Task, error) {
    return []*Task{
        {Key: "1", Title: "Write the schema", Done: true, OwnerID: "u1"},
        {Key: "2", Title: "Make it live", OwnerID: "u2"},
    }, nil
})

http.Handle("/graphql/ws", graphql.TransportWSHandler(built))
```

A subscription here is a **live query**: the operation is executed like a
query, re-executed whenever a resource it read is invalidated, and the complete
result is sent each time. Queries and mutations work over the same socket.

Authentication hooks into `connection_init`:

```go
type userKey struct{}

graphql.TransportWSHandler(built,
    graphql.WithTransportWSConnectionInit(func(ctx context.Context, payload json.RawMessage) (context.Context, error) {
        var init struct {
            User string `json:"user"`
        }
        if err := json.Unmarshal(payload, &init); err != nil {
            // returning an error closes the connection with code 4401
            return nil, err
        }
        return context.WithValue(ctx, userKey{}, init.User), nil
    }))
```

### lightning's diff protocol

The efficient one, and the reason for the fork. `graphql.Handler(built)` serves
a websocket that pushes a **JSON diff** of what changed instead of the whole
payload. A list of a thousand rows where one field changed is a few dozen
bytes.

A live query over this protocol is declared as a `subscription`, because that is
the operation a Relay client routes to its subscribe function. The server
validates the document against the subscription root and then executes the
selection set against the query root, so the field has to be registered on both:

```go
tasks := func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Task, error) {
    return []*Task{
        {Key: "1", Title: "Write the schema", Done: true, OwnerID: "u1"},
        {Key: "2", Title: "Make it live", OwnerID: "u2"},
    }, nil
}

relay.Connection(b.Query(), "tasks", tasks)
relay.Connection(b.Subscription(), "tasks", tasks)
```

Registered on the subscription root alone, the document validates and then fails
with `unknown field "tasks"`.

The TypeScript package in `js/` is a Relay network layer for it. It applies each
diff to the payload it is holding and hands Relay the complete result, so Relay's
store never sees the diffs.

```ts
import { createLightningNetwork } from "@hiett/lightning-relay";

const environment = new Environment({
  network: createLightningNetwork({ url: "ws://localhost:8080/graphql/live" }),
  store: new Store(new RecordSource()),
});
```

---

## How live queries work

Everything else in lightning is standard GraphQL. Live queries are the
exception.

**1. Execution records what it read.** While a resolver runs, it can call
`reactive.AddDependency(ctx, resource, dep)` to register the resource a value
came from. The `reactive` package keeps a
dependency graph of which computations read which resources.

**2. Something invalidates a resource.** `resource.Strobe()` marks every
computation that read it as stale.

**3. The rerunner re-executes.** A `reactive.Rerunner` wrapping a query
re-runs it, debounced by a minimum interval, and produces a new result.

**4. The difference is pushed.** For the diff protocol, the new result is
diffed against the previous one and only the difference is sent.

Steps 3 and 4 are lightning's. Steps 1 and 2 are where your code goes, and the
`invalidation` package is what you call for both. It wraps `reactive` so that
you work in keys rather than resources, and it leaves the source of change
events to you:

```go
invalidator := invalidation.New(invalidation.NewMemorySource())
go invalidator.Run(ctx)

tasks := map[string]*Task{
    "1": {Key: "1", Title: "Write the schema", Done: true, OwnerID: "u1"},
    "2": {Key: "2", Title: "Make it live", OwnerID: "u2"},
}

// in a resolver, say what you are about to read — before reading it
b.Query().Field("tasks", func(ctx context.Context, _ *lightning.Root) ([]*Task, error) {
    invalidator.Depend(ctx, "tasks")
    return []*Task{tasks["1"], tasks["2"]}, nil
})

b.Query().FieldArgs("task", func(ctx context.Context, _ *lightning.Root, args struct{ ID string }) (*Task, error) {
    invalidator.Depend(ctx, "task:"+args.ID)
    return tasks[args.ID], nil
})

// wherever data changes, say what changed
b.Mutation().FieldArgs("markDone", func(ctx context.Context, _ *lightning.Root, args struct{ ID string }) (*Task, error) {
    found := tasks[args.ID]
    found.Done = true
    return found, invalidator.Invalidate(ctx, "task:"+args.ID, "tasks")
})
```

A key is an opaque string. You choose its granularity: a row, a table, a tenant.

`Depend` goes **before** the read. Invalidating a key only reaches computations
already registered against it, so a resolver that reads first and registers
second misses anything that changed in between, and the live query serves a
stale value until the next change to that key.

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

A Postgres implementation is a thin wrapper over `LISTEN` / `NOTIFY`. `Publish`
issues `NOTIFY` with the keys as payload. `Subscribe` holds a dedicated
connection issuing `LISTEN` and calls `deliver` for each notification. That
implementation is not shipped here, because it would put a database driver in a
GraphQL library's dependency list.

---

## Exporting the schema

relay-compiler, and most other schema-driven tooling, needs a schema file.

```go
// Command schema writes schema.graphql from the Go schema.
package main

import (
    "log"

    "example.com/tasks/schema"
    "github.com/hiett/lightning/graphql"
)

func main() {
    if err := graphql.WriteSchemaFile(schema.Build(), "schema.graphql"); err != nil {
        log.Fatal(err)
    }
}
```

`schema.Build` is your own build function: `func Build() *graphql.Schema`,
ending in `b.MustBuild()`. The `//go:generate go run ./cmd/schema` directive goes
in the package the schema file sits in, because `go generate` runs a directive
from its own directory.

Output is deterministic. Types, fields, arguments and enum values are sorted, so
the file can be committed and diffed. It is written atomically. A failed build
step leaves the previous schema in place instead of a truncated file.

`example/` does this, and `example/web/` runs relay-compiler against the result.

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

Only errors marked client-safe keep their message. Anything else becomes
"Internal server error", so internal detail cannot leak:

```go
return nil, graphql.NewClientError("no task %q", id)  // the client sees this
return nil, fmt.Errorf("pq: connection refused")      // the client does not
```

A **request error** (a malformed query, a validation failure) omits the `data`
key. A **field error** keeps `data` present, so a client can use whatever did
resolve.

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

The module requires **Go 1.27** for generic methods: they let a field's type be
inferred from its resolver rather than named again at the call site.

CI runs build, vet, gofmt, test, the race detector and a `go mod tidy` check on
every push.

---

## Licence

MIT, inherited from thunder. See `LICENSE`.
