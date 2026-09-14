# Decisions

Design decisions taken during the thunder → lightning refactor, and deviations from `PLAN.md`.

---

## D1. Module path is `github.com/hiett/lightning`

Phase 0. The fork lives at `github.com/hiett/lightning`, so that is the module path. Every
import in the tree was rewritten mechanically.

## D2. `go` directive

Phase 0 set `go 1.23` as the plan asked. Bumping `golang.org/x/sync` and `stretchr/testify` to
current in Phase 2 raised the requirement, so `go.mod` now declares the version those
dependencies require. CI pins `go-version: stable`.

## D3. Type-name uniqueness no longer recurses into unexported struct fields

Phase 0. `schemabuilder.checkTypeNameUniqueness` recursed into *every* struct field, including
unexported ones. Unexported fields never become schema fields, so their types are not part of the
schema and have no business participating in schema type-name uniqueness.

This was not merely cosmetic: on Go 1.25+, `sync.Mutex` embeds `internal/sync.Mutex`. Any schema
type transitively holding a mutex in an unexported field — e.g. `graphql/connection_test.go`'s
`User.resource *reactive.Resource` — panicked with
`Mutex type name is duplicated in packages sync and internal/sync`. The fork did not build its own
test suite on a modern toolchain.

## D4. `samsarahq/go` removed entirely, not just `oops`

Phase 2. `PLAN.md` §4 Phase 2.4 asked for `oops` to be replaced with `fmt.Errorf("%w")`, which was
done (8 call sites). That left `samsarahq/go` in `go.mod` for `snapshotter` alone, used by the test
suite. `samsarahq/go` has been unmaintained since 2018, and the final acceptance checklist requires
no abandoned dependencies, so `snapshotter` was vendored to `internal/snapshotter` (with
`io/ioutil` updated to `os`) and the dependency dropped.

## D5. GraphiQL assets come from a CDN

Phase 2. `PLAN.md` offered vendoring a GraphiQL build or serving it from a CDN, and preferred the
CDN "simpler, and this is a dev tool". Taken. `graphql/graphiql/index.html` is embedded with
`go:embed`; React and GraphiQL 3.7.1 load from unpkg. This removes `rakyll/statik`, `webpack`,
`package.json` and `yarn.lock` — there is no JavaScript build step in the Go module any more.

`graphiql.Handler()` targets `/graphql`; `graphiql.HandlerForEndpoint(path)` targets any other
path. The endpoint is substituted into the page with JavaScript string escaping.

Consequence: GraphiQL needs network access to unpkg. Acceptable for a development tool; a consumer
who needs an air-gapped IDE can serve their own page against the same endpoint.

## D6. The tree is `gofmt`-clean and CI enforces it

Phase 2. Thirteen files carried pre-Go-1.19 doc comment indentation. The whole tree was
reformatted once, and `.github/workflows/ci.yml` fails on `gofmt -l`, `go vet`, `go test`,
`go test -race`, and a dirty `go mod tidy`.

## D7. `PLAN.md` Trap 1 is stale: there is only ONE executor

`PLAN.md` §3 Trap 1 warns that "There are TWO executors" — `graphql/executor.go` (238 LOC) and
`graphql/batch_executor.go` (563 LOC) — and that every type-system change must land in both.

**This is not true of this tree.** Upstream commit `1de8d7d` ("clean out non-batch executor",
June 2019) deleted the non-batch executor. `graphql/executor.go` retains only shared helpers —
`pathError`, `PrepareQuery`, `SafeExecuteResolver`, `SafeExecuteBatchResolver` and the
`ExecutorRunner` interface. The single implementation of `ExecutorRunner` is `*graphql.Executor`
in `graphql/batch_executor.go`. `internal/testgraphql.GetExecutors()` returns exactly one entry,
`"batchExecutor:"`, and its multi-executor comparison loop is now vestigial.

The line count in the plan matches (`executor.go` really is 238 lines) because the file still
exists — it just no longer contains an executor.

Consequence: the Phase 4 and Phase 5 acceptance criteria that say "under **both** executors" are
satisfied by the one executor that exists. New tests still go through `internal/testgraphql` so
they would automatically cover a second executor if one were ever reintroduced.

## D8. Parser replaced with `vektah/gqlparser/v2`; four behaviour changes

Phase 3. `graphql/parser.go` now parses with `gqlparser/v2` instead of the 2016 snapshot of
`graphql-go/graphql`. The AST → `graphql.SelectionSet` conversion is preserved, as are the error
strings the test suite asserts. Four behaviours deliberately changed:

1. **`operationName` is supported.** `Parse` gained `ParseOperation(source, vars, operationName)`,
   `httpPostBody` and the websocket `subscribe`/`mutate` messages gained an `operationName` field.
   A document with several operations and no `operationName` is now rejected with
   *"must provide operation name if query contains multiple operations"*, replacing
   *"only support a single query"*, which stopped being true. Relay always sends `operationName`.

2. **A non-null variable may declare a default value.** Thunder rejected
   `query Op($x: Int! = 2)` with *"required variable cannot provide a default value"*. This is
   legal GraphQL, and relay-compiler emits it whenever `@argumentDefinitions` gives a non-null
   argument a default — `$count: Int! = 10` is the ordinary shape of a paginated Relay query. The
   restriction would have blocked Phase 10, so it is gone.

3. **An explicitly-null variable keeps its null.** Thunder replaced any null-valued variable with
   the operation's declared default. The specification's `CoerceVariableValues` only applies a
   default when the variable is *absent*, which is what is implemented now.

4. **`subscription` operations parse**, as Phase 3 asks; block strings (`"""`) and the `null`
   literal parse too, both of which post-date the 2016 parser.

Two latent bugs were fixed in passing, each covered by a new test:

- **Fragment spread directives leaked between spreads.** Thunder appended the *shared* `*Fragment`
  from its global fragment map to every spread site and then wrote the spread's directives onto it,
  so `...F @include(if: false)` in one place suppressed a plain `...F` somewhere else. Each spread
  now gets its own `*Fragment` sharing the fragment's `*SelectionSet`. Fragments are consequently
  converted in dependency order — `orderFragments` returns the DFS post-order it already computed
  for cycle detection — because a spread now copies a selection set that must already be built.
- **A type-condition-less inline fragment panicked.** `... @include(if: $x) { a }` is legal and
  dereferenced a nil type condition. It now parses to a `Fragment` with an empty `On`.

## D9. Query validation, and the SDL printer landing in Phase 3

Phase 3 asks for gqlparser's validator to be wired up. The validator needs an `*ast.Schema`, and
the only way to get one from a runtime `graphql.Schema` is to render the schema as SDL — which is
Phase 8's deliverable. Rather than write a throwaway converter and then a printer, `graphql/sdl.go`
was written once, in Phase 3, and serves both:

- `PrintSchema(*Schema) (string, error)` renders deterministic SDL — types, fields, arguments and
  enum values each in sorted order — so the export can be committed and diffed.
- `ASTSchema(*Schema) (*ast.Schema, error)` prints and then loads that SDL with
  `gqlparser.LoadSchema`, which also proves the schema is legal.
- `graphql.Validator` wraps the loaded schema. `Validator.Parse(source, vars, operationName)`
  parses, validates and converts in one step. `HTTPHandler` and the websocket connection each build
  one and use it in place of the bare `Parse`.

Two consequences worth stating:

- **Schemas are checked eagerly now.** A schema that cannot be printed as legal SDL fails when the
  handler is constructed rather than lazily, per field, during execution.
- **schemabuilder always creates a `Mutation` object even when nothing is registered on it.** A
  GraphQL object type with no fields is illegal, so a fieldless root operation type is left out of
  both the printed SDL and the `schema { ... }` block rather than being printed and rejected.

## D10. Spec-compliant response errors

Phase 4b's serialisation change landed with Phase 3, because the HTTP handler was being rewritten
for `operationName` and validation anyway, and validation errors have locations to report.

`graphql/response.go` defines `ResponseError{message, locations, path, extensions}` and a `Response`
whose `MarshalJSON` keeps the specification's distinction between the two kinds of failure: a
**request error**, where execution never began, omits `data` entirely; a **field error**, where
execution ran, includes `data` even when it is null. The old `Errors []string` field could not
express either.

`SanitizedError`/`ClientError` are untouched — `AsResponseErrors` runs every message through
`SanitizeError`, so internal errors still become "Internal server error" rather than leaking.

Path accumulation in the executor is finished in Phase 4b.

## D11. Go → GraphQL scalar mapping

Phase 4a. `PLAN.md` asks for these decisions to be recorded.

| Go type | GraphQL scalar | Why |
|---|---|---|
| `string` | `String` | |
| `bool` | `Boolean` | |
| `int8`, `int16`, `int32`, `uint8`, `uint16` | `Int` | Everything that fits in the specification's 32-bit signed `Int` |
| `int`, `int64`, `uint`, `uint32`, `uint64` | `Int64` | Too wide for `Int` |
| `float32`, `float64` | `Float` | |
| `schemabuilder.ID` | `ID` | New; Phase 6 depends on it |
| `time.Time` | `Time` | Custom scalar, RFC 3339 |
| `[]byte` | `Bytes` | Custom scalar, base64. Renamed from `bytes` |

**`Int64` is serialised as a decimal string, not a number.** A GraphQL `Int` is 32-bit, and a JSON
number loses precision above 2^53 the moment a JavaScript client runs `JSON.parse`, so neither can
carry a Go `int64` without silently corrupting large values — which `PLAN.md` explicitly forbids.
On input, `Int64` accepts a string, and also accepts a JSON number when it is exactly an integer;
`1.5`, or a number that has already lost precision, is rejected rather than truncated.

Two consequences, both deliberate:

- **Go's `int` maps to `Int64`, so `{"age": "5"}` is the wire form of an `Age int` field.** `int` is
  64 bits on every platform this library targets, and mapping it to `Int` would truncate silently at
  2^31. A field that genuinely is a small number should be declared `int32`, which maps to `Int` and
  stays a JSON number.
- **`uint64` above `math.MaxInt64` now survives.** The old `uint64` argument parser converted through
  `int64(asFloat)`, which truncated. It parses as `uint64` now.

`ID` is a struct (`schemabuilder.ID{Value string}`) rather than `type ID string` because
`schemabuilder` treats any two Go types of the same kind as the same scalar — a defined string type
would be indistinguishable from `string`. Per the specification, an `ID` argument accepts both a
string and an integer.

The introspection and query snapshots were regenerated and the diff read: it contains only scalar
renames (`string`→`String`, `bool`→`Boolean`, `int64`→`Int64`) and `int64` values changing from JSON
numbers to strings. Nothing else moved.

## D12. Response paths carry typed segments, and start at the root field

Phase 4b. `pathError` and the executor's `pathTracker` stored path segments as `[]string`, with list
indices stringified. The specification requires an error's `path` to be a list of field names
(strings) and list indices (numbers), and Relay's partial-data handling reads it, so both now carry
`[]interface{}` with real `int` indices. `writePath` still renders the dotted form for `Error()` and
`Reason()`, so no error *message* changed.

The top-level output node was seeded with the **operation name**, which then prefixed every error
path — `query foo { error }` produced `foo.error: test error`. An operation name is not part of a
response path. The root node now contributes no segment, and that error reads `error: test error`.

## D13. Interfaces mirror the union authoring pattern

Phase 5. `PLAN.md` asks for "the analogous thing for interfaces" to `schemabuilder.Union`. Taken
literally: `schemabuilder.Interface` is a marker struct, and the embedded pointer fields of the
struct that embeds it are the implementing types.

```go
type Content struct {
    schemabuilder.Interface

    *Photo
    *Article
}
```

A field returning `*Content` returns the struct with exactly one member set, exactly as a union
does. The marker struct *is* the type discriminator, so no `TypeResolver` function has to be written
by hand; `schemabuilder` builds one that matches by **struct field index**, which means an object
registered under a GraphQL name different from its Go type name still resolves. (`resolveUnionBatch`
still matches unions by Go field name and so still has that limitation; unions were out of scope.)

**The interface's field set** defaults to every field all implementing types agree on — same name,
same result type, same arguments. `schema.Interface("Content", Content{}).Fields("id", "summary")`
declares it explicitly instead, and building fails if any named field is missing from an
implementing type or disagrees about its signature. Relay's `Node` wants the explicit form; a
grab-bag interface is happy with the default.

### Type conditions were previously ignored altogether

`graphql/types.go` said of `Fragment.On`: *"That is not currently implemented in this package."* It
was not an understatement — `Flatten` merged **every** fragment into the selection regardless of its
type condition, so `... on Photo { caption }` inside a list of articles asked every article for
`caption`. Unions worked only because `resolveUnionBatch` filtered fragments itself before calling
into the object resolver.

`Flatten` now takes the concrete `*Object` it is flattening against and keeps only fragments whose
condition matches — no condition, the object's own name, or an interface the object implements.
`FlattenAll` is the unfiltered form, for callers that have already narrowed. `FragmentApplies` is
exported because both executor and `PrepareQuery` need the same rule, and union fragment matching
now goes through it too, so `... on SomeInterface` works inside a union.

This changed one existing test: `graphql/directive_test.go` registered its type as
`schema.Object("item", Item{})` but wrote `fragment X on Item`. The mismatch had no effect while
type conditions were ignored, and the fragment simply stopped applying once they were not. The
schema now registers `Item`, which is what the fragments always meant. gqlparser's validator would
reject the old query outright.

## D14. Phase 8 introspection work landed with Phase 5

`registerType` in `graphql/introspection/introspection.go` had to be rewritten for interfaces
(`kind: INTERFACE`, real `interfaces`, `possibleTypes` for interfaces as well as unions, `fields`
for interfaces). Rewriting the same function twice would have been wasteful, so Phase 8 items 3, 4
and 5 landed at the same time:

- **`subscriptionType`** added to `introspection_query.go` and populated from `Schema.Subscription`.
- **Descriptions** are now reported for scalars, enums, enum values, input objects, input fields,
  interfaces, fields and arguments, not only objects and unions. The carrying fields
  (`Field.Description`, `Field.ArgDescriptions`, `Enum.Descriptions`, `InputObject.FieldDescriptions`,
  `Scalar.Description`, …) were added to `graphql/types.go`. How they are *authored* in
  `schemabuilder` is Phase 8's remaining work.
- **Deprecation** is wired through: `Field.DeprecationReason` and `Enum.DeprecationReasons` drive
  `isDeprecated`/`deprecationReason`, `includeDeprecated` actually filters, and the printed SDL
  emits `@deprecated(reason: ...)`.

Two removals:

- `TypeAsOptionalDirective` (`@type_as_optional`) was a Samsara-internal, client-side-only directive
  referring to "Troy persistence schema", with no implementation anywhere in the tree. It is
  replaced in the advertised directive list by `@deprecated`, which the schema now actually uses.
- Enum value descriptions were being filled with the Go value behind the enum (`"0"`, `"1"`, …),
  which is not a description. They are empty unless one is supplied.

**Known gap:** `__Type.interfaces` and `__Type.possibleTypes` report `[]` rather than `null` for
kinds that have neither. The specification says null. `schemabuilder` cannot express a nullable list
return (`*[]T` is not a type it can build), and relay-compiler is fed the exported SDL rather than
introspection JSON, so this is recorded rather than worked around.

## D15. Pre-existing data races in the `reactive` test suite

`go test -race ./...` was never clean on this tree. Three tests in `reactive/rerunner_test.go`
(`TestErrorRetry`, `TestErrorRetryDelay`, `TestMinRerunInterval`) shared state between the test
goroutine and the rerunner's goroutine with no synchronisation, and `Expect.Trigger` panicked with
"close of closed channel" when a computation ran more than once against the same `Expect`.

The races are in the tests, not in `reactive` itself. The shared state is now mutex-guarded,
`Expect.Trigger` is idempotent via `sync.Once`, and `TestMinRerunInterval` stops its runner. Verified
with `go test -race -count=5 ./reactive/`.

## D16. Node registration is per-type, not a marker struct

Phase 6. Interfaces (D13) are declared with a marker struct listing their members, but `Node` is
different: which types are nodes is a property each type declares about itself, and a marker struct
listing them all would have to be edited every time a type joins.

```go
author := schema.Object("Author", Author{})
author.Node(
    func(a *Author) string { return a.Key },                          // type-local identifier
    func(ctx context.Context, id string) (*Author, error) { ... },    // fetch by that identifier
)
```

`Schema.Build` then, when at least one type has registered:

1. defines each node type's **`id` field as its global identifier**, replacing whatever `id` the Go
   struct would have exposed — that is what Relay's store keys off, and `schemabuilder` already lets
   a registered field override a struct field. The field is built with `reflect.MakeFunc` because
   its source parameter type is only known at run time, then goes through the ordinary `FieldFunc`
   path;
2. **builds every node type up front**, before the query root. A type reachable only through
   `node(id:)` would otherwise never be built and would be missing from the interface's possible
   types;
3. assembles the `Node` interface from those objects and marks each as implementing it;
4. adds `node(id: ID!): Node` and `nodes(ids: [ID!]!): [Node]!` to the query root, failing if the
   root already defines either name.

A field returns a value *as* a Node by wrapping it: `schemabuilder.NodeOf(typeName, value)` returns
a `*NodeRef`, and a field declared to return `*NodeRef` has the GraphQL type `Node`.

**Why `node`/`nodes` are built as `graphql.Field` values directly** rather than through `FieldFunc`:
their result types are interfaces, and `nodes` needs `[Node]!` — nullable entries, so an unknown
identifier yields null instead of failing the whole request. `schemabuilder`'s reflection path
passes `forceListEntryNonNull: true` everywhere and can only produce `[Node!]!`.

Resolution semantics: a malformed identifier or an unregistered type name is a **client error** with
a usable message; an object the fetcher does not find is **null**; an error from the fetcher
**propagates**.

## D17. Global identifiers are base64 of `TypeName:localID`, and swappable

The codec is the `GlobalIDCodec` interface, with `Base64GlobalIDCodec` as the default, replaceable
per schema with `(*Schema).SetGlobalIDCodec`. Decoding splits on the *first* colon, so a type-local
identifier may itself contain colons.

The default is obfuscation, not secrecy — anyone can base64-decode it. A schema whose identifiers
must not be guessable or forgeable should supply a codec that signs them; that is why the interface
exists and why `Encode` and `Decode` both return errors.

## D18. Connection conformance: three bugs and four deliberate changes

Phase 7. `PLAN.md` expected only a rename here. Printing a generated connection as SDL turned up
three bugs first.

**Bugs fixed:**

1. **`node: Item!!`** — `constructEdgeType` wrapped the node type in `NonNull` unconditionally, but
   `getType` had already wrapped a non-pointer node type. The doubled wrapper is not legal SDL and
   `gqlparser.LoadSchema` rejects it, so *no* schema with a connection over a value type could have
   been exported at all. `node` is now wrapped only if it is not already non-null.
2. **`NonNullItemConnection`** — generated type names came from `getTypeName`, which prefixed
   non-pointer Go types with the literal string `NonNull` to keep `[]Item` and `[]*Item` apart.
   That name reaches clients. Names now come from the node's **GraphQL** type name, so both spell
   `ItemConnection` / `ItemEdge`.
3. `hasPrevPage` → **`hasPreviousPage`**, the name in the specification (this one the plan predicted).

**Deliberate changes:**

4. **`startCursor` and `endCursor` are nullable** (`*string` in Go). An empty page has no cursors,
   and the specification types both as nullable; thunder returned `String!` and an empty string.
5. **`first` and `last` are `Int`, not `Int64`.** `ConnectionArgs.First/Last` and
   `PaginationArgs.First/Last` changed from `*int64` to `*int32`. relay-compiler declares pagination
   count variables as `Int` — `@argumentDefinitions(count: {type: "Int"})` is what
   `usePaginationFragment` generates — and would reject an `Int64` argument. A page size does not
   need 64 bits.
6. **`edges: [ItemEdge!]!` and `node: Item!` are kept stricter than the specification**, which allows
   `[ItemEdge]` with nullable entries. relay-compiler accepts the stricter form, and a connection
   that can hand back null edges is not useful. `PLAN.md` asked to tighten where thunder was looser;
   here it was already stricter, so it stays.
7. **`pageInfo.pages` and `totalCount` are kept as documented extensions.** Neither is in the
   specification; Relay ignores fields it does not know about. `PageInfo`'s doc comment now says so.
   `totalCount` stays `Int64!` because `Connection.TotalCount` is a Go `int64`.

Node types inside connections implement `Node` automatically when they register with
`(*Object).Node` — the connection reuses the same built `*graphql.Object`, so no extra work was
needed for `PLAN.md` item 4.
