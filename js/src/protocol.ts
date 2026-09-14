/**
 * The lightning websocket wire protocol.
 *
 * One websocket, JSON text frames, one envelope per frame. The server's read
 * loop is graphql/server.go; these types mirror its `inEnvelope` and
 * `outEnvelope` structs, including their asymmetry (the client sends
 * `extensions`, the server returns `metadata`).
 *
 * `id` is chosen by the client and is opaque to the server. Subscriptions and
 * mutations share one id namespace in both directions, because the server
 * tracks both in a single map.
 */

import type { JsonValue } from "./merge.js";

/** The body of a `subscribe` or `mutate` envelope. */
export interface OperationMessage {
  query: string;
  /**
   * Which operation of the document to run. The server treats an empty name as
   * "the document's only operation" and rejects a document with several, so
   * sending the name is what makes multi-operation documents usable.
   */
  operationName?: string;
  variables: Record<string, unknown>;
}

/** An envelope sent by the client. */
export type ClientEnvelope =
  | {
      type: "subscribe";
      id: string;
      message: OperationMessage;
      extensions?: Record<string, unknown>;
    }
  | {
      type: "mutate";
      id: string;
      message: OperationMessage;
      extensions?: Record<string, unknown>;
    }
  | {
      type: "unsubscribe";
      id: string;
    }
  // The heartbeat carries no id: the server echoes whatever it is given, and
  // the reply to an id-less echo is the bare frame {"type":"echo"}.
  | { type: "echo" };

/** An envelope sent by the server. */
export type ServerEnvelope =
  | {
      type: "update";
      id: string;
      /**
       * A diff against the payload last sent on this subscription, to be
       * applied with merge(). The first update of a subscription is always a
       * complete snapshot, because the server has no previous value to diff
       * against; later updates are incremental, and an execution that changes
       * nothing produces no frame at all.
       */
      message: JsonValue;
      metadata?: Record<string, unknown>;
    }
  | {
      type: "result";
      id: string;
      /**
       * A mutation's payload. Always a complete snapshot wrapped as a
       * replacement, never incremental, because the server diffs it against
       * nothing.
       */
      message: JsonValue;
      metadata?: Record<string, unknown>;
    }
  | {
      type: "error";
      id: string;
      /**
       * A plain string, never a GraphQL `errors` array. The server reduces any
       * error it does not consider safe to disclose to "Internal server error".
       *
       * An error also *ends* the subscription server-side, so the same id can
       * be subscribed again afterwards.
       */
      message: string;
      metadata?: Record<string, unknown>;
    }
  | { type: "echo" };

/**
 * Parses a frame from the socket.
 *
 * Returns undefined for anything that is not a recognizable envelope, which the
 * caller should treat as a protocol error rather than silently ignore: the only
 * frames a lightning server sends are the four below.
 */
export function parseServerEnvelope(data: unknown): ServerEnvelope | undefined {
  if (typeof data !== "string") {
    // Binary frames are not part of the protocol.
    return undefined;
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(data);
  } catch {
    return undefined;
  }

  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    return undefined;
  }

  const envelope = parsed as {
    type?: unknown;
    id?: unknown;
    message?: unknown;
    metadata?: unknown;
  };

  if (envelope.type === "echo") {
    return { type: "echo" };
  }

  // Every other frame is addressed to a subscription or a mutation. The server
  // copies the id of the frame that caused the error onto an error, which for a
  // malformed frame can be the empty string; that is a valid envelope that
  // simply matches no request.
  if (typeof envelope.id !== "string") {
    return undefined;
  }

  const metadata =
    typeof envelope.metadata === "object" &&
    envelope.metadata !== null &&
    !Array.isArray(envelope.metadata)
      ? (envelope.metadata as Record<string, unknown>)
      : undefined;

  switch (envelope.type) {
    case "update":
    case "result":
      return {
        type: envelope.type,
        id: envelope.id,
        // An omitted message means "nothing changed": the server's envelope
        // drops a nil message rather than encoding it, and merging {} is a
        // no-op. A message that is present and null is a different frame and
        // stays one -- a bare null is the diff format's scalar replacement, so
        // collapsing it to {} would turn "this became null" into "nothing
        // happened".
        message: hasOwn(envelope, "message")
          ? (envelope.message as JsonValue)
          : {},
        metadata,
      };

    case "error":
      return {
        type: "error",
        id: envelope.id,
        message:
          typeof envelope.message === "string"
            ? envelope.message
            : "Internal server error",
        metadata,
      };

    default:
      return undefined;
  }
}

function hasOwn(object: object, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(object, key);
}
