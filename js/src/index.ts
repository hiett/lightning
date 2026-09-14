/**
 * A Relay network layer for lightning's reactive live-query websocket.
 *
 * Most applications need only createLightningNetwork. The connection and the
 * merge algorithm are exported because both are useful on their own: the
 * connection speaks the protocol without any Relay involvement, and merge is
 * what turns a stream of diffs back into payloads.
 */

export { createLightningNetwork } from "./network.js";
export type { LightningNetworkOptions } from "./network.js";

export {
  LightningConnection,
  LightningConnectionError,
  LightningServerError,
  ERROR_MUTATION_TIMEOUT,
  ERROR_NOT_CONNECTED,
} from "./connection.js";
export type {
  ConnectFunction,
  ConnectionStatus,
  LightningConnectionOptions,
  Logger,
  SubscriptionHandle,
  SubscriptionHandlers,
  WebSocketLike,
} from "./connection.js";

export { merge, stripKeys, KEY_FIELD } from "./merge.js";
export type {
  JsonObject,
  JsonScalar,
  JsonValue,
  MergeValue,
} from "./merge.js";

export { parseServerEnvelope } from "./protocol.js";
export type {
  ClientEnvelope,
  OperationMessage,
  ServerEnvelope,
} from "./protocol.js";
