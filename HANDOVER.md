# Handover

Two refactors are done. The first brought thunder up to the modern GraphQL
specification; the second replaced the schema-authoring layer with a
generics-based API. This is what the repository is now, what is verified, and
what is left for a human to decide.

---

## 1. What the repository is now

A Go GraphQL server library, module `github.com/hiett/lightning`, requiring
**Go 1.27**, with:

- a schema-authoring API where a resolver of the wrong shape is a **compile**
  error, not a build-time one;
- a spec-compliant parser and full query validation before execution;
- the specification's scalars, interfaces and unions, both backed by Go
  interfaces;
- Relay as a **plugin** — `Node`, global identifiers, connections, sorting and
  searching — with no privileged access to the core;
- SDL export, so a client's tooling reads a generated `schema.graphql`;
- subscriptions over `graphql-transport-ws`, **and** thunder's diff-pushing
  live-query protocol with a TypeScript Relay network layer for it;
- an invalidation adapter that connects change events to the reactive core;
- an example server and a Relay app that exercise all of it.

Six direct dependencies, all current. No database. No `replace` directives.

### Layout

| | |
|---|---|
| `.` (`lightning`) | the schema-authoring API: builder, types, fields, arguments, enums, scalars, the plugin seam |
| `relay/` | the Relay plugin: `Node`, global identifiers, connections, sorting, searching |
| `graphql/` | the runtime: parser, validator, executor, SDL printer, HTTP and websocket handlers |
| `graphql/introspection/` | the introspection schema, itself built with the authoring API |
| `graphql/graphiql/` | the GraphiQL IDE, embedded |
| `reactive/` | dependency tracking and re-execution — the live-query core |
| `invalidation/` | change events → reactive invalidations |
| `diff/`, `merge/` | the JSON diff format live queries push |
| `batch/`, `concurrencylimiter/`, `internal/` | execution support |
| `js/` | `@hiett/lightning-relay`, the Relay network layer |
| `example/` | a working server, and a Relay app in `example/web/` |

### Deleted

By the first refactor: `livesql/`, `sqlgen/`, `federation/`,
`federationexample/`, `thunderpb/`, `client/`, `doc/`, `tools/`, `ci/`,
`internal/integrationtest/`, `internal/proto/`, `internal/testfixtures/`,
`internal/fields/`.

By the second: **`graphql/schemabuilder/`** and the dead federation surface it
carried — `FetchObjectFromKeys`, `RootObjectType`, `ShadowObjectType`,
`ServiceName`, `NewSchemaWithName`, `buildFederatedFunction`,
`buildShadowObjectFederationFunction`, `graphql.Field.FederatedKey` — plus
`internal/filter`, whose behaviour now lives in `relay`.

All recoverable from git history; nothing in the current tree refers to them.

---

## 2. Verification

Every one of these was run, not reasoned about.

```
go build ./...       clean
go vet ./...         silent
gofmt -l .           empty
go test ./...        all packages pass
go test -race ./...  all packages pass
```

No test requires a database, or any other external process.

**The example's exported schema is essentially unchanged.** `DECISIONS.md` D42
lists every line that differs from the pre-refactor `schema.graphql` and why:
added descriptions, one dropped argument that did nothing, one removed field
that no longer has to exist, and an added enum. No other type, field, argument
or nullability change.

**relay-compiler runs clean.** `example/web/` contains a real Relay app —
`usePaginationFragment` over the generated connection, `@refetchable` on a
fragment, `@appendNode` into the connection after a mutation, a subscription —
and `npx relay-compiler` compiles all nine documents against
`example/schema.graphql` with zero errors.

**The Relay app was run, not described.** `example/web/e2e/app.test.tsx` mounts
the real components in jsdom against a running example server, over the
live-query websocket, with nothing mocked. All four acceptance behaviours pass,
including a live query pushed a change made over a *different* connection.

```
cd example && go run ./cmd/server      # terminal one
cd example/web && npm run e2e          # terminal two
```

**Live queries are proven in Go too**, over `graphql-transport-ws`, by
`example/schema/schema_test.go:TestExampleLiveQuery`.

`js/` typechecks under `strict` and its **154 tests** pass.

**CI enforces the chain.** It regenerates `schema.graphql` from the Go schema
and re-runs relay-compiler, failing if either the exported schema or the
generated artifacts are stale. A schema change that was never exported cannot
pass.

`BLOCKERS.md` does not exist. Nothing was blocked.

---

## 3. What the authoring API looks like

The whole of it, in the order you meet it:

```go
type Task struct {
    lightning.Meta `graphql:"Task" description:"A unit of work."`

    Key   string `graphql:"-"`
    Title string `description:"What needs doing." sortable:"true" filterable:"true"`
    Done  bool   `description:"Whether it has been done."`
}

func (t *Task) NodeID() string { return t.Key }

b := lightning.New(relay.Plugin())

task := lightning.Object[Task](b)
relay.Node(b, store.Task)

task.Field("owner", store.Owner).Describe("Whoever the task belongs to.")

relay.Connection(b.Query(), "tasks", store.PageTasks).Describe("Every task.")

return b.MustBuild()
```

What is absent is the point: no type argument at a call site, no nullability
flag, no `ArgDescription`, no `Key("key")`, no marker struct, no `localID`
helper, no second place for anything to disagree with the Go type.

`README.md` covers each piece. `example/schema/` is 95 lines and exercises an
interface, three node types, a connection, an enum, two mutations and a live
query.

---

## 4. Decisions a human should look at

`DECISIONS.md` has all forty-seven in full. These are the ones with consequences.

From the first refactor:

**`Int64` is a string on the wire** (D11). Go's `int` maps to `Int64`, so an
`Age int` field serialises as `"5"`, not `5`. A field that genuinely is a small
number should be declared `int32`. This is still the change most likely to
surprise someone writing their first schema.

**Global identifiers are base64, and guessable** (D17). That is the Relay
convention and it is obfuscation, not security. Supply a signing `relay.Codec`
if identifiers must not be forgeable.

**No Postgres invalidation source is shipped.** `invalidation` ships
`MemorySource` and documents the two-method `Source` interface a
`LISTEN`/`NOTIFY` implementation would fill.

**Two websocket protocols are served, not one.** `graphql-transport-ws` is the
interoperable path; lightning's own protocol sends diffs and is the reason for
the fork. Keeping both is a real maintenance cost.

From the second:

**Go 1.27 is a hard floor** (D24). Generic methods are what let a field's type
be inferred from its resolver. There is no fallback for an older toolchain.

**Nullability is derived from the Go type and nowhere else** (D26). `*T` is
nullable, `T` is not, and a Go interface value is nullable because it can be
nil. `.NonNull()` exists but is documented as rarely right: when the Go type is
wrong, changing the Go type says the same thing to every reader.

**Interface and union membership is explicit** (D28). A witness function
`func(u *User) Actor { return u }` proves membership at compile time. Satisfying
a Go interface by accident is ordinary; joining a GraphQL interface by accident
is not.

**Sortable and filterable are per-field, and the arguments follow** (D35). A
connection over a type with nothing sortable has no `sortBy` argument at all.
`filterType`, custom filter functions and custom tokenizers were dropped; the
matching behaviour is now fixed and documented.

**Batching has no separate fallback implementation** (D34). A batch field's
single-parent path is the same function called with one parent, so
`.UseBatch(fn)` switches between them with nothing written twice.

**`__key` is opt-in** (D33). It travels only where the live-query diff needs it,
not on every HTTP response.

**A global identifier can be typed** (D47). `relay.ID[Task]` is an `ID` on the
wire and a decoded local identifier in Go, and another type's identifier is
refused while the query is prepared. `relay.GID` still accepts any, for the
fields that genuinely take any.

---

## 5. Known gaps

- **The app has not been clicked through in a real browser.** It is driven in
  jsdom, which exercises React, the Relay store, the generated artifacts, the
  network layer and the server — everything but a real rendering engine.
- **`npm run e2e` needs a running server**, so it is not part of `npm test` and
  CI does not run it. CI runs relay-compiler and the typechecks, which do not.
- **Sorting and filtering happen in memory.** A `relay.Connection` resolver
  returns the whole list and the plugin narrows it. A list too large for that
  should use `relay.ManualConnection`, which hands the resolver the sort and
  filter arguments to push down; there is no automatic pushdown and there
  cannot be one without knowing where the list comes from.
- **A plugin must wrap both of a batch field's resolvers.** A batch field has
  `Resolve` for one parent and `BatchResolver` for many, and which runs is
  decided per request, so a plugin that wraps only `Resolve` has behaviour that
  comes and goes with the batching. The seam says so in `FieldPlugin`'s
  documentation; it does not enforce it.
- **Persisted queries are unsupported** by the diff protocol, which carries
  query text. `js/` reports this clearly rather than failing obscurely.
- **`relay.ManualConnection` does not cap its own page size.** `WithMaxPageSize`
  applies to the connections the plugin pages; a manual resolver returns what it
  returns, by definition.

---

## 6. If you are picking this up

Read `DECISIONS.md` first — it is the log of every judgement call, including the
ones that turned out to be wrong and were corrected. Then `README.md` for the
API, then `example/` for a working schema of every shape.

The tests are worth reading as documentation: the root package's
`lightning_test.go`, `abstract_test.go`, `args_test.go` and
`batchfield_test.go` each state a claim about the API in their test names, and
`relay/` does the same for nodes, connections, sorting and conformance.
