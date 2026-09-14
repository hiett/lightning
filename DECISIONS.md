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
