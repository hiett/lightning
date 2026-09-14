/**
 * The websocket half of the lightning client: one socket, many subscriptions
 * and mutations multiplexed over it by id.
 *
 * This layer knows the protocol and nothing about Relay or about diffs. It
 * hands the diff messages it receives to the caller untouched.
 *
 * Three policies here are deliberate and worth stating up front, because all
 * three look like omissions:
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
 *
 *  - close() is terminal for the work in flight. Every subscription ends with
 *    an error and every in-flight mutation rejects, rather than being dropped
 *    quietly or held for a reconnect that may never come. The object itself is
 *    reusable: connect() opens a fresh socket for new operations, and does not
 *    resurrect what close() ended.
 */

import type { JsonValue } from "./merge.js";
import type { ClientEnvelope, OperationMessage } from "./protocol.js";
import { parseServerEnvelope } from "./protocol.js";

/** WebSocket ready states. Spelled out so no DOM global is required at runtime. */
const WS_OPEN = 1;
const WS_CLOSING = 2;
const WS_CLOSED = 3;

/**
 * How long a socket has to stay open before its loss counts as a blip rather
 * than a refusal. See #scheduleReconnect.
 */
const HEALTHY_CONNECTION_MS = 10_000;

export const ERROR_NOT_CONNECTED = "lightning: not connected";
export const ERROR_MUTATION_TIMEOUT = "lightning: mutation timed out";
export const ERROR_CLOSED = "lightning: connection closed";

const REASON_CONNECT_TIMEOUT = "connection timeout";
const REASON_PING_TIMEOUT = "ping timeout";
const REASON_CLOSE_CALLED = "close called";
const REASON_SOCKET_DEAD = "socket closed while connecting";

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
  /**
   * Terminal. Either the server ended this subscription on its side, or
   * {@link LightningConnection.close} ended it on ours. Nothing further arrives
   * for it, and a reconnect does not replay it.
   */
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
  /** When the current socket reached its open event, if it ever did. */
  #openedAtMs: number | undefined;

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

  /**
   * Opens the socket, and reopens one closed by {@link close}.
   *
   * It does not bring back the subscriptions close() ended: those were reported
   * as over, and a subscriber that has been told so has already let go. New
   * subscriptions run on the new socket as they always did.
   */
  connect(): void {
    this.#closed = false;
    if (this.#status === "closed") {
      this.#status = "idle";
    }
    // A reopen is a fresh start, not the continuation of whatever run of
    // failures preceded the close.
    this.#reconnectAttempt = 0;
    this.#maybeConnect();
  }

  /**
   * Closes the socket, ends every subscription, and stops reconnecting.
   *
   * This is terminal for the work in flight. In-flight mutations reject and
   * every subscription is ended through its onError, because the alternative --
   * dropping them quietly, as this used to -- leaves each caller holding a
   * handle to something that will never produce another value and never say
   * why, and a later connect() would not bring any of them back. Telling them
   * is what lets a caller resubscribe if it wants to.
   */
  close(): void {
    this.#closed = true;
    this.#shutdownSocket(REASON_CLOSE_CALLED);
    this.#status = "closed";

    // Drained before anyone is told: a handler is free to dispose its handle or
    // to subscribe again, and neither may run against a map still being walked.
    const ended = [...this.#subscriptions.values()];
    this.#subscriptions.clear();

    const error = new LightningConnectionError(
      `${ERROR_CLOSED} (${REASON_CLOSE_CALLED})`,
    );
    for (const subscription of ended) {
      this.#safely("onError", () => {
        subscription.handlers.onError(error);
      });
    }
  }

  /**
   * Starts a subscription and keeps it alive across reconnects.
   *
   * onReset can run synchronously from here, before the handle exists, when the
   * socket is already open: the caller's accumulated payload has to be cleared
   * before the snapshot answering this subscribe can arrive, and deferring it
   * would open a window in which a diff is applied to a value the server has
   * already forgotten. onUpdate and onError are only ever driven by socket
   * messages, so neither can run before this returns -- which is what makes it
   * safe for those two, and only those two, to close over the handle.
   *
   * Throws if the connection has been closed. Reporting that through onError
   * would mean calling a handler before the handle it wants to dispose exists.
   */
  subscribe(
    request: OperationMessage,
    handlers: SubscriptionHandlers,
  ): SubscriptionHandle {
    if (this.#closed) {
      throw new LightningConnectionError(ERROR_CLOSED);
    }

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

    if (socket.readyState === WS_CLOSING || socket.readyState === WS_CLOSED) {
      // The socket died while the connect function was still in flight, so its
      // error and close events fired before anything was listening for them.
      // Attaching handlers now and waiting would buy nothing but the whole
      // connection timeout, spent waiting for an event that has already been.
      this.#fail(epoch, REASON_SOCKET_DEAD);
      return;
    }

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
    this.#openedAtMs = Date.now();
    this.#clearConnectTimer();
    this.#schedulePing();

    for (const [id, subscription] of this.#subscriptions) {
      // Guarded one by one. onReset is caller-supplied code, and a throw from
      // one subscription's must not take the rest of the replay down with it,
      // leaving them registered, silent and never sent again.
      this.#safely("resubscribe", () => {
        this.#sendSubscribe(id, subscription);
      });
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
    // Read before the shutdown clears it.
    const wasHealthy =
      this.#openedAtMs !== undefined &&
      Date.now() - this.#openedAtMs >= HEALTHY_CONNECTION_MS;

    this.#shutdownSocket(reason);
    this.#scheduleReconnect(wasHealthy);
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
    this.#openedAtMs = undefined;
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

  #scheduleReconnect(wasHealthy: boolean): void {
    if (this.#closed) {
      return;
    }

    // A connection that stood up for a while and then dropped is treated as a
    // blip and retried at once, because making an application wait out a
    // backoff after a momentary loss of network is an outage for nothing.
    // Anything else is treated as a refusal -- a rejected upgrade, a bad token,
    // a server that is down, or one that accepts the socket and hangs up on it
    // -- and backed off.
    //
    // What bounds this is resetting the attempt counter rather than stepping
    // around it: an immediate retry is followed by another only if the
    // connection in between also lasted HEALTHY_CONNECTION_MS, so a server that
    // flaps climbs the same backoff curve as one that never connects at all. An
    // earlier version keyed this off "did any frame ever arrive", which a
    // server that greets you and immediately closes satisfies every time --
    // hundreds of connection attempts a second, forever.
    if (wasHealthy) {
      this.#reconnectAttempt = 0;
    }
    const delay = wasHealthy ? 0 : this.#backoffDelay();
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
    if (typeof extensions !== "function") {
      return extensions;
    }

    try {
      return extensions();
    } catch (error) {
      // The operation still goes out, without them. A server that needs these
      // values rejects it, and that reaches the caller as an error it can act
      // on; holding the frame back instead would leave the subscription
      // registered and permanently silent, with nothing to report it.
      this.#logger?.warn("lightning: extensions() threw", error);
      return undefined;
    }
  }

  /**
   * Runs a caller-supplied handler without letting it take the connection with
   * it. A handler that throws is the caller's bug, but these are reached from
   * loops over every subscription, where one bad handler would otherwise strand
   * all the ones after it.
   */
  #safely(what: string, run: () => void): void {
    try {
      run();
    } catch (error) {
      this.#logger?.warn(`lightning: ${what} threw`, error);
    }
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
