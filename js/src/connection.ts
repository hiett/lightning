/**
 * The websocket half of the lightning client: one socket, many subscriptions
 * and mutations multiplexed over it by id.
 *
 * This layer knows the protocol and nothing about Relay or about diffs. It
 * hands the diff messages it receives to the caller untouched.
 *
 * Two policies here are deliberate and worth stating up front, because both
 * look like omissions:
 *
 *  - There is no outbound queue. A frame sent while the socket is down is
 *    dropped, not buffered. Subscriptions do not need a queue because they are
 *    replayed in full whenever a socket opens, and mutations must not have one
 *    (see mutate).
 *
 *  - The client is the only record of what is subscribed. The server keeps no
 *    session and no resume tokens, so a reconnect re-subscribes everything from
 *    scratch and the server's first update on each is a complete snapshot.
 *    That is why a reconnect has to reset the caller's accumulated payload --
 *    a diff is meaningless against a value the server no longer remembers.
 */

import type { JsonValue } from "./merge.js";
import type { ClientEnvelope, OperationMessage } from "./protocol.js";
import { parseServerEnvelope } from "./protocol.js";

/** WebSocket.OPEN. Spelled out so no DOM global is required at runtime. */
const WS_OPEN = 1;

export const ERROR_NOT_CONNECTED = "lightning: not connected";
export const ERROR_MUTATION_TIMEOUT = "lightning: mutation timed out";

const REASON_CONNECT_TIMEOUT = "connection timeout";
const REASON_PING_TIMEOUT = "ping timeout";
const REASON_CLOSE_CALLED = "close called";

/** An error reported by the server in an `error` envelope. */
export class LightningServerError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "LightningServerError";
  }
}

/** A transport-level failure: the socket was down, or went down mid-flight. */
export class LightningConnectionError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "LightningConnectionError";
  }
}

/** The part of the WebSocket API this connection uses. */
export interface WebSocketLike {
  readonly readyState: number;
  send(data: string): void;
  close(code?: number, reason?: string): void;
}

/**
 * Opens a socket. It need not be open yet when it is returned.
 *
 * Returning a promise is what lets an application fetch a token or sign a URL
 * before connecting; the connection handles a close that happens while this is
 * still in flight.
 */
export type ConnectFunction = () => WebSocketLike | Promise<WebSocketLike>;

export interface Logger {
  warn(message: string, detail?: unknown): void;
}

export interface LightningConnectionOptions {
  /** Server URL, e.g. wss://example.com/graphql. Ignored if `connect` is set. */
  url?: string;
  /** Full control over how the socket is opened. Takes precedence over `url`. */
  connect?: ConnectFunction;
  /** WebSocket implementation, for environments without a global one. */
  webSocketImpl?: new (url: string) => WebSocketLike;

  /** How long `connect` and the socket's open event get, together. Default 30s. */
  connectionTimeoutMs?: number;
  /** Delay between a heartbeat reply and the next heartbeat. Default 30s. */
  pingIntervalMs?: number;
  /** How long a heartbeat reply may take before the socket is dead. Default 30s. */
  pingTimeoutMs?: number;

  /** First reconnect delay; doubles per failed attempt. Default 1s. */
  initialReconnectDelayMs?: number;
  /** Ceiling for the reconnect delay. Default 30s. */
  maxReconnectDelayMs?: number;

  /** How long a mutation waits for its reply. Default 10s. */
  mutationTimeoutMs?: number;

  /**
   * Opaque values passed through to the server's computation input, sent with
   * every subscribe and mutate. A function is called per operation, so it can
   * carry something that changes over time.
   */
  extensions?:
    | Record<string, unknown>
    | (() => Record<string, unknown> | undefined);

  /** Connect immediately. Default true; set false for SSR or tests. */
  autoConnect?: boolean;

  logger?: Logger;
}

export interface SubscriptionHandlers {
  /**
   * Discard the accumulated payload: what follows is a complete snapshot, not a
   * diff against what you are holding. Called before every (re)subscribe.
   */
  onReset(): void;
  /** A diff for this subscription. Apply it with merge(). */
  onUpdate(message: JsonValue): void;
  /** Terminal. The server has already ended this subscription on its side. */
  onError(error: Error): void;
}

export interface SubscriptionHandle {
  dispose(): void;
}

export type ConnectionStatus =
  | "idle"
  | "connecting"
  | "open"
  | "reconnecting"
  | "closed";

interface ActiveSubscription {
  request: OperationMessage;
  handlers: SubscriptionHandlers;
}

interface PendingMutation {
  resolve(message: JsonValue): void;
  reject(error: Error): void;
  timeout: ReturnType<typeof setTimeout>;
}

/**
 * The handler properties of a socket.
 *
 * Kept separate from {@link WebSocketLike} on purpose: the DOM's and ws's event
 * types are mutually unassignable under strictFunctionTypes (handler parameters
 * are contravariant), so requiring either one in the public option type would
 * reject the other. Handlers are attached through this structural view instead,
 * and only `data` is ever read from an event.
 */
interface SocketEventHandlers {
  onopen: (() => void) | null;
  onclose: (() => void) | null;
  onerror: (() => void) | null;
  onmessage: ((event: { data: unknown }) => void) | null;
}

export class LightningConnection {
  readonly #connectFunction: ConnectFunction;
  readonly #connectionTimeoutMs: number;
  readonly #pingIntervalMs: number;
  readonly #pingTimeoutMs: number;
  readonly #initialReconnectDelayMs: number;
  readonly #maxReconnectDelayMs: number;
  readonly #mutationTimeoutMs: number;
  readonly #extensionsOption: LightningConnectionOptions["extensions"];
  readonly #logger: Logger | undefined;

  readonly #subscriptions = new Map<string, ActiveSubscription>();
  readonly #mutations = new Map<string, PendingMutation>();

  #socket: WebSocketLike | undefined;
  #status: ConnectionStatus = "idle";
  #closed = false;

  /**
   * Bumped for every connect attempt and again when one is torn down. Callbacks
   * and timers capture it and compare, which is how a socket that has been
   * replaced stops being able to act: there is no way to reach the listener
   * after the teardown that invalidated it.
   */
  #epoch = 0;

  #nextRequestId = 0;
  #reconnectAttempt = 0;
  /** Whether the current socket ever delivered a message. */
  #hadSuccess = false;

  #connectTimer: ReturnType<typeof setTimeout> | undefined;
  #sendPingTimer: ReturnType<typeof setTimeout> | undefined;
  #receivePingTimer: ReturnType<typeof setTimeout> | undefined;
  #reconnectTimer: ReturnType<typeof setTimeout> | undefined;

  constructor(options: LightningConnectionOptions = {}) {
    this.#connectFunction = resolveConnectFunction(options);
    this.#connectionTimeoutMs = options.connectionTimeoutMs ?? 30_000;
    this.#pingIntervalMs = options.pingIntervalMs ?? 30_000;
    this.#pingTimeoutMs = options.pingTimeoutMs ?? 30_000;
    this.#initialReconnectDelayMs = options.initialReconnectDelayMs ?? 1_000;
    this.#maxReconnectDelayMs = options.maxReconnectDelayMs ?? 30_000;
    this.#mutationTimeoutMs = options.mutationTimeoutMs ?? 10_000;
    this.#extensionsOption = options.extensions;
    this.#logger = options.logger;

    if (options.autoConnect !== false) {
      this.#maybeConnect();
    }
  }

  get status(): ConnectionStatus {
    return this.#status;
  }

  /** Opens the socket, and reopens one closed by {@link close}. */
  connect(): void {
    this.#closed = false;
    if (this.#status === "closed") {
      this.#status = "idle";
    }
    this.#maybeConnect();
  }

  /**
   * Closes the socket and stops reconnecting.
   *
   * In-flight mutations reject. Subscriptions are dropped without being
   * notified: a caller that tore the connection down is not waiting to be told.
   */
  close(): void {
    this.#closed = true;
    this.#shutdownSocket(REASON_CLOSE_CALLED);
    this.#status = "closed";
    this.#subscriptions.clear();
  }

  /**
   * Starts a subscription and keeps it alive across reconnects.
   *
   * The handlers are never called before this returns: all three are driven by
   * socket messages.
   */
  subscribe(
    request: OperationMessage,
    handlers: SubscriptionHandlers,
  ): SubscriptionHandle {
    const id = this.#makeId();
    const subscription: ActiveSubscription = { request, handlers };
    this.#subscriptions.set(id, subscription);

    if (this.#status === "open") {
      this.#sendSubscribe(id, subscription);
    } else {
      // Nothing to send yet. Whenever a socket next opens it replays every
      // registered subscription, this one included.
      this.#maybeConnect();
    }

    return {
      dispose: () => {
        if (!this.#subscriptions.delete(id)) {
          return;
        }
        // Unsubscribing an id the server does not know is a silent no-op there,
        // so this is safe even if the socket has been replaced since.
        this.#send({ id, type: "unsubscribe" });
      },
    };
  }

  /**
   * Runs a mutation and resolves with its `result` message.
   *
   * Rejects immediately when the socket is not open rather than waiting for one.
   * That is a safety property, not an oversight: the server offers no
   * idempotency, and a client cannot tell a mutation that never arrived from
   * one whose reply was lost, so a mutation held across a reconnect risks being
   * applied twice. Failing loudly leaves the decision to retry with the caller,
   * who knows whether the operation is safe to repeat.
   */
  mutate(request: OperationMessage): Promise<JsonValue> {
    if (this.#status !== "open") {
      return Promise.reject(new LightningConnectionError(ERROR_NOT_CONNECTED));
    }

    const id = this.#makeId();
    this.#send({
      id,
      type: "mutate",
      message: request,
      extensions: this.#extensions(),
    });

    return new Promise<JsonValue>((resolve, reject) => {
      // Safe to register after sending: a reply cannot be processed until this
      // executor has returned.
      const timeout = setTimeout(() => {
        this.#mutations.delete(id);
        reject(new LightningConnectionError(ERROR_MUTATION_TIMEOUT));
      }, this.#mutationTimeoutMs);

      this.#mutations.set(id, { resolve, reject, timeout });
    });
  }

  // --- connecting -------------------------------------------------------

  #maybeConnect(): void {
    // "connecting" and "open" are already there; "reconnecting" is on its way
    // and must not be hurried past its backoff.
    if (this.#closed || this.#status !== "idle") {
      return;
    }
    void this.#openSocket();
  }

  async #openSocket(): Promise<void> {
    if (this.#closed || this.#status === "connecting" || this.#status === "open") {
      return;
    }

    this.#status = "connecting";
    const epoch = ++this.#epoch;

    // One timeout covers both the connect function and the socket reaching its
    // open event, because either can hang.
    this.#connectTimer = setTimeout(() => {
      this.#fail(epoch, REASON_CONNECT_TIMEOUT);
    }, this.#connectionTimeoutMs);

    let socket: WebSocketLike;
    try {
      socket = await this.#connectFunction();
    } catch (error) {
      this.#fail(epoch, `connect failed: ${describeError(error)}`);
      return;
    }

    if (epoch !== this.#epoch) {
      // Torn down while the connect function was in flight. This socket was
      // never stored, so nothing else will ever close it.
      closeQuietly(socket);
      return;
    }

    this.#socket = socket;

    const events = socket as unknown as SocketEventHandlers;
    events.onopen = () => {
      if (epoch === this.#epoch) {
        this.#handleOpen();
      }
    };
    events.onmessage = (event) => {
      if (epoch === this.#epoch) {
        this.#handleMessage(event.data);
      }
    };
    events.onerror = () => {
      this.#fail(epoch, "socket error");
    };
    events.onclose = () => {
      this.#fail(epoch, "socket closed");
    };

    // A connect function may hand back a socket that is already open, in which
    // case the open event has been and gone.
    if (socket.readyState === WS_OPEN) {
      this.#handleOpen();
    }
  }

  #handleOpen(): void {
    if (this.#status === "open") {
      return;
    }
    this.#status = "open";
    this.#clearConnectTimer();
    this.#schedulePing();

    for (const [id, subscription] of this.#subscriptions) {
      this.#sendSubscribe(id, subscription);
    }
  }

  #sendSubscribe(id: string, subscription: ActiveSubscription): void {
    // This subscription is starting over on the server, with no previous value
    // to diff against, so the update that follows is a complete snapshot.
    // Whatever the caller accumulated describes a state nobody remembers.
    subscription.handlers.onReset();

    this.#send({
      id,
      type: "subscribe",
      message: subscription.request,
      extensions: this.#extensions(),
    });
  }

  // --- receiving --------------------------------------------------------

  #handleMessage(data: unknown): void {
    // Any frame proves the socket works, which is what distinguishes a blip
    // from a server that refuses us. See #scheduleReconnect.
    this.#hadSuccess = true;
    this.#reconnectAttempt = 0;

    const envelope = parseServerEnvelope(data);
    if (envelope === undefined) {
      this.#logger?.warn("lightning: unrecognized frame", data);
      return;
    }

    if (envelope.type === "echo") {
      this.#handleEcho();
      return;
    }

    switch (envelope.type) {
      case "update": {
        // An update for an unknown id is normal: it can have been in flight
        // when the subscription was disposed.
        this.#subscriptions.get(envelope.id)?.handlers.onUpdate(envelope.message);
        return;
      }

      case "result": {
        const mutation = this.#mutations.get(envelope.id);
        if (mutation !== undefined) {
          this.#settleMutation(envelope.id, mutation);
          mutation.resolve(envelope.message);
        }
        return;
      }

      case "error": {
        const error = new LightningServerError(envelope.message);

        // Ids are unique across both maps, so at most one of these matches.
        const subscription = this.#subscriptions.get(envelope.id);
        if (subscription !== undefined) {
          // The server closed this subscription before sending the error, so
          // there is nothing left to unsubscribe from and nothing to replay on
          // the next reconnect.
          this.#subscriptions.delete(envelope.id);
          subscription.handlers.onError(error);
        }

        const mutation = this.#mutations.get(envelope.id);
        if (mutation !== undefined) {
          this.#settleMutation(envelope.id, mutation);
          mutation.reject(error);
        }
        return;
      }
    }
  }

  #settleMutation(id: string, mutation: PendingMutation): void {
    clearTimeout(mutation.timeout);
    this.#mutations.delete(id);
  }

  // --- heartbeat --------------------------------------------------------
  //
  // The server sets no read deadline and uses no websocket-level pings, so this
  // application-level echo is the only thing that detects a connection that has
  // stopped carrying traffic without being closed.

  #schedulePing(): void {
    this.#clearPingTimers();
    this.#sendPingTimer = setTimeout(() => {
      this.#sendPing();
    }, this.#pingIntervalMs);
  }

  #sendPing(): void {
    this.#sendPingTimer = undefined;
    this.#send({ type: "echo" });

    // Sending and awaiting are mutually exclusive: there is never more than one
    // heartbeat outstanding.
    this.#receivePingTimer = setTimeout(() => {
      this.#failCurrent(REASON_PING_TIMEOUT);
    }, this.#pingTimeoutMs);
  }

  #handleEcho(): void {
    this.#schedulePing();
  }

  // --- tearing down -----------------------------------------------------

  /** Fails the socket identified by `epoch`, if it is still the current one. */
  #fail(epoch: number, reason: string): void {
    if (epoch !== this.#epoch) {
      return;
    }
    this.#failCurrent(reason);
  }

  /**
   * Fails the current socket. Safe to call from a timer without an epoch check:
   * every timer is cleared when its socket goes away, so one that fires is
   * always talking about the socket that is current now.
   */
  #failCurrent(reason: string): void {
    this.#shutdownSocket(reason);
    this.#scheduleReconnect();
  }

  #shutdownSocket(reason: string): void {
    // Invalidate every callback and timer still referring to this socket.
    this.#epoch++;

    this.#clearConnectTimer();
    this.#clearPingTimers();
    if (this.#reconnectTimer !== undefined) {
      clearTimeout(this.#reconnectTimer);
      this.#reconnectTimer = undefined;
    }

    const socket = this.#socket;
    this.#socket = undefined;
    this.#status = "idle";
    if (socket !== undefined) {
      closeQuietly(socket);
    }

    // A mutation that was in flight may or may not have been applied, and there
    // is no way to find out, so it fails rather than being retried.
    if (this.#mutations.size > 0) {
      const error = new LightningConnectionError(
        `lightning: connection closed (${reason})`,
      );
      for (const mutation of this.#mutations.values()) {
        clearTimeout(mutation.timeout);
        mutation.reject(error);
      }
      this.#mutations.clear();
    }

    // Subscriptions are intentionally kept: they are replayed on the next open.
  }

  #scheduleReconnect(): void {
    if (this.#closed) {
      return;
    }

    // A socket that delivered at least one message is treated as a blip and
    // retried at once. One that never produced anything is treated as a refusal
    // -- a rejected upgrade, a bad token, a server that is down -- and backed
    // off, so a failing server does not get hammered.
    const delay = this.#hadSuccess ? 0 : this.#backoffDelay();
    this.#hadSuccess = false;
    this.#status = "reconnecting";

    this.#reconnectTimer = setTimeout(() => {
      this.#reconnectTimer = undefined;
      this.#status = "idle";
      void this.#openSocket();
    }, delay);
  }

  #backoffDelay(): number {
    const windowMs = Math.min(
      this.#maxReconnectDelayMs,
      this.#initialReconnectDelayMs * 2 ** this.#reconnectAttempt,
    );
    this.#reconnectAttempt++;

    // Half the window fixed and half jittered, so that a fleet that lost the
    // same server does not come back in lockstep and knock it over again.
    return windowMs / 2 + Math.random() * (windowMs / 2);
  }

  #clearConnectTimer(): void {
    if (this.#connectTimer !== undefined) {
      clearTimeout(this.#connectTimer);
      this.#connectTimer = undefined;
    }
  }

  #clearPingTimers(): void {
    if (this.#sendPingTimer !== undefined) {
      clearTimeout(this.#sendPingTimer);
      this.#sendPingTimer = undefined;
    }
    if (this.#receivePingTimer !== undefined) {
      clearTimeout(this.#receivePingTimer);
      this.#receivePingTimer = undefined;
    }
  }

  // --- sending ----------------------------------------------------------

  #send(envelope: ClientEnvelope): void {
    const socket = this.#socket;
    if (socket === undefined || socket.readyState !== WS_OPEN) {
      // Dropped, not queued. See the note at the top of the file.
      return;
    }

    try {
      socket.send(JSON.stringify(envelope));
    } catch (error) {
      this.#logger?.warn("lightning: send failed", error);
    }
  }

  #extensions(): Record<string, unknown> | undefined {
    const extensions = this.#extensionsOption;
    return typeof extensions === "function" ? extensions() : extensions;
  }

  #makeId(): string {
    // Subscriptions and mutations share one namespace because the server tracks
    // both in a single map: reusing an id across the two would clobber a live
    // subscription.
    return (this.#nextRequestId++).toString();
  }
}

function resolveConnectFunction(
  options: LightningConnectionOptions,
): ConnectFunction {
  if (options.connect !== undefined) {
    return options.connect;
  }

  const url = options.url;
  if (url === undefined) {
    throw new Error("lightning: one of `url` or `connect` is required");
  }

  // Annotated rather than inferred: without it the fallback widens to a union
  // of two constructor types and `new impl(...)` stops resolving.
  const impl: (new (url: string) => WebSocketLike) | undefined =
    options.webSocketImpl ??
    (typeof WebSocket === "undefined" ? undefined : WebSocket);
  if (impl === undefined) {
    throw new Error(
      "lightning: no global WebSocket; pass `webSocketImpl` or `connect`",
    );
  }

  return () => new impl(url);
}

function closeQuietly(socket: WebSocketLike): void {
  try {
    socket.close();
  } catch {
    // Closing an already-dead socket throws in some implementations, and there
    // is nothing useful to do about it.
  }
}

function describeError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
