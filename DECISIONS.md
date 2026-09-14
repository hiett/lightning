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
