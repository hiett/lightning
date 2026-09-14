# @hiett/lightning-relay

A [Relay](https://relay.dev) network layer for
[lightning](https://github.com/hiett/lightning)'s reactive live-query websocket.

lightning does not re-send a payload when a live query's data changes. It
re-executes the query, diffs the new result against the one it last sent, and
writes only the difference. This package is the client's half of that contract:
it holds the accumulated payload for each live operation, applies each diff to
it, and hands Relay the complete result.

```ts
import { Environment, RecordSource, Store } from "relay-runtime";
import { createLightningNetwork } from "@hiett/lightning-relay";

const environment = new Environment({
  network: createLightningNetwork({ url: "ws://localhost:8080/graphql/live" }),
  store: new Store(new RecordSource()),
});
```

That is the whole setup. Queries, mutations and subscriptions all travel over
the one socket.

## What a subscription means here

A Relay subscription over this transport is a **live query**. The server
re-runs any query whose data was invalidated and pushes the difference, so the
payloads an operation emits have the query's own shape rather than a
subscription event's.

Write the operation as an ordinary GraphQL subscription against the server's
subscription root and Relay treats it like any other:

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

## Design notes

**Full payloads, not store patches.** Relay is handed a complete
`GraphQLResponse` every time. Translating diffs straight into Relay store
updates would be faster — Relay would re-normalise only what changed — but it
means reimplementing the reordering and replacement semantics against the
store's record API, where a mistake shows up as quietly wrong data rather than a
failed test. Full payloads are correct by construction, and Relay's
normalisation already skips writes for values that did not change. Diff-to-store
is future work.

**`__key` is stripped.** The server adds a `__key` field to objects of a keyed
type so that the diff algorithm can line array elements up across executions. It
is a correlation token, not part of any selection set, so it is removed from the
payload handed to Relay — by deep copy, so that the accumulated merge state
keeps its keys for the next diff and cannot be mutated by a consumer.

**A reconnect resets accumulated state.** The protocol has no session and no
resume token, so a reconnect re-subscribes everything from scratch and the next
message is a complete snapshot. Applying a snapshot to a stale accumulated value
would be merging against a state the server no longer has, so the accumulator is
cleared first.

**Persisted queries are not supported.** The protocol carries query text. Build
without `--persist-output`.

## Exports

| | |
|---|---|
| `createLightningNetwork(options)` | the Relay `Network` |
| `LightningConnection` | the protocol client, usable without Relay |
| `merge(accumulated, diff)` | the diff algorithm |
| `stripKeys(value)` | deep copy without `__key` fields |
| `parseServerEnvelope(raw)` | wire-envelope parsing |

Pass an existing `connection` in the options to share one socket between
several Relay environments, or to control the socket's lifetime yourself.

## Development

```
npm install
npm run typecheck
npm test
npm run build
```
