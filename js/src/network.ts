/**
 * The Relay network layer.
 *
 * Relay wants a complete `GraphQLResponse` every time a result changes; the
 * server sends a diff. This module owns that impedance mismatch: it keeps the
 * accumulated payload for each live operation, applies each diff to it, and
 * hands Relay the whole thing.
 *
 * That is a deliberate trade. Translating diffs straight into Relay store
 * updates would be faster -- Relay would re-normalize only what changed instead
 * of the entire payload -- but it means reimplementing the reorder and
 * replacement semantics against the store's record API, where a mistake shows
 * up as quietly wrong data rather than a failed test. Full payloads are
 * correct by construction, and Relay's normalization already skips writes for
 * fields whose values did not change. Diff-to-store is future work.
 */

import { Network, Observable } from "relay-runtime";
import type {
  CacheConfig,
  GraphQLResponse,
  RequestParameters,
  UploadableMap,
  Variables,
} from "relay-runtime";

import {
  LightningConnection,
  LightningConnectionError,
  type LightningConnectionOptions,
  type SubscriptionHandle,
} from "./connection.js";
import { merge, stripKeys, type MergeValue } from "./merge.js";
import type { OperationMessage } from "./protocol.js";

export const ERROR_QUERY_TIMEOUT = "lightning: query timed out";
export const ERROR_UPLOADABLES =
  "lightning: file uploads are not supported by this protocol; the websocket " +
  "carries JSON text frames only. Upload out of band and send the result as a " +
  "variable.";
const ERROR_NOT_AN_OBJECT =
  "lightning: server sent a payload that is not an object";

export interface LightningNetworkOptions extends LightningConnectionOptions {
  /**
   * An existing connection to run on, instead of opening one. Pass this when
   * the application needs to control the socket's lifetime, or to share one
   * socket between several Relay environments.
   */
  connection?: LightningConnection;

  /**
   * How long a query waits for its first payload before rejecting. Default 30s.
   *
   * Unlike a mutation, a query is allowed to wait for a socket -- it is safe to
   * replay, so it is held and sent whenever one opens. It is not allowed to
   * wait forever: a Relay query that never settles is a spinner with no end and
   * no error, which is the one outcome an application cannot render.
   */
  queryTimeoutMs?: number;
}

/**
 * Builds a Relay `Network` backed by a lightning websocket.
 *
 * Queries and mutations go through `fetchFn`, subscriptions through
 * `subscribeFn`. Note what a "subscription" means here: the server re-runs any
 * query whose data was invalidated and pushes the difference, so a Relay
 * subscription operation over this transport is a live query, and the payloads
 * it emits are the query's own shape rather than a subscription event.
 *
 * The return type is named off the factory because @types/relay-runtime -- the
 * only typings there are, relay-runtime itself shipping Flow and no .d.ts --
 * exports the interface under a different name (INetwork) than the value.
 */
export function createLightningNetwork(
  options: LightningNetworkOptions = {},
): ReturnType<typeof Network.create> {
  const connection = options.connection ?? new LightningConnection(options);
  const queryTimeoutMs = options.queryTimeoutMs ?? 30_000;

  const fetchFn = (
    request: RequestParameters,
    variables: Variables,
    _cacheConfig: CacheConfig,
    uploadables?: UploadableMap | null,
  ): Promise<GraphQLResponse> => {
    if (uploadables != null && Object.keys(uploadables).length > 0) {
      // commitMutation({uploadables}) is answered rather than obeyed. There is
      // no multipart request to attach files to here -- there is no request at
      // all, only a JSON frame -- and sending the mutation without them would
      // apply it with the files silently missing.
      return Promise.reject(new Error(ERROR_UPLOADABLES));
    }

    let operation: OperationMessage;
    try {
      operation = toOperationMessage(request, variables);
    } catch (error) {
      return Promise.reject(asError(error));
    }

    if (request.operationKind === "mutation") {
      return connection.mutate(operation).then((message) =>
        // A result is a complete payload wrapped as a replacement -- the server
        // diffs it against nothing -- so it still has to go through merge to be
        // unwrapped. Handing Relay the wrapper would give it a one-element list
        // where the data should be.
        toGraphQLResponse(merge(undefined, message)),
      );
    }

    return fetchOnce(connection, operation, queryTimeoutMs);
  };

  // The return type is left to inference, because naming it would mean naming
  // RelayObservable, which @types/relay-runtime exports only under the alias
  // used for the value.
  const subscribeFn = (request: RequestParameters, variables: Variables) =>
    Observable.create<GraphQLResponse>((sink) => {
      // The accumulated payload for this subscription. Every diff is applied to
      // it, and it is what gets handed to Relay -- Relay is never shown a diff.
      // Declared before subscribe(), which calls onReset synchronously when the
      // socket is already open.
      let payload: MergeValue;

      let handle: SubscriptionHandle;
      try {
        handle = connection.subscribe(toOperationMessage(request, variables), {
          onReset: () => {
            // The socket dropped and the subscription is starting over. What
            // follows is a complete snapshot, and applying it to the old value
            // would be merging against a state the server no longer has.
            payload = undefined;
          },
          onUpdate: (message) => {
            payload = merge(payload, message);
            sink.next(toGraphQLResponse(payload));
          },
          onError: (error) => {
            // Terminal: the server, or a close() on this side, has already
            // ended this subscription. Relay decides what to do about it, which
            // is why there is no silent retry here -- a live query that quietly
            // stops updating is worse than one that reports it stopped.
            sink.error(error);
          },
        });
      } catch (error) {
        // An unusable operation, or a connection that has been closed.
        sink.error(asError(error));
        return;
      }

      return () => {
        handle.dispose();
      };
    });

  return Network.create(fetchFn, subscribeFn);
}

/**
 * Runs a query: subscribe, take the first payload, unsubscribe.
 *
 * The first update of a subscription is always a complete snapshot, so one
 * frame is a whole answer. Later frames would be the live-query updates this
 * caller did not ask for.
 *
 * A query issued while the socket is down is held, not rejected -- it is safe
 * to replay, so it goes out on whatever socket opens next, which is what makes
 * a reconnect invisible to an application that is merely loading a screen. The
 * timeout is the limit of that patience: past it the query rejects, because a
 * promise that never settles gives Relay nothing to render, not even a failure.
 */
function fetchOnce(
  connection: LightningConnection,
  operation: OperationMessage,
  timeoutMs: number,
): Promise<GraphQLResponse> {
  return new Promise<GraphQLResponse>((resolve, reject) => {
    let settled = false;

    // Safe to reference `handle` and `timeout` from onUpdate and onError: both
    // are driven by socket messages, so neither can run before subscribe()
    // returns. onReset can run synchronously from inside it, which is why it
    // touches nothing out here.
    const handle = connection.subscribe(operation, {
      onReset: () => {
        // A query takes the first payload it is given, and a reset only means
        // the payload on its way is a fresh snapshot. Nothing accumulated yet.
      },
      onUpdate: (message) => {
        if (claim()) {
          resolve(toGraphQLResponse(merge(undefined, message)));
        }
      },
      onError: (error) => {
        if (claim()) {
          reject(error);
        }
      },
    });

    const timeout = setTimeout(() => {
      if (claim()) {
        reject(new LightningConnectionError(ERROR_QUERY_TIMEOUT));
      }
    }, timeoutMs);

    /**
     * Takes ownership of the one outcome this query gets, and lets go of the
     * subscription behind it. A second update can arrive before the unsubscribe
     * lands -- a mutation on the same socket re-runs every live subscription at
     * once -- so losing the race has to be ordinary rather than an error.
     */
    function claim(): boolean {
      if (settled) {
        return false;
      }
      settled = true;
      clearTimeout(timeout);
      handle.dispose();
      return true;
    }
  });
}

function toOperationMessage(
  request: RequestParameters,
  variables: Variables,
): OperationMessage {
  if (request.text == null) {
    throw new Error(
      `lightning: operation "${request.name}" has no query text. Persisted ` +
        "queries are not supported by this protocol; build without " +
        "--persist-output.",
    );
  }

  return {
    query: request.text,
    // relay-compiler names every operation, and the name it records matches the
    // one in the document text. Sending it is what makes the server able to
    // pick an operation out of a multi-operation document at all.
    operationName: request.name,
    variables: variables as Record<string, unknown>,
  };
}

function toGraphQLResponse(payload: MergeValue): GraphQLResponse {
  // stripKeys deep-copies, which does two things at once: it removes the
  // server's __key correlation fields, which are not in the selection set and
  // would make Relay's normalizer complain about an unexpected field, and it
  // keeps Relay away from the live merge state -- the accumulated value has to
  // keep its keys for the next diff, and has to not be mutated by a consumer.
  const data = stripKeys(payload);

  if (data === null || typeof data !== "object" || Array.isArray(data)) {
    // A GraphQL result is the object its selection set describes. Anything else
    // means the accumulated payload was replaced by a scalar or deleted
    // outright, neither of which a lightning server does. Relay is told in the
    // one way it always understands: `{data: null}` with no errors is the shape
    // it throws on, and the throw says nothing about where the trouble came
    // from.
    return { errors: [{ message: ERROR_NOT_AN_OBJECT }] };
  }

  // The cast is the JSON-to-Relay boundary: `data` is a payload shaped by the
  // query's selection set, which is exactly what Relay's normalizer expects,
  // but nothing in the type system connects the two.
  return { data } as unknown as GraphQLResponse;
}

function asError(error: unknown): Error {
  return error instanceof Error ? error : new Error(String(error));
}
