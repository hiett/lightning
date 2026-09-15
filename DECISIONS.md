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
   `query Op($x: Int! = 2)` with *"required variable cannot provide a default value"*. The
   specification permits it — §5.8.5 constrains a default value's *type*, not the nullability of
   the variable it belongs to — and gqlparser's validator accepts it, so a server that rejects it
   is refusing a legal document.

   A correction to what this entry first claimed: relay-compiler does **not** emit non-null
   variables with defaults from `@argumentDefinitions`. It rejects them itself, with
   *"Non-nullable variable 'count' has a default value"*, which Phase 10 found the moment the
   example app was compiled. So this change was not required by Relay. It stands because it is
   correct, not because anything downstream needed it.

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

## D19. Descriptions and deprecation are authored where the thing is written

Phase 8 asks this decision to be recorded. There are two ways to write a field, so there are two
ways to document one:

- **A field derived from a Go struct field takes struct tags**, because the documentation then sits
  next to the thing it documents:

  ```go
  type User struct {
      Name  string `description:"The user's display name."`
      Email string `description:"Their address." deprecated:"Use emails instead."`
  }
  ```

  `description` and `deprecated` are separate tags rather than options inside the existing
  comma-separated `graphql:"..."` tag, because prose contains commas. A bare `deprecated:""` means
  deprecated with the specification's default reason, "No longer supported".

- **A field registered with `FieldFunc` takes options**, because there is no struct field to hang a
  tag on:

  ```go
  user.FieldFunc("friends", resolve,
      schemabuilder.Description("Everyone this user follows."),
      schemabuilder.ArgDescription("limit", "How many to return."),
      schemabuilder.Deprecated("Use following instead."))
  ```

Object types use `(*Object).Describe`, interfaces `(*InterfaceObject).Describe`, and enums take
`EnumDescription`, `EnumValueDescriptions` and `EnumValueDeprecations` options on `(*Schema).Enum`.

Documentation is applied to built fields in one place, after `buildStruct`'s method loop, rather
than inside each of `buildFunction` / `buildBatchFunction` / `buildPaginatedField`, so every kind of
field is documented the same way.

In printed SDL, a field with documented arguments switches to the multi-line argument form, because
an argument description cannot sit inside a one-line list.

## D20. SDL export is a library helper, not a command

`PLAN.md` offered "a small `cmd/` tool or exported helper". A command cannot work: the schema is Go
code in the consuming project, and no binary in this repository can import it.

`graphql.WriteSchemaFile(schema, path)` and `graphql.WriteSchema(w, schema)` are the helpers. A
consuming project wires one up in a three-line `main` and calls it from `go:generate`, which
`example/` demonstrates. The file is written atomically — rendered to a temporary file in the same
directory, then renamed — so a failed build step leaves the previous schema in place rather than a
truncated one.

## D21. What the adversarial review found, and what was done about it

After the ten phases were complete, the tree was reviewed by five independent
reviewers across parser/validation, execution and the type system, Node and
scalars, the websocket and concurrency, and connections. Every candidate finding
was then handed to a separate verifier whose job was to *refute* it by running
real code. Thirty-one survived. The ones that changed code:

**Correctness bugs in code this refactor wrote**

- **Multi-operation documents were rejected.** `orderFragments` walked only the
  selected operation but checked "unused fragment" against every fragment in the
  document, so a two-operation document with a fragment each failed whichever
  operation you asked for — breaking the `operationName` support of D8 the day it
  landed. The unused check now considers every operation.
- **Interface field arguments were parsed once and reused.** A field selected
  directly on an interface is resolved against every implementing type, and each
  has its own Go argument struct. The first implementation's struct was handed to
  all of them, which is a type error. Arguments are now parsed per implementing
  type and carried on the selection (`Selection.ArgsForType`).
- **`{ __typename }` on the root operation was an internal server error**, because
  the root type has no field of that name. Relay asks for it.
- **A type condition naming a union was silently dropped**, and a union value
  whose selection set matched no fragment resolved to null rather than an object.
  `FragmentApplies` now knows about union membership, and unions go through the
  same path as interfaces.
- **A union member registered under a name other than its Go field name crashed
  the process**, because the executor matched by field name and called `IsNil` on
  the resulting zero `reflect.Value`. Unions now use the same field-index type
  resolver as interfaces, which also lifts the rename limitation recorded in D13.
- **`PrintSchema` silently dropped a second type sharing a name** — along with
  everything reachable only through it — and which one won was decided by map
  iteration order, so the committed schema flipped between runs. It is an error
  now, the schema builder refuses a generated name that collides with a
  registered one, and two paginated fields over one node type share one
  connection type instead of minting two.
- **The printer mutated the schema it was printing**, naming anonymous input
  objects from a counter written into the type. That made output depend on how
  many times it had run and two concurrent prints a data race. Names now come
  from the object's own shape.
- **`@skip`/`@include` were ignored on any field selected more than once.**
  Merging occurrences dropped their directives, so a skipped occurrence not only
  ran but contributed its sub-selections to a sibling. Directives are now applied
  per occurrence, before merging, as the specification requires.
- **`__typename` ignored `@skip`/`@include`** because it was written before the
  directive check.
- **Integer literals above 2^53 were rounded** on the way through `float64`. They
  keep their `int64` type now, and the argument parsers accept both.
- **`Int` arguments were never range- or integer-checked**: out-of-range values
  wrapped and fractional ones truncated, silently.
- **`hasNextPage` was computed against a stale count** when `after` and `before`
  were used together, so a page at the end of a list claimed another page and a
  client paginating forward asked for it for ever.
- **A description containing `"""` produced SDL that would not parse.**
- **`connection_init`'s returned context was thrown away**, so the authentication
  hook could not actually carry identity into resolvers — which is what its own
  documentation said it was for.
- **Every completed operation leaked its `context.CancelFunc`**, and a client
  that stopped reading could pin a writer for ever. Streams are cancelled on
  completion and writes have a deadline.
- **A finishing operation unregistered whatever held its id**, so a client that
  reused an id lost the new operation's registration.
- Node registration mistakes panicked out of `Build` instead of being returned,
  and the generated `Node` name was not reserved.
- `filterText` on a connection with nothing filterable dropped every row and
  reported a total of zero; `sortBy` on an unregistered field produced "Internal
  server error". Both are client errors with usable messages now.
- A client-safe error lost its response path, because `nestPathError` returned
  `SanitizedError`s undecorated. Paths are for clients, so that is exactly
  backwards; `IsSanitized` looks through the decoration.

**Bugs inherited from thunder**

- **`diff` corrupted every field that was new since the last execution.** A new
  field's value went into the diff raw, which is indistinguishable from a diff
  node: a new field holding `[]` was read as a deletion, `[v]` was unwrapped to
  `v`, and an object was recursed into as though it were a diff. Since a live
  query re-executes whenever its data changes, and a field appearing for the
  first time is completely ordinary, this corrupted live-query payloads
  routinely. New fields are marked as replacements now.
- **A `reactive.Resource` was permanently poisoned once its last dependent went
  away.** `release()` invalidates, `invalidated` is sticky, and `addOut` then
  invalidated every new dependent — which re-ran, depended again, and spun for
  ever, burning a full query execution each time. One ordinary HTTP request
  against a resource with no current subscribers was enough to trigger it.
  Holding a `Resource` on a struct is the documented pattern and thunder's own
  tests do it. A released node no longer propagates its invalidation.
- **The reactive test suite had three data races** and `Expect.Trigger` panicked
  on a second call (D15).

**Not changed, and why**

- `__key` appears in plain HTTP responses for any type with a key field. It is
  the correlation token the diff algorithm needs and the executor emits it
  regardless of transport. Relay ignores unknown fields and `js/` strips it.
- `detectConflicts` only examines the top level of a query. gqlparser's validator
  does the complete job before execution, so this is redundant rather than wrong.
- Invalidation reruns fan out serially. It is a throughput characteristic of the
  reactive core, not a correctness problem.

## D22. The Relay network layer applies diffs client-side and hands Relay whole payloads

Phase 9.2. `PLAN.md` posed the design question and recommended starting with the simpler answer.
Taken, and it stays: `js/src/network.ts` holds the accumulated payload for each live operation,
applies each diff to it with `merge()`, and gives Relay a complete `GraphQLResponse` every time.

The alternative — translating diffs directly into Relay store updates — would be faster, because
Relay would re-normalise only what changed. It also means reimplementing the reordering and
replacement semantics against the store's record API, where a mistake shows up as quietly wrong data
rather than a failed test. Full payloads are correct by construction, and Relay's normalisation
already skips writes for values that did not change. **Diff-to-store is noted as future work.**

Three consequences worth knowing:

- **`__key` is stripped** from what Relay is handed — it is the server's correlation token for
  lining up array elements, not part of any selection set — but kept in the accumulated merge
  state, because the next diff needs it. The strip is a deep copy, so a consumer cannot mutate the
  live state either.
- **A reconnect resets the accumulated payload.** The protocol has no session and no resume token,
  so a reconnect re-subscribes from scratch and the next message is a complete snapshot. Applying a
  snapshot to a stale accumulator would be merging against a state the server no longer has.
- **Persisted queries are unsupported**, because the protocol carries query text. The network layer
  says so rather than failing obscurely.

`close()` is terminal: every subscription ends through `onError` and in-flight mutations reject,
rather than being silently orphaned. Reconnect backoff escalates unless the previous connection
lived at least ten seconds, so a server that accepts and immediately closes is backed off from
rather than hammered.

## D23. The Relay app is verified by running it, not by reading it

`PLAN.md` §4 Phase 10 calls the example Relay app "the gate that proves the whole refactor". It is
run as a test rather than described:

- `example/web/e2e/app.test.tsx` mounts the **real components** — the ones relay-compiler compiled
  against the exported `schema.graphql` — in jsdom, points them at a running example server over the
  live-query websocket, and drives them. Nothing is mocked.
- It covers all four acceptance behaviours: `usePaginationFragment` renders a page and loads
  another, an interface resolves to its concrete types, `@refetchable` fetches a node by its global
  id, a mutation commits and `@appendNode` splices the result into the connection without a
  refetch, and a live query is pushed a change made over a *different* connection.
- CI regenerates `schema.graphql` from the Go schema and re-runs relay-compiler, failing if either
  the exported schema or the generated artifacts are out of date. A schema change that was never
  exported cannot pass.

The end-to-end test needs a running server, so it is a separate `npm run e2e` rather than part of
`npm test`; CI runs relay-compiler and the typecheck, which need no server.

Building it turned up one real gap in the example: `@appendNode` needs the connection's Relay id,
and `__id` has to be selected explicitly on a `@connection` field to get it. Without it the add
button silently did nothing.

---

# The authoring API refactor

Everything from D24 on concerns the second refactor: replacing the schema-authoring layer with the
generics-based API in the root `lightning` package. The runtime — executor, validator, SDL printer,
introspection, reactive live queries, the diff protocol — is unchanged except where a decision below
says otherwise.

The two tests every choice was settled by, in order:

1. **The programmer writes as little as possible.**
2. **What they do write lives next to the thing it describes.**

## D24. Go 1.27 generic methods are the enabling constraint

`go.mod` declares `go 1.27.0`, and the API depends on a feature that arrived with it:

```go
func (t *Type[T]) Field[R any](name string, resolve func(ctx context.Context, parent *T) (R, error)) *Field
```

A **generic method** is what lets the result type be inferred from the resolver. Without it, `R`
would have to be a parameter of `Type`, which would mean one handle per field type, or the call site
would have to name it. Both were prototyped and both read worse than the old library.

This was verified on the installed toolchain before anything was designed around it, because the
whole shape of the API turns on it.

## D25. The Go type is the ref

Pothos needs `type: PostRef` at every call site because TypeScript has no runtime types. Go does, so
the GraphQL type is looked up from the resolver's Go return type:

```go
task.Field("owner", store.Owner)   // store.Owner returns (Actor, error); that names the type
```

There is no type argument anywhere in the public API's ordinary path, and no registry key to keep in
step with a Go type. The registry is `map[reflect.Type]*typeDecl`, and a resolver returning a type
that is not in it is a build error naming the type and the declaration that would fix it.

The one escape hatch, `RawFieldArgs`, is for plugins and is documented as such: a field whose type is
generated (`TaskConnection`) has no Go counterpart to read the type off.

## D26. Nullability is derived from the Go type, at every level

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

An interface value is nullable for the same reason a pointer is: it can be nil, and a resolver
returning `(Actor, error)` is able to return nothing.

This deletes `NonNullable`, `ListEntryNonNullable` and every other nullability option, and with them
the old library's bug where `[]*string` silently became `[String!]!`. `.NonNull()` and `.Nullable()`
survive as overrides and are documented as rarely right: when the Go type is wrong, changing the Go
type says the same thing to every reader rather than to one field.

## D27. Struct tags carry documentation, and `lightning.Meta` carries the type's own

```go
type Task struct {
    lightning.Meta `graphql:"Task" description:"A unit of work."`

    Key   string `graphql:"-"`
    Title string `description:"What needs doing." sortable:"true" filterable:"true"`
    Notes *string `description:"Anything else." deprecated:"Use comments."`
}
```

The vocabulary is `graphql`, `description`, `deprecated`, `default`, `sortable`, `filterable`. A tag
key that looks like one of these but is not — `describe:`, `desc:`, `doc:`, `sort:` — is a build
error naming the field and the key that was meant, because a misspelled documentation tag documents
nothing and says so nowhere.

One marker type serves objects, inputs and argument structs: what it carries, a name and a
description, is the same for all three.

## D28. A GraphQL interface is a Go interface, and membership is a witness function

```go
type Actor interface{ DisplayName() string }

actor := lightning.Interface[Actor](b)
lightning.Implements(actor, user, func(u *User) Actor { return u })
```

The witness is the point: `func(u *User) Actor { return u }` compiles only if `*User` satisfies
`Actor`, so membership is checked by the compiler and the error names the missing method. A resolver
returns the interface value, and the concrete Go type behind it decides `__typename`.

This deletes the marker struct and its one-hot invariant — `&Actor{User: u}`, valid only if exactly
one embedded pointer is set — along with every runtime check that existed to police it. An invalid
value is now unrepresentable.

Membership is **explicit** rather than inferred from which Go types happen to satisfy the interface.
Satisfying an interface by accident is ordinary Go; joining a GraphQL interface by accident is not.

## D29. A member inherits an interface field it does not declare

The old library refused a schema where an implementing type did not itself provide every interface
field. That made sense when the interface had no resolvers of its own. Here it does: an interface
field's resolver takes the interface value, which every member satisfies by construction.

So a member that declares the field must agree about its type — a mismatch is a build error naming
both types and the field — and a member that does not simply inherits it. Declaring a field once, on
the interface, is the reason for declaring it there.

## D30. Arguments are an ordinary Go struct

```go
type AddTaskArgs struct {
    Title   string        `description:"What needs doing."`
    OwnerID relay.GID     `graphql:"ownerId" description:"Who it belongs to."`
    Limit   *int32        `description:"How many to return." default:"20"`
}
```

`A` is inferred from the resolver and never named at a call site. A pointer field is optional, a
value field is required, and a `default` makes a value field optional too.

**`ArgDescription` is deleted.** It matched an unchecked string against a reflection-mangled name and
documented nothing at all when the two disagreed — which is the exact failure the second test above
exists to prevent.

Field-name derivation is acronym-aware: `OwnerID` → `ownerId`, `ID` → `id`, `HTTPServer` →
`httpServer`, `UserURL` → `userUrl`. The old `OwnerID` → `ownerID` was a surprise that a client only
found at runtime.

Nested structs reached through an argument are declared as input objects automatically, named after
the Go type. The old `_InputObject` suffix is gone: it named nothing a client could see the reason
for.

## D31. The plugin seam is a set of small optional interfaces

`Plugin` is a name. Beyond it, a plugin implements only what it needs: `InstallPlugin`,
`FieldPlugin`, `BeforeBuildPlugin`, `AfterBuildPlugin`. A capability added later does not break the
plugins that came before it.

The seam is enforced by the package system, not by convention: `relay` lives in its own package and
imports only the core's exported API. Everything it does, anything else can do. When relay needed
something the seam did not offer — a key field, arguments computed at build time — the seam grew a
public method rather than relay reaching inside.

The one thing that moved *out* of the core for this: the text search behind `filterText` now lives in
`relay`, because searching text is a property of this plugin's connections rather than of the schema
builder.

## D32. Relay is a plugin, and node identifiers are the cursor keys

`graphql/schemabuilder/pagination.go` was 1,640 lines and `node.go` 546, inside a package that should
not have known what Relay is. Both are now `lightning/relay`, which the core is unaware of.

Registering a node type takes one line beyond declaring it:

```go
relay.Node(b, store.Task)   // uses (*Task).NodeID()
```

and that one line now supplies **three** things it used to take three declarations to say: the `id`
field, the cursor key for every connection over the type, and the `__key` the live-query diff lines
list elements up by. `Key("key")` is gone, and with it the requirement that the key be an exposed
field — which is why `example/`'s `Task` no longer publishes `key: String!` alongside `id`.

Cursors stay **key-based and insertion-stable**: a cursor names the item it points at, so inserting
earlier in the list does not move it. In a library whose headline feature is live queries over
changing lists, an offset cursor would be wrong in a way it would not be elsewhere.

## D33. `__key` is emitted only for the live-query diff

`__key` is the diff protocol's correlation token: with it, moving an item in a list is a reorder, and
without it a delete and an insert of everything after. It is not part of the schema and no client
asked for it, so emitting it on every response was noise on every response.

It is now opt-in — `graphql.WithKeys(ctx)` — and the diff protocol's rerunner is what opts in. A
plain HTTP response carries the client's selection set and nothing else.

## D34. Batching is index-aligned slices, and there is no second implementation

```go
task.Batch("owner", func(ctx context.Context, tasks []*Task) ([]*User, error) { ... })
task.Load("owner", func(t *Task) string { return t.OwnerID }, store.UsersByID)
```

`Batch` hands the resolver every parent and takes one result per parent, in order — the contract the
executor already had, and the one every loader library uses. `R` is the type of one result, so the
Go type still says everything about the field. Returning the wrong number of results is reported by
name rather than silently misaligning the response.

`Load` is the case batching is nearly always for: read a key off each parent, fetch the distinct
keys once, hand each answer back to everyone who wanted it. Three tasks with the same owner cost one
lookup, and the deduplication is not the caller's problem.

This replaces `map[batch.Index]*T` in and `map[batch.Index]R` out. The old shape existed so a
resolver could return fewer results than it was given; an index-aligned slice says the same thing
with a nil.

**There is no `BatchFieldFuncWithFallback`.** A batch field's single-parent path is derived from the
batch resolver by calling it with one parent, so `UseBatch(func(ctx) bool)` switches between one call
with N parents and N calls with one — both correct, with nothing written twice and nothing that can
drift apart. The old API required a second function and then compared the two signatures at build
time to check they agreed.

`Field.Split` replaces `NumParallelInvocationsFunc`, unchanged in meaning.

## D35. Sortable and filterable are declared on the field, and the arguments follow

Whether a task's title can be searched is a fact about the title, so it is written on the title:

```go
Title string `description:"What needs doing." sortable:"true" filterable:"true"`
```

or, for a computed field, `.Sortable()` and `.Filterable()`.

A connection over a type with nothing sortable **has no `sortBy` argument**. This is the fix for the
old library, where every connection advertised `sortBy`, `sortOrder`, `filterText`,
`filterTextFields` and `filterType` whether or not anything was registered to use them, and silently
did nothing when a client used them — which is how `example/`'s `tasks` field came to advertise five
arguments that did nothing at all.

It also deletes nine near-identical option constructors — `FilterField`, `BatchFilterField`,
`BatchFilterFieldWithFallback`, `SortField`, `BatchSortField`, `BatchSortFieldWithFallback` and the
rest — each a closure factory that panicked on a duplicate name. A sortable field is an ordinary
declared field, so it is batched if it batches and expensive if it is expensive, with nothing extra
to say.

`filterType`, `FilterFunc` and the custom tokenizers are **dropped**. They were extension points with
no consumers, and the matching behaviour they defaulted to is now fixed and documented: terms split
on whitespace, a quoted run is one term, matching is case-insensitive on a substring, and any term
matching any searchable field keeps the row. Something else is a plugin's job.

A sortable or filterable field may take no arguments — there is no selection for a sort to read them
from — and that is a build error rather than a runtime surprise. A filterable field must resolve to
a string.

## D36. `pageInfo.pages` is kept

It is not part of the Relay specification and a Relay client ignores it, but it is what
page-number pagination needs, and it costs one field. The first entry is the empty cursor, meaning
the start of the list, exactly as before.

## D37. A resolver that pages itself says so, and is believed

```go
relay.ManualConnection(q, "tasks", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Task, relay.PageResult, error) {
    rows, total, more := store.PageTasks(ctx, p)
    return rows, relay.PageResult{TotalCount: total, HasNextPage: more}, nil
})
```

The resolver is given the page the client asked for — including the sort and filter arguments, so it
can push them down to the database — and returns exactly the items in it. The plugin adds cursors
and nothing else: it does not narrow, order or trim what came back.

This replaces embedding `PaginationArgs` in the argument struct and returning `PaginationInfo`
alongside, a shape that was valid only in one combination and was checked for at build time.
`PostProcessOptions` is dropped: a manual connection is manual, and a resolver that wants the plugin
to do the work should use `Connection`.

## D38. A text-marshalling type is converted at resolve time

A Go type that implements `encoding.TextMarshaler` is a `String`, and the conversion happens in the
field's resolver rather than in the JSON encoder. A type that fails to marshal now reports a GraphQL
error naming the field, instead of failing halfway through writing a response that has already
started.

A nil pointer to such a type is `null`. The old code returned `""`, which is a different value.

## D39. An ordinary field is not `External`

`graphql.Field.External` schedules a field as a potentially-blocking call. The old builder set it on
every resolver it registered, so a plain accessor cost a work unit and a goroutine.

Only a batch field is external now — a batch resolver is an external call by definition — and only an
expensive field is split per source. A query over a list of objects whose fields are accessors runs
in a single work unit.

## D40. A pointer to an enum is an enum

`func() *Status` produced a plain number in the old builder while `func() Status` produced the
enum's name, so the same value came back two different ways depending on which field asked. Both are
now the enum.

## D41. `graphql/schemabuilder` is deleted, and its tests were rewritten rather than translated

Two authoring APIs is a permanent tax on every future change, and there are no consumers. The
package is gone, along with the dead federation surface it carried: `FetchObjectFromKeys`,
`RootObjectType`, `ShadowObjectType`, `ServiceName`, `NewSchemaWithName`, `buildFederatedFunction`,
`buildShadowObjectFederationFunction` and `graphql.Field.FederatedKey`. All are recoverable from git
history.

Around 9,500 lines of test exercised it. Where a test was about the **runtime** — directives,
errors, the executor, scalars, transports, introspection — it was ported line for line and its
expectations were only changed where a decision above changed the answer. Where it was about the
**old authoring API** — union marker validation, one-hot invariants, `map[batch.Index]` signatures,
per-connection filter registration — the behaviour it tested no longer exists, and the test was
replaced by one covering the new API's equivalent. `graphql/connection_test.go` was 2,253 lines of
the second kind; what survives of it is in `relay/`.

The connection conformance tests, which `PLAN.md` §3 names as the contract, were ported unchanged in
meaning and pass.

## D42. Every change to the example's exported schema

`example/schema.graphql` is the wire contract a client compiles against. Against the pre-refactor
file, every line that changed:

- **Added descriptions**, on `PageInfo` and its fields, on connection and edge fields, on `id`, on
  `Task.added`, and on every connection argument. Documentation, not contract.
- **`filterType: String` removed** from the `tasks` arguments. The custom filter functions it
  selected are gone (D35); nothing in the example registered one, so the argument did nothing.
- **`key: String!` removed** from `Task`. Cursors come from the node identifier (D32), so a key no
  longer has to be an exposed field. It was documented as "use `id` for anything a client stores".
- **`status: TaskStatus!` and `enum TaskStatus` added.** The example gained an enum, so that the
  example exercises one.
- **`sortBy`, `sortOrder`, `filterText` and `filterTextFields` now work.** They are the same
  arguments with the same types, but they appear because `Task.title` and friends declare themselves
  sortable and filterable, and a client using one gets the behaviour it asks for rather than silence.

Nothing else differs: no type, field, argument or nullability change beyond those five. relay-compiler
compiles the app's nine documents clean against it and the end-to-end tests pass unchanged.

## D43. Every field takes one path

A struct's exported fields used to be turned straight into finished
`graphql.Field` values, while declared fields went through the builder. Two
paths meant two of everything, and both halves drifted:

- **A plugin never saw a struct field.** `FieldPlugin.Field` is how a plugin
  adds authorisation or tracing to every resolver; it was shown the declared
  ones only, so a type whose fields were all struct-derived was invisible to it.
  That is not a gap a plugin author could reasonably guess at.
- **A promoted field read the wrong value.** An embedded struct's field was read
  by its index *inside the embedded struct*, applied to the outer struct. For
  `struct { Name string; Timestamps; Note string }`, `createdBy` — field 0 of
  `Timestamps` — returned field 0 of the outer struct. Silently, with no error
  anywhere, and only for types that embed.
- **`DeclaredType.HasField` answered "no" for a field that existed**, so relay's
  check for a type that already has an `id` missed a struct field called `Id`
  and the collision surfaced later as a generic duplicate-field error.

A struct field is now a `fieldDecl` like any other, carrying an index *path*
rather than an index, and there is one loop that checks the name, builds the
field, shows it to the plugins and records whether it is sortable.

## D44. A field the executor answers itself cannot be declared

`__typename` and `__key` are written by the executor whatever a type says, so a
field declared under either name would never be called. That is now a build
error naming the field and saying what supplies it, rather than a resolver that
silently never runs.

Other `__`-prefixed names are left alone: `__schema` and `__type` are ordinary
fields that the introspection schema declares through the same API an
application uses, and banning the prefix outright would ban that.

## D45. Two argument mistakes that used to be silent or fatal

**An output object cannot be an argument.** A Go type declared with
`lightning.Object` and then used in an argument struct built a schema whose
argument was an object type, which is illegal GraphQL that only a client would
discover. It is now a build error saying to give the argument a struct of its
own. The old library generated a parallel `_InputObject` for the same Go type,
which meant a type could be two different things depending on where it was used.

**A connection argument cannot clash with a pagination one.** The connection's
own arguments and the resolver's share one struct, so an argument named `First`
used to panic out of `reflect.StructOf` with "duplicate field First". It is now
a build error naming the argument.

## D46. The introspection schema follows the specification's nullability

`__Type.fields`, `interfaces`, `possibleTypes`, `inputFields` and `enumValues`
are nullable lists in the specification, and null is what they mean: a scalar
does not have an empty set of interfaces, it has no answer to the question. They
reported `[]`. Descriptions and deprecation reasons are nullable Strings and
reported `""`. `__schema.queryType` is non-null and was nullable.

All are now as the specification says. The old builder could not express a
nullable list return, which is why they were wrong; the new one derives it from
`*[]T`, so saying it is a one-character change.

Fixing it turned up a runtime gap: the executor assumed every list source was a
slice, so a `*[]T` reached it as a pointer and it panicked taking the length of
one. A nil pointer to a list is now null and a non-nil one is the list it points
at — which is what the nullability table in D26 promised all along, and what
nothing had yet returned.

## D47. A global identifier can say what it names

`relay.GID` accepts any global identifier, which is right for `node(id:)` and
wrong almost everywhere else: a mutation that takes a task's identifier has no
use for a user's, and being handed one is a client mistake.

`relay.ID[Task]` is an `ID` on the wire — the schema is identical, which is why
adding it to the example changed no line of `schema.graphql` — and a decoded
local identifier in Go. An identifier naming anything but a `Task` is refused
**while the query is prepared**, which is earlier than a resolver could refuse
it, with a message saying what was expected.

This deletes the decode-and-check helper every project writes by hand, along
with the check the hand-written one usually forgets.

Reaching it needed one addition to the seam. `ID[T]` is a different Go type for
every `T` and nothing can enumerate the instantiations an application will use,
so scalars could not be registered one at a time. `Builder.ScalarShapes` takes a
function consulted for any Go type the builder does not otherwise recognise, and
relay claims the ones that are an `ID[T]`. The seam is general — it is how any
plugin claims a family of Go types — and the core still knows nothing about
relay.
