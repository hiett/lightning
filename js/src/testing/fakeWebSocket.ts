/**
 * A websocket the test drives by hand.
 *
 * Nothing here happens on its own. A socket opens, delivers a frame and dies
 * only when a test says so, which is what makes the parts of the connection
 * that are pure timing -- backoff, the heartbeat, the window between a connect
 * function resolving and its socket being listened to -- something a test can
 * state rather than wait out.
 */

import type { ConnectFunction, WebSocketLike } from "../connection.js";
import type { ClientEnvelope } from "../protocol.js";

export const CONNECTING = 0;
export const OPEN = 1;
export const CLOSING = 2;
export const CLOSED = 3;

export class FakeWebSocket implements WebSocketLike {
  readyState = CONNECTING;

  /** Raw frames the client sent, newest last. */
  readonly sent: string[] = [];
  /** How many times the client closed this socket. */
  closeCount = 0;

  // Attached by the connection through a structural cast, so they are plain
  // properties rather than an addEventListener the connection never calls.
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((event: { data: unknown }) => void) | null = null;

  send(data: string): void {
    this.sent.push(data);
  }

  close(): void {
    this.closeCount++;
    this.readyState = CLOSED;
    // No close event: a real one arrives later, and by then the connection has
    // already invalidated this socket's callbacks. Firing it here would only
    // test the epoch guard, over and over.
  }

  // --- what the test drives --------------------------------------------

  /** The socket reaches its open event. */
  openSocket(): void {
    this.readyState = OPEN;
    this.onopen?.();
  }

  /** The server sends an envelope. */
  emit(envelope: unknown): void {
    this.deliver(JSON.stringify(envelope));
  }

  /** The server sends a frame verbatim, however malformed. */
  deliver(data: unknown): void {
    this.onmessage?.({ data });
  }

  /** The server hangs up. */
  drop(): void {
    this.readyState = CLOSED;
    this.onclose?.();
  }

  /** The socket reports a transport error. */
  fail(): void {
    this.onerror?.();
  }

  /** The frames the client sent, parsed. */
  frames(): ClientEnvelope[] {
    return this.sent.map((frame) => JSON.parse(frame) as ClientEnvelope);
  }

  /** The frames of one type, parsed. */
  framesOfType(type: ClientEnvelope["type"]): ClientEnvelope[] {
    return this.frames().filter((frame) => frame.type === type);
  }
}

export interface FakeTransport {
  /** Pass as the connection's `connect` option. */
  connect: ConnectFunction;
  /** Every socket the connection has asked for, oldest first. */
  readonly sockets: FakeWebSocket[];
  /** The most recent socket. Throws rather than returning undefined. */
  last(): FakeWebSocket;
}

/**
 * A connect function that hands out {@link FakeWebSocket}s and remembers them.
 *
 * `beforeReturning` runs on each new socket before the connect function
 * resolves, which is the only place a test can arrange for a socket to be
 * already open, or already dead, by the time the connection first looks at it.
 */
export function fakeTransport(
  beforeReturning?: (socket: FakeWebSocket, index: number) => void,
): FakeTransport {
  const sockets: FakeWebSocket[] = [];

  return {
    sockets,
    connect: () => {
      const socket = new FakeWebSocket();
      sockets.push(socket);
      beforeReturning?.(socket, sockets.length - 1);
      return socket;
    },
    last: () => {
      const socket = sockets[sockets.length - 1];
      if (socket === undefined) {
        throw new Error("no socket has been opened");
      }
      return socket;
    },
  };
}
