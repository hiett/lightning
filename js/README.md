# @hiett/lightning-relay

A Relay network layer for [lightning](https://github.com/hiett/lightning)'s
reactive websocket protocol.

Lightning does not re-send a payload when a live query's data changes. It
re-executes the query, diffs the new result against the one it last sent, and
pushes only the difference. This package speaks that protocol, reassembles the
payloads, and hands Relay a complete `GraphQLResponse` every time.

- One websocket for queries, mutations and live queries.
- Reconnects with jittered exponential backoff and replays every subscription.
- Application-level heartbeat, because the server sets no read deadline.
- No runtime dependencies beyond `relay-runtime` and `graphql`.

## Install

```sh
npm install @hiett/lightning-relay relay-runtime graphql
```

## Quick start

```ts
import { Environment, RecordSource, Store } from "relay-runtime";
import { createLightningNetwork } from "@hiett/lightning-relay";

export const environment = new Environment({
  network: createLightningNetwork({ url: "wss://api.example.com/graphql" }),
  store: new Store(new RecordSource()),
});
```

Fetch a token before connecting, or supply your own socket, with `connect`:

```ts
createLightningNetwork({
  connect: async () => {
    const token = await getAccessToken();
    return new WebSocket(`wss://api.example.com/graphql?token=${token}`);
  },
});
```

To control the socket's lifetime yourself -- or to share one socket between
several environments -- build the connection and pass it in:

```ts
import { LightningConnection, createLightningNetwork } from "@hiett/lightning-relay";

const connection = new LightningConnection({ url: "wss://api.example.com/graphql" });
const network = createLightningNetwork({ connection });

// on sign-out
connection.close();
```

## A worked example

Take a schema whose `User` type declares a key field. The key is what lets the
server line up array elements between executions, and every paginated (Relay
connection) node type is required to have one:

```go
user := schema.Object("User", User{})
user.Key("id")
```

A Relay query:

```graphql
query UsersQuery {
  users {
    id
    name
  }
}
```

Relay calls the network layer, which sends:

```json
{"id":"0","type":"subscribe","message":{
  "query":"query UsersQuery { users { id name } }",
  "operationName":"UsersQuery",
  "variables":{}}}
```

The first reply is always a complete snapshot, because the server has nothing to
diff against yet. A snapshot is wrapped in a one-element array -- that wrapper is
the protocol's way of saying "this subtree is final, do not merge into it":

```json
{"id":"0","type":"update","message":[
  {"users":[{"id":"1","name":"bob"},{"id":"2","name":"alice"}]}]}
```

Relay receives the payload:

```json
{ "data": { "users": [{ "id": "1", "name": "bob" }, { "id": "2", "name": "alice" }] } }
```

Now someone renames alice, and someone else is added. The server re-executes the
query and sends only what moved:

```json
{"id":"0","type":"update","message":{
  "users":{"$":[[0,2],-1],"1":{"name":"alex"},"2":[{"id":"3","name":"carol"}]}}}
```

`"$"` is the reordering: `[0, 2]` is a run meaning "slots 0 and 1 come from
previous indices 0 and 1", and `-1` means "slot 2 has no previous value". Then
slot 1's `name` is patched in place and slot 2 is filled with a new object.

This package applies that to the payload it is holding and gives Relay the whole
thing again:

```json
{ "data": { "users": [
  { "id": "1", "name": "bob" },
  { "id": "2", "name": "alex" },
  { "id": "3", "name": "carol" }
] } }
```

An execution that changes nothing sends no frame at all. Silence means
"unchanged", not "stalled".

## Queries, mutations and live queries

| Relay operation | Wire message | Behaviour |
| --- | --- | --- |
| query | `subscribe`, then `unsubscribe` | Resolves with the first payload. |
| mutation | `mutate` | Resolves with the result payload. |
| subscription | `subscribe` | Stays open; emits a full payload per change. |

A mutation also causes the server to re-run every live query on the same socket
immediately, so the effects of a mutation arrive as updates without any
client-side cache invalidation.

### Making a query live

A subscription here is a live query: nothing is pushed as an event, the query is
simply re-run and the difference sent. Relay routes an operation to
`subscribeFn` only when it is declared as a `subscription`, so a live query is
written as one:

```graphql
subscription TasksLiveSubscription {
  tasks(first: 100) {
    edges {
      node {
        id
        title
        done
      }
    }
  }
}
```

The server has one requirement that is easy to trip over. It validates the
document against the schema's **subscription** root, but executes the selection
set against the **query** root, so a field used this way has to exist on both.
Registering it twice is the whole of it -- and it is what lets the same field be
fetched once or watched, as the example schema does:

```go
query.FieldFunc("tasks", func(ctx context.Context) []*Task {
    return store.Tasks(ctx)
}, schemabuilder.Paginated)

subscription.FieldFunc("tasks", func(ctx context.Context) []*Task {
    return store.Tasks(ctx)
}, schemabuilder.Paginated)
```

Nothing about the resolver makes it live. What makes it live is the dependency
the store records while resolving: when that key is invalidated, the operation
re-executes and the new result is pushed to everyone watching it.

To drive a live query without Relay's operation routing -- outside a Relay
environment, or for a field that only exists on the query root -- use the
connection directly:

```ts
import { LightningConnection, merge, stripKeys, type MergeValue } from "@hiett/lightning-relay";

let payload: MergeValue;
const handle = connection.subscribe(
  { query: "query UsersQuery { users { id name } }", variables: {} },
  {
    onReset: () => { payload = undefined; },
    onUpdate: (diff) => {
      payload = merge(payload, diff);
      render(stripKeys(payload));
    },
    onError: (error) => console.error(error),
  },
);

handle.dispose();
```

Routing ordinary Relay queries through a multi-emit observable, so that any
query can be live without being declared a subscription, is future work.

## Behaviour worth knowing

**Mutations are never queued.** A mutation attempted while the socket is down
rejects immediately with `lightning: not connected`, and one that was in flight
when the socket dropped rejects with `connection closed`. This is deliberate.
The server offers no idempotency, and a client cannot tell a mutation that never
arrived from one whose reply was lost, so replaying it across a reconnect risks
applying it twice. Retrying is left to the caller, who knows whether the
operation is safe to repeat. Queries and subscriptions have no such problem and
are replayed automatically.

**A reconnect starts every subscription over.** The server keeps no session and
there are no resume tokens: the client is the only record of what is subscribed.
On reconnect every subscription is re-sent with its original id, the server's
previous value is empty again, and the first update is a fresh snapshot. The
accumulated payload is discarded at that moment, because a diff against a value
nobody remembers is meaningless.

**Subscription errors are terminal.** The server closes a subscription when it
reports an error on it, so the error reaches Relay through `sink.error` rather
than being retried silently. A live query that quietly stops updating is worse
than one that says it stopped. Socket-level failures are different and are
retried transparently.

**`__key` never reaches Relay.** The server adds a `__key` field to objects of
keyed types so that it can line up array elements across executions. Nothing
selected it, and Relay would not expect it, so it is removed from a deep copy on
the way out -- the accumulated payload keeps its keys for the next diff.

**Only the first execution of a subscription can fail visibly.** An error in a
*re*-execution is swallowed server-side and retried, so a live query will not
report a transient resolver failure.

## Options

| Option | Default | |
| --- | --- | --- |
| `url` | -- | Server URL. One of `url` or `connect` is required. |
| `connect` | -- | `() => WebSocket \| Promise<WebSocket>`. Takes precedence over `url`. |
| `webSocketImpl` | global `WebSocket` | For Node, or for tests. |
| `connection` | -- | An existing `LightningConnection` to run on. |
| `connectionTimeoutMs` | 30000 | Covers `connect` and the socket opening. |
| `pingIntervalMs` | 30000 | Delay between a heartbeat reply and the next. |
| `pingTimeoutMs` | 30000 | How long a heartbeat reply may take. |
| `initialReconnectDelayMs` | 1000 | Doubles per failed attempt, then jittered. |
| `maxReconnectDelayMs` | 30000 | Ceiling for the reconnect delay. |
| `mutationTimeoutMs` | 10000 | How long a mutation waits for its reply. |
| `extensions` | -- | Opaque values sent with every operation; may be a function. |
| `autoConnect` | `true` | Set `false` for SSR or tests. |
| `logger` | -- | `{ warn(message, detail?) }`. |

A socket that delivered at least one message reconnects immediately, on the
assumption that it was a network blip. One that never produced anything is
backed off, on the assumption that the server is refusing us.

## The diff format

`merge` and `stripKeys` are exported, and are useful on their own. A diff node is
one of four things, told apart by JSON type alone:

| node | meaning |
| --- | --- |
| `[]` | the field was deleted |
| `[v]` | the field was replaced by `v`, which is already complete |
| a non-object | the field was replaced by that scalar |
| an object | recurse: an array diff, or a field-by-field diff |

An array diff is an object whose numeric keys index the array *after*
reordering, plus an optional `"$"` holding the reordering: for each slot of the
new array, where its previous value came from. Runs of consecutive indices are
compressed to `[start, length]`, and `-1` means a slot with no previous value.

Two details bite every port of this format:

- **An absent `"$"` means "same length, same order". It does not mean "empty".**
  `{"$": []}` is a legal diff meaning the new array is empty. Test for the key's
  presence, never for truthiness.
- **`[start, length]` is a count, not an end index.** Reading it as an inclusive
  end produces a stale trailing element, loses the tail of any run that does not
  start at index 1, and can index past the end of the array.

```ts
import { merge } from "@hiett/lightning-relay";

merge(["a", "b", "c", "d"], { $: [[1, 3], -1], "3": "e" });
// => ["b", "c", "d", "e"]
```

`merge` never mutates its arguments, shares the subtrees a diff did not touch,
and freezes what it returns.

## Development

```sh
npm install
npm run typecheck
npm test
npm run build
```

The merge test suite asserts against diffs generated by the Go differ itself
(`diff.Diff`), including every example in `diff/diff.go`'s package
documentation, so it tests the bytes a real server emits rather than a reading
of the spec.
