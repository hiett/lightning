# @hiett/lightning-relay

A Relay network layer for [lightning](https://github.com/hiett/lightning)'s
reactive websocket protocol.

Lightning does not re-send the whole payload when a live query's data changes.
It re-executes the query, diffs the new result against the one it last sent, and
pushes only the difference. This package speaks that protocol. It reassembles
the payloads and hands Relay a complete `GraphQLResponse` every time.

- One websocket for queries, mutations and live queries.
- Reconnects with jittered exponential backoff and replays every subscription.
- Application-level heartbeat, because the server sets no read deadline.
- No runtime dependencies beyond `relay-runtime`.

## Install

```sh
npm install @hiett/lightning-relay relay-runtime
```

`@types/relay-runtime` is a dependency rather than a peer, because this
package's own `.d.ts` files refer to those types and `relay-runtime` ships Flow
types and no TypeScript ones.

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

To control the socket's lifetime yourself, or to share one socket between
several environments, build the connection and pass it in:

```ts
import { LightningConnection, createLightningNetwork } from "@hiett/lightning-relay";

const connection = new LightningConnection({ url: "wss://api.example.com/graphql" });
const network = createLightningNetwork({ connection });

// on sign-out. Every subscription ends with an error, and in-flight mutations
// reject; see "Behaviour to expect".
connection.close();
```

## A worked example

Take a schema whose `User` type is a Relay node. Nothing declares a key field:
`relay.Node` takes one from the type's `NodeID` method. The server uses the key
to line up array elements between executions, and every paginated (Relay
connection) node type must have one:

```go
type User struct {
    lightning.Meta `description:"A person."`

    Key  string `graphql:"-"`
    Name string `description:"The user's display name."`
}

func (u *User) NodeID() string { return u.Key }

users := map[string]*User{
    "1": {Key: "1", Name: "bob"},
    "2": {Key: "2", Name: "alice"},
}

b := lightning.New(relay.Plugin())
lightning.Object[User](b)
relay.Node(b, func(ctx context.Context, id string) (*User, error) {
    return users[id], nil
})

b.Query().Field("users", func(ctx context.Context, _ *lightning.Root) ([]*User, error) {
    return []*User{users["1"], users["2"]}, nil
})
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

The first reply is always a complete snapshot: the server has nothing to diff
against yet. A snapshot is wrapped in a one-element array. The wrapper means
"this subtree is final, do not merge into it":

```json
{"id":"0","type":"update","message":[
  {"users":[{"id":"VXNlcjox","name":"bob"},{"id":"VXNlcjoy","name":"alice"}]}]}
```

`id` is the global identifier `relay.Node` gives the type, so it arrives
base64 encoded: `VXNlcjox` decodes to `User:1`. Relay receives the payload:

```json
{ "data": { "users": [
  { "id": "VXNlcjox", "name": "bob" },
  { "id": "VXNlcjoy", "name": "alice" }
] } }
```

Now someone renames alice, and someone else is added. The server re-executes the
query and sends only what moved:

```json
{"id":"0","type":"update","message":{
  "users":{"$":[[0,2],-1],"1":{"name":"alex"},"2":[{"id":"VXNlcjoz","name":"carol"}]}}}
```

`"$"` is the reordering: `[0, 2]` is a run meaning "slots 0 and 1 come from
previous indices 0 and 1", and `-1` means "slot 2 has no previous value". Then
slot 1's `name` is patched in place and slot 2 is filled with a new object.

This package applies that to the payload it is holding and gives Relay the whole
thing again:

```json
{ "data": { "users": [
  { "id": "VXNlcjox", "name": "bob" },
  { "id": "VXNlcjoy", "name": "alex" },
  { "id": "VXNlcjoz", "name": "carol" }
] } }
```

An execution that changes nothing sends no frame. Silence means the data is
unchanged. It does not mean the subscription has stalled.

## Queries, mutations and live queries

| Relay operation | Wire message | Behaviour |
| --- | --- | --- |
| query | `subscribe`, then `unsubscribe` | Resolves with the first payload. |
| mutation | `mutate` | Resolves with the result payload. |
| subscription | `subscribe` | Stays open; emits a full payload per change. |

A mutation also makes the server re-run every live query on the same socket
immediately, so the effects of the mutation arrive as updates with no
client-side cache invalidation.

### Making a query live

A subscription here is a live query. Nothing is pushed as an event: the server
re-runs the query and sends the difference. Relay routes an operation to
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

The server has one requirement that is often missed. It validates the document
against the schema's **subscription** root, but executes the selection set
against the **query** root, so a field used this way has to exist on both.
Register it twice, and the same field can then be fetched once or watched, as
the example schema does:

```go
tasks := func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Task, error) {
    invalidator.Depend(ctx, "tasks") // before the read, never after
    return []*Task{
        {Key: "1", Title: "Write the schema", Done: true},
        {Key: "2", Title: "Make it live"},
    }, nil
}

relay.Connection(b.Query(), "tasks", tasks)
relay.Connection(b.Subscription(), "tasks", tasks)
```

Nothing about the resolver makes it live. Liveness comes from the dependency it
records while resolving: `Depend` names the keys it is about to read, and when
one of them is invalidated, the operation re-executes and the new result is
pushed to everyone watching it.

To drive a live query without Relay's operation routing (outside a Relay
environment, or for a field that only exists on the query root), use the
connection directly:

```ts
import { LightningConnection, merge, stripKeys, type MergeValue } from "@hiett/lightning-relay";

const connection = new LightningConnection({ url: "wss://api.example.com/graphql" });

let payload: MergeValue;
const handle = connection.subscribe(
  { query: "query UsersQuery { users { id name } }", variables: {} },
  {
    // onReset runs before every (re)subscribe, and runs synchronously from
    // subscribe() itself when the socket is already open, so declare whatever
    // it clears above the call.
    onReset: () => { payload = undefined; },
    onUpdate: (diff) => {
      payload = merge(payload, diff);
      render(stripKeys(payload));
    },
    onError: (error) => console.error(error),
  },
);

// later
handle.dispose();
```

Routing query operations through a multi-emit observable, so that any query can
be live without being declared a subscription, is future work.

## Behaviour to expect

**Mutations are never queued.** A mutation attempted while the socket is down
rejects immediately with `lightning: not connected`, and one that was in flight
when the socket dropped rejects with `connection closed`. Neither waits for a
new socket. The server offers no idempotency, and a client cannot tell a
mutation that never arrived from one whose reply was lost, so replaying one
across a reconnect risks applying it twice. Retrying is left to the caller, who
knows whether the operation is safe to repeat. Queries and subscriptions are
safe to replay, and are replayed automatically.

**A query waits for a socket, but not for ever.** A query issued while the
connection is down is held and sent on whatever socket opens next, so a
reconnect is invisible to a screen that is still loading. Past `queryTimeoutMs`
(default 30s) the query gives up and rejects with `lightning: query timed out`,
because a promise that never settles gives Relay nothing to render, not even a
failure.

**`close()` is terminal for the work in flight.** In-flight mutations reject,
and every subscription ends through its `onError` with `lightning: connection
closed`. A later `connect()` opens a fresh socket for new operations. It does
not bring back the subscriptions `close()` ended, and `subscribe()` throws in
between. An earlier version dropped them quietly, leaving each caller holding a
handle to something that would never produce another value, with no error to say
why.

**File uploads are not supported.** The protocol sends a JSON text frame, and
there is no multipart request to attach files to, so
`commitMutation({uploadables})` rejects rather than sending the mutation with
the files silently missing. Upload out of band and pass the result as a
variable.

**A reconnect starts every subscription over.** The server keeps no session and
there are no resume tokens: the client is the only record of what is subscribed.
On reconnect every subscription is re-sent with its original id, the server's
previous value is empty again, and the first update is a fresh snapshot. The
accumulated payload is discarded at that point, because a diff against a value
the server no longer remembers is meaningless.

**Subscription errors are terminal.** The server closes a subscription when it
reports an error on it, so the error reaches Relay through `sink.error` rather
than being retried silently. Socket-level failures are different and are
retried transparently.

**`__key` never reaches Relay.** The server adds a `__key` field to objects of
keyed types so that it can line up array elements across executions. Nothing
selected it, and Relay would not expect it, so it is removed from a deep copy on
the way out. The accumulated payload keeps its keys for the next diff.

**Only the first execution of a subscription can fail visibly.** An error in a
*re*-execution is swallowed server-side and retried, so a live query will not
report a transient resolver failure.

## Options

| Option | Default | |
| --- | --- | --- |
| `url` | -- | Server URL. One of `url` or `connect` is required. |
| `connect` | -- | `() => WebSocketLike \| Promise<WebSocketLike>`. Takes precedence over `url`. |
| `webSocketImpl` | global `WebSocket` | A `new (url) => WebSocketLike`, for Node or for tests. |
| `connection` | -- | An existing `LightningConnection` to run on. |
| `connectionTimeoutMs` | 30000 | Covers `connect` and the socket opening. |
| `pingIntervalMs` | 30000 | Delay between a heartbeat reply and the next. |
| `pingTimeoutMs` | 30000 | How long a heartbeat reply may take. |
| `initialReconnectDelayMs` | 1000 | Doubles per failed attempt, then jittered. |
| `maxReconnectDelayMs` | 30000 | Ceiling for the reconnect delay. |
| `mutationTimeoutMs` | 10000 | How long a mutation waits for its reply. |
| `queryTimeoutMs` | 30000 | How long a query waits for its first payload. |
| `extensions` | -- | Opaque values sent with every operation; may be a function. |
| `autoConnect` | `true` | Set `false` for SSR or tests. |
| `logger` | -- | `{ warn(message, detail?) }`. |

`WebSocketLike` is the three members this package uses (`readyState`, `send`,
`close`) plus the four handler properties it assigns. Anything with those works,
so the DOM's `WebSocket` and Node's `ws` can both be passed despite their
incompatible event types.

A connection that stood up for at least ten seconds and then dropped is treated
as a network blip and retried immediately. Anything else is treated as a refusal
and backed off: a refused upgrade, a bad token, a server that accepts the socket
and hangs up on it. A blip buys one immediate retry. The retry that follows it
is backed off unless the connection in between also lasted ten seconds, so a
flapping server climbs the same curve as a server that never answers.

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

Ports of this format go wrong on two details:

- **An absent `"$"` means "same length, same order". It does not mean "empty".**
  `{"$": []}` is a legal diff meaning the new array is empty. Test for the key's
  presence rather than for truthiness.
- **`[start, length]` is a count. It is not an end index.** Reading `length` as
  an inclusive end produces a stale trailing element, loses the tail of any run
  that does not start at index 1, and can index past the end of the array.

```ts
import { merge } from "@hiett/lightning-relay";

merge(["a", "b", "c", "d"], { $: [[1, 3], -1], "3": "e" });
// => ["b", "c", "d", "e"]
```

`merge` never mutates its arguments, not even by freezing them. It freezes the
containers it builds, and shares the subtrees of the previous value that a diff
did not touch. A value taken out of a diff is copied on the way in, so nothing
it returns aliases the message you handed it.

## Development

```sh
npm install
npm run typecheck
npm test
npm run build
```

The merge test suite asserts against diffs generated by the Go differ
(`diff.Diff`), including every example in `diff/diff.go`'s package
documentation, so it tests the bytes a real server emits rather than a reading
of the spec. The connection and network suites run on a websocket the test
drives by hand (`src/testing/fakeWebSocket.ts`) and on fake timers, so backoff,
the heartbeat and the reconnect-and-replay sequence are asserted instead of
waited for.

`npm run typecheck` checks the tests too. `npm run build` is the only step that
excludes them, so nothing untested ends up in `dist`.
