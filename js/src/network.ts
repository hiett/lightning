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
  Variables,
} from "relay-runtime";

import {
  LightningConnection,
  type LightningConnectionOptions,
} from "./connection.js";
import { merge, stripKeys, type MergeValue } from "./merge.js";
import type { OperationMessage } from "./protocol.js";

export interface LightningNetworkOptions extends LightningConnectionOptions {
  /**
   * An existing connection to run on, instead of opening one. Pass this when
   * the application needs to control the socket's lifetime, or to share one
   * socket between several Relay environments.
   */
  connection?: LightningConnection;
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
 * The return type is spelled this way because the network type is exported
 * under different names by relay-runtime's own typings and by
 * @types/relay-runtime.
 */
export function createLightningNetwork(
  options: LightningNetworkOptions = {},
): ReturnType<typeof Network.create> {
  const connection = options.connection ?? new LightningConnection(options);

  const fetchFn = (
    request: RequestParameters,
    variables: Variables,
    _cacheConfig: CacheConfig,
  ): Promise<GraphQLResponse> => {
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

    return fetchOnce(connection, operation);
  };

  // The return type is left to inference: relay-runtime exports its observable
  // as a value, and the two sets of typings in circulation disagree about
  // whether the same name is usable as a type.
  const subscribeFn = (request: RequestParameters, variables: Variables) =>
    Observable.create<GraphQLResponse>((sink) => {
      let operation: OperationMessage;
      try {
        operation = toOperationMessage(request, variables);
      } catch (error) {
        sink.error(asError(error));
        return;
      }

      // The accumulated payload for this subscription. Every diff is applied to
      // it, and it is what gets handed to Relay -- Relay is never shown a diff.
      let payload: MergeValue;

      const handle = connection.subscribe(operation, {
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
          // Terminal: the server has already ended this subscription. Relay
          // decides what to do about it, which is why there is no silent retry
          // here -- a live query that quietly stops updating is worse than one
          // that reports it stopped.
          sink.error(error);
        },
      });

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
 */
function fetchOnce(
  connection: LightningConnection,
  operation: OperationMessage,
): Promise<GraphQLResponse> {
  return new Promise<GraphQLResponse>((resolve, reject) => {
    let settled = false;

    // Safe to reference `handle` from the callbacks: every one of them is
    // driven by a socket message, so none can run before subscribe() returns.
    const handle = connection.subscribe(operation, {
      onReset: () => {
        // A query takes the first payload it is given, and a reset only means
        // the payload on its way is a fresh snapshot. Nothing accumulated yet.
      },
      onUpdate: (message) => {
        if (settled) {
          // A mutation on the same socket makes the server re-run every live
          // subscription at once, so a second update can arrive before the
          // unsubscribe below lands.
          return;
        }
        settled = true;
        handle.dispose();
        resolve(toGraphQLResponse(merge(undefined, message)));
      },
      onError: (error) => {
        if (settled) {
          return;
        }
        settled = true;
        handle.dispose();
        reject(error);
      },
    });
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

  // The cast is the JSON-to-Relay boundary: `data` is a payload shaped by the
  // query's selection set, which is exactly what Relay's normalizer expects,
  // but nothing in the type system connects the two.
  return { data } as unknown as GraphQLResponse;
}

function asError(error: unknown): Error {
  return error instanceof Error ? error : new Error(String(error));
}
