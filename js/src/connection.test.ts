import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  ERROR_CLOSED,
  ERROR_MUTATION_TIMEOUT,
  ERROR_NOT_CONNECTED,
  LightningConnection,
  type LightningConnectionOptions,
  type SubscriptionHandlers,
} from "./connection.js";
import type { ClientEnvelope, OperationMessage } from "./protocol.js";
import { CLOSED, OPEN, fakeTransport } from "./testing/fakeWebSocket.js";

/**
 * Every test here runs on fake timers with Math.random pinned, because most of
 * what this file is testing is timing: a backoff delay, a heartbeat, the window
 * between a connect function resolving and its socket being listened to. With
 * the jitter pinned to 0 a backoff delay is exactly half its window, so a test
 * can say when a reconnect is due instead of waiting to see one.
 */
beforeEach(() => {
  vi.useFakeTimers();
  vi.spyOn(Math, "random").mockReturnValue(0);
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

/** Lets the connect function's promise settle and its handlers be attached. */
async function settle(): Promise<void> {
  await vi.advanceTimersByTimeAsync(0);
}

/**
 * Defaults chosen to keep one mechanism out of another's way: the heartbeat is
 * far enough out that no test trips it by accident, and the first backoff
 * window is 1000ms, so with the jitter pinned the first reconnect is due at
 * exactly 500ms.
 */
const DEFAULTS: LightningConnectionOptions = {
  connectionTimeoutMs: 30_000,
  pingIntervalMs: 60_000,
  pingTimeoutMs: 5_000,
  initialReconnectDelayMs: 1_000,
  maxReconnectDelayMs: 30_000,
  mutationTimeoutMs: 10_000,
};

const FIRST_BACKOFF_MS = 500;
/** How long a socket has to live before its loss counts as a blip. */
const HEALTHY_MS = 10_000;

function query(name: string): OperationMessage {
  return { query: `query ${name} { x }`, operationName: name, variables: {} };
}

function spyHandlers(): SubscriptionHandlers {
  return { onReset: vi.fn(), onUpdate: vi.fn(), onError: vi.fn() };
}

/** Builds a connection on fake sockets and returns it with its transport. */
function connect(options: LightningConnectionOptions = {}) {
  const transport = fakeTransport();
  const connection = new LightningConnection({
    ...DEFAULTS,
    connect: transport.connect,
    ...options,
  });
  return { transport, connection };
}

/** Builds a connection and takes it all the way to open. */
async function connectOpen(options: LightningConnectionOptions = {}) {
  const started = connect(options);
  await settle();
  started.transport.last().openSocket();
  return started;
}

/** Only a subscribe or a mutate carries extensions; the other two do not. */
function extensionsOf(frames: ClientEnvelope[]): unknown[] {
  return frames.map((frame) =>
    "extensions" in frame ? frame.extensions : undefined,
  );
}

function subscribeFrames(frames: ClientEnvelope[]): Array<{
  id: string;
  operationName: string | undefined;
}> {
  return frames
    .filter((frame) => frame.type === "subscribe")
    .map((frame) => ({
      id: frame.id,
      operationName: frame.message.operationName,
    }));
}

describe("connecting", () => {
  it("asks for a socket as soon as it is built", () => {
    const { transport, connection } = connect();

    expect(transport.sockets).toHaveLength(1);
    expect(connection.status).toBe("connecting");
  });

  it("stays idle when autoConnect is off, and connects on demand", async () => {
    const { transport, connection } = connect({ autoConnect: false });

    expect(transport.sockets).toHaveLength(0);
    expect(connection.status).toBe("idle");

    connection.connect();
    await settle();
    transport.last().openSocket();

    expect(connection.status).toBe("open");
  });

  it("accepts a socket that is already open when the connect function returns", async () => {
    // A connect function is free to hand back a live socket, in which case the
    // open event has been and gone before anything was listening for it.
    const transport = fakeTransport((socket) => {
      socket.readyState = OPEN;
    });
    const connection = new LightningConnection({
      ...DEFAULTS,
      connect: transport.connect,
    });

    await settle();

    expect(connection.status).toBe("open");
  });

  it("does not wait out the connect timeout for a socket that is already dead", async () => {
    // The socket died while the connect function was still in flight, so its
    // close event fired into nothing. Waiting for another one means waiting the
    // full connectionTimeoutMs -- 30 seconds of "connecting" for an answer that
    // arrived before anyone asked.
    const transport = fakeTransport((socket) => {
      socket.readyState = CLOSED;
    });
    const connection = new LightningConnection({
      ...DEFAULTS,
      connect: transport.connect,
    });

    await settle();
    expect(connection.status).toBe("reconnecting");

    await vi.advanceTimersByTimeAsync(FIRST_BACKOFF_MS);
    expect(transport.sockets).toHaveLength(2);
  });

  it("gives up on a socket that never opens, and retries", async () => {
    const { transport, connection } = connect({ connectionTimeoutMs: 1_000 });
    await settle();

    await vi.advanceTimersByTimeAsync(1_000);
    expect(transport.sockets[0]?.closeCount).toBe(1);
    expect(connection.status).toBe("reconnecting");

    await vi.advanceTimersByTimeAsync(FIRST_BACKOFF_MS);
    expect(transport.sockets).toHaveLength(2);
  });

  it("retries when the connect function itself fails", async () => {
    let calls = 0;
    const connection = new LightningConnection({
      ...DEFAULTS,
      connect: () => {
        calls++;
        return Promise.reject(new Error("no token"));
      },
    });

    await settle();
    expect(connection.status).toBe("reconnecting");

    await vi.advanceTimersByTimeAsync(FIRST_BACKOFF_MS);
    expect(calls).toBe(2);
  });
});

describe("subscribing", () => {
  it("sends subscribe as soon as the socket is open", async () => {
    const { transport, connection } = connect();
    await settle();

    connection.subscribe(query("A"), spyHandlers());
    expect(transport.last().sent).toEqual([]);

    transport.last().openSocket();
    expect(subscribeFrames(transport.last().frames())).toEqual([
      { id: "0", operationName: "A" },
    ]);
  });

  it("sends subscribe immediately when the socket is already open", async () => {
    const { transport, connection } = await connectOpen();

    connection.subscribe(query("A"), spyHandlers());

    expect(subscribeFrames(transport.last().frames())).toEqual([
      { id: "0", operationName: "A" },
    ]);
  });

  it("gives each operation its own id", async () => {
    const { transport, connection } = await connectOpen();

    connection.subscribe(query("A"), spyHandlers());
    connection.subscribe(query("B"), spyHandlers());

    expect(subscribeFrames(transport.last().frames())).toEqual([
      { id: "0", operationName: "A" },
      { id: "1", operationName: "B" },
    ]);
  });

  it("routes an update to the subscription it is addressed to", async () => {
    const { transport, connection } = await connectOpen();
    const a = spyHandlers();
    const b = spyHandlers();
    connection.subscribe(query("A"), a);
    connection.subscribe(query("B"), b);

    transport.last().emit({ id: "1", type: "update", message: { n: 1 } });

    expect(b.onUpdate).toHaveBeenCalledWith({ n: 1 });
    expect(a.onUpdate).not.toHaveBeenCalled();
  });

  it("ignores an update for an id it does not know", async () => {
    const { transport, connection } = await connectOpen();
    const a = spyHandlers();
    connection.subscribe(query("A"), a);

    // Normal: an update can be in flight when a subscription is disposed.
    transport.last().emit({ id: "99", type: "update", message: {} });

    expect(a.onUpdate).not.toHaveBeenCalled();
  });

  it("unsubscribes on dispose and stops delivering", async () => {
    const { transport, connection } = await connectOpen();
    const a = spyHandlers();
    const handle = connection.subscribe(query("A"), a);

    handle.dispose();
    transport.last().emit({ id: "0", type: "update", message: {} });

    expect(transport.last().frames()).toContainEqual({
      id: "0",
      type: "unsubscribe",
    });
    expect(a.onUpdate).not.toHaveBeenCalled();
  });

  it("disposes only once", async () => {
    const { transport, connection } = await connectOpen();
    const handle = connection.subscribe(query("A"), spyHandlers());

    handle.dispose();
    handle.dispose();

    expect(
      transport.last().framesOfType("unsubscribe" as const),
    ).toHaveLength(1);
  });

  it("calls onReset before the subscribe frame goes out", async () => {
    const { transport, connection } = await connectOpen();
    const order: string[] = [];
    const socket = transport.last();
    const originalSend = socket.send.bind(socket);
    socket.send = (data: string) => {
      order.push("send");
      originalSend(data);
    };

    connection.subscribe(query("A"), {
      onReset: () => order.push("reset"),
      onUpdate: () => {},
      onError: () => {},
    });

    // The accumulated payload has to be gone before the snapshot answering this
    // subscribe can arrive, so the reset leads and is deliberately synchronous.
    expect(order).toEqual(["reset", "send"]);
  });

  it("sends the extensions option with every operation", async () => {
    const { transport, connection } = await connectOpen({
      extensions: { token: "abc" },
    });

    connection.subscribe(query("A"), spyHandlers());

    expect(transport.last().frames()[0]).toMatchObject({
      extensions: { token: "abc" },
    });
  });

  it("calls a function-valued extensions option per operation", async () => {
    let n = 0;
    const { transport, connection } = await connectOpen({
      extensions: () => ({ n: n++ }),
    });

    connection.subscribe(query("A"), spyHandlers());
    connection.subscribe(query("B"), spyHandlers());

    expect(extensionsOf(transport.last().frames())).toEqual([
      { n: 0 },
      { n: 1 },
    ]);
  });
});

describe("reconnecting", () => {
  it("replays every subscription, resetting each one first", async () => {
    const { transport, connection } = await connectOpen();
    const a = spyHandlers();
    const b = spyHandlers();
    connection.subscribe(query("A"), a);
    connection.subscribe(query("B"), b);
    transport.last().emit({ id: "0", type: "update", message: { n: 1 } });

    transport.last().drop();
    await vi.advanceTimersByTimeAsync(FIRST_BACKOFF_MS);
    transport.last().openSocket();

    // Same ids, on a server that has never heard of them: it keeps no session,
    // so its previous value for each is empty again and the next update on each
    // is a fresh snapshot. That is what the second reset is warning about.
    expect(subscribeFrames(transport.last().frames())).toEqual([
      { id: "0", operationName: "A" },
      { id: "1", operationName: "B" },
    ]);
    expect(a.onReset).toHaveBeenCalledTimes(2);
    expect(b.onReset).toHaveBeenCalledTimes(2);
    expect(a.onError).not.toHaveBeenCalled();

    transport.last().emit({ id: "0", type: "update", message: [{ n: 9 }] });
    expect(a.onUpdate).toHaveBeenLastCalledWith([{ n: 9 }]);
  });

  it("does not replay a subscription that was disposed while down", async () => {
    const { transport, connection } = await connectOpen();
    const handle = connection.subscribe(query("A"), spyHandlers());

    transport.last().drop();
    handle.dispose();
    await vi.advanceTimersByTimeAsync(FIRST_BACKOFF_MS);
    transport.last().openSocket();

    expect(subscribeFrames(transport.last().frames())).toEqual([]);
  });

  it("treats a socket error like a close", async () => {
    const { transport } = await connectOpen();

    transport.last().fail();
    await vi.advanceTimersByTimeAsync(FIRST_BACKOFF_MS);

    expect(transport.sockets).toHaveLength(2);
  });

  it("does not resubscribe a subscription whose handler threw", async () => {
    // One caller's bad handler must not take the rest of the replay with it.
    const warn = vi.fn();
    const { transport, connection } = connect({ logger: { warn } });
    await settle();
    connection.subscribe(query("A"), {
      onReset: () => {
        throw new Error("handler bug");
      },
      onUpdate: () => {},
      onError: () => {},
    });
    connection.subscribe(query("B"), spyHandlers());

    transport.last().openSocket();

    expect(subscribeFrames(transport.last().frames())).toEqual([
      { id: "1", operationName: "B" },
    ]);
    expect(warn).toHaveBeenCalled();
  });

  it("keeps replaying when the extensions callback throws", async () => {
    // An expired token should not leave every subscription on the socket
    // registered, silent and never sent again.
    const warn = vi.fn();
    const { transport, connection } = connect({
      logger: { warn },
      extensions: () => {
        throw new Error("token expired");
      },
    });
    await settle();
    connection.subscribe(query("A"), spyHandlers());
    connection.subscribe(query("B"), spyHandlers());

    transport.last().openSocket();

    expect(subscribeFrames(transport.last().frames())).toEqual([
      { id: "0", operationName: "A" },
      { id: "1", operationName: "B" },
    ]);
    // Sent without them. A server that needs those values rejects the
    // operation, which is a failure the caller can see and act on.
    expect(transport.last().frames()[0]).not.toHaveProperty("extensions");
    expect(warn).toHaveBeenCalled();
  });
});

describe("backoff", () => {
  it("backs off a server that accepts the socket and hangs up", async () => {
    const { transport } = await connectOpen();

    transport.last().drop();
    await vi.advanceTimersByTimeAsync(FIRST_BACKOFF_MS - 1);
    expect(transport.sockets).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(transport.sockets).toHaveLength(2);

    // And the window doubles rather than staying put.
    transport.last().openSocket();
    transport.last().drop();
    await vi.advanceTimersByTimeAsync(999);
    expect(transport.sockets).toHaveLength(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(transport.sockets).toHaveLength(3);
  });

  it("does not take a delivered frame as proof the connection was healthy", async () => {
    // The storm this replaced: any frame at all set "we had success", so a
    // server that greeted a client and hung up bought an instant retry every
    // time -- as many connection attempts a second as the event loop allowed.
    const { transport } = await connectOpen();

    transport.last().emit({ id: "0", type: "update", message: {} });
    transport.last().drop();

    await vi.advanceTimersByTimeAsync(0);
    expect(transport.sockets).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(FIRST_BACKOFF_MS);
    expect(transport.sockets).toHaveLength(2);
  });

  it("retries at once after a connection that stood up", async () => {
    const { transport } = await connectOpen();

    await vi.advanceTimersByTimeAsync(HEALTHY_MS);
    transport.last().drop();

    await vi.advanceTimersByTimeAsync(0);
    expect(transport.sockets).toHaveLength(2);
  });

  it("allows only one immediate retry before backing off again", async () => {
    const { transport } = await connectOpen();
    await vi.advanceTimersByTimeAsync(HEALTHY_MS);

    transport.last().drop();
    await vi.advanceTimersByTimeAsync(0);
    expect(transport.sockets).toHaveLength(2);

    // The replacement dies at once, so it was not a blip after all.
    transport.last().openSocket();
    transport.last().drop();
    await vi.advanceTimersByTimeAsync(0);
    expect(transport.sockets).toHaveLength(2);

    await vi.advanceTimersByTimeAsync(FIRST_BACKOFF_MS);
    expect(transport.sockets).toHaveLength(3);
  });

  it("caps the delay at maxReconnectDelayMs", async () => {
    const { transport } = connect({
      initialReconnectDelayMs: 1_000,
      maxReconnectDelayMs: 4_000,
    });
    await settle();

    // Windows: 1000, 2000, 4000, then capped. Half of each, jitter pinned to 0.
    for (const delay of [500, 1_000, 2_000, 2_000, 2_000]) {
      const before = transport.sockets.length;
      transport.last().openSocket();
      transport.last().drop();
      await vi.advanceTimersByTimeAsync(delay - 1);
      expect(transport.sockets).toHaveLength(before);
      await vi.advanceTimersByTimeAsync(1);
      expect(transport.sockets).toHaveLength(before + 1);
    }
  });
});

describe("mutating", () => {
  it("resolves with the result message", async () => {
    const { transport, connection } = await connectOpen();

    const pending = connection.mutate(query("M"));
    expect(transport.last().frames()[0]).toMatchObject({
      id: "0",
      type: "mutate",
      message: { operationName: "M" },
    });

    transport.last().emit({ id: "0", type: "result", message: [{ ok: true }] });

    await expect(pending).resolves.toEqual([{ ok: true }]);
  });

  it("rejects on an error addressed to it", async () => {
    const { transport, connection } = await connectOpen();

    const pending = connection.mutate(query("M"));
    transport.last().emit({ id: "0", type: "error", message: "denied" });

    await expect(pending).rejects.toThrow("denied");
  });

  it("rejects immediately when the socket is not open", async () => {
    const { connection } = connect();

    // Never queued: the server offers no idempotency, so a mutation held across
    // a reconnect risks being applied twice.
    await expect(connection.mutate(query("M"))).rejects.toThrow(
      ERROR_NOT_CONNECTED,
    );
  });

  it("rejects one that was in flight when the socket dropped", async () => {
    const { transport, connection } = await connectOpen();

    const pending = connection.mutate(query("M"));
    transport.last().drop();

    await expect(pending).rejects.toThrow(/connection closed \(socket closed\)/);
  });

  it("rejects when the reply does not come", async () => {
    const { connection } = await connectOpen({ mutationTimeoutMs: 1_000 });

    // The assertion is attached before time moves, so the rejection is never
    // momentarily unhandled.
    const settled = expect(connection.mutate(query("M"))).rejects.toThrow(
      ERROR_MUTATION_TIMEOUT,
    );
    await vi.advanceTimersByTimeAsync(1_000);

    await settled;
  });

  it("ignores a reply that arrives after the timeout", async () => {
    const { transport, connection } = await connectOpen({
      mutationTimeoutMs: 1_000,
    });

    const settled = expect(connection.mutate(query("M"))).rejects.toThrow(
      ERROR_MUTATION_TIMEOUT,
    );
    await vi.advanceTimersByTimeAsync(1_000);
    await settled;

    expect(() => {
      transport.last().emit({ id: "0", type: "result", message: [{}] });
    }).not.toThrow();
  });
});

describe("server errors", () => {
  it("ends the subscription it names", async () => {
    const { transport, connection } = await connectOpen();
    const a = spyHandlers();
    connection.subscribe(query("A"), a);

    transport.last().emit({ id: "0", type: "error", message: "boom" });

    expect(a.onError).toHaveBeenCalledTimes(1);
    expect((a.onError as ReturnType<typeof vi.fn>).mock.calls[0]?.[0]).toMatchObject({
      name: "LightningServerError",
      message: "boom",
    });
  });

  it("stops delivering to it and does not replay it on reconnect", async () => {
    const { transport, connection } = await connectOpen();
    const a = spyHandlers();
    connection.subscribe(query("A"), a);

    // The server closed the subscription on its side before sending this, so
    // there is nothing left to unsubscribe from and nothing to replay.
    transport.last().emit({ id: "0", type: "error", message: "boom" });
    transport.last().emit({ id: "0", type: "update", message: {} });
    expect(a.onUpdate).not.toHaveBeenCalled();

    transport.last().drop();
    await vi.advanceTimersByTimeAsync(FIRST_BACKOFF_MS);
    transport.last().openSocket();

    expect(subscribeFrames(transport.last().frames())).toEqual([]);
  });

  it("warns about a frame it cannot read, and carries on", async () => {
    const warn = vi.fn();
    const { transport, connection } = await connectOpen({ logger: { warn } });
    const a = spyHandlers();
    connection.subscribe(query("A"), a);

    transport.last().deliver("<html>proxy error</html>");
    expect(warn).toHaveBeenCalledWith(
      "lightning: unrecognized frame",
      "<html>proxy error</html>",
    );

    transport.last().emit({ id: "0", type: "update", message: { n: 1 } });
    expect(a.onUpdate).toHaveBeenCalledWith({ n: 1 });
  });
});

describe("the heartbeat", () => {
  it("sends an echo when the socket has been quiet", async () => {
    const { transport } = await connectOpen({ pingIntervalMs: 1_000 });

    await vi.advanceTimersByTimeAsync(1_000);

    expect(transport.last().frames()).toContainEqual({ type: "echo" });
  });

  it("schedules the next one from the reply, not from the send", async () => {
    const { transport } = await connectOpen({ pingIntervalMs: 1_000 });

    await vi.advanceTimersByTimeAsync(1_000);
    transport.last().emit({ type: "echo" });
    await vi.advanceTimersByTimeAsync(1_000);

    expect(
      transport.last().frames().filter((frame) => frame.type === "echo"),
    ).toHaveLength(2);
  });

  it("kills a socket that stops answering", async () => {
    // The server sets no read deadline and sends no websocket pings, so this is
    // the only thing that notices a connection that has quietly stopped
    // carrying traffic without being closed.
    const { transport, connection } = await connectOpen({
      pingIntervalMs: 1_000,
      pingTimeoutMs: 500,
    });

    await vi.advanceTimersByTimeAsync(1_000 + 500);

    expect(transport.last().closeCount).toBe(1);
    expect(connection.status).toBe("reconnecting");
  });
});

describe("close", () => {
  it("ends every subscription rather than dropping it quietly", async () => {
    // A caller holding a handle to something that will never produce another
    // value deserves to be told, not left waiting.
    const { connection } = await connectOpen();
    const a = spyHandlers();
    const b = spyHandlers();
    connection.subscribe(query("A"), a);
    connection.subscribe(query("B"), b);

    connection.close();

    for (const handlers of [a, b]) {
      expect(handlers.onError).toHaveBeenCalledTimes(1);
      const error = (handlers.onError as ReturnType<typeof vi.fn>).mock
        .calls[0]?.[0] as Error;
      expect(error.message).toContain(ERROR_CLOSED);
    }
  });

  it("rejects an in-flight mutation", async () => {
    const { connection } = await connectOpen();

    const pending = connection.mutate(query("M"));
    connection.close();

    await expect(pending).rejects.toThrow(/connection closed \(close called\)/);
  });

  it("closes the socket and stops reconnecting", async () => {
    const { transport, connection } = await connectOpen();

    connection.close();

    expect(transport.last().closeCount).toBe(1);
    expect(connection.status).toBe("closed");

    await vi.advanceTimersByTimeAsync(60_000);
    expect(transport.sockets).toHaveLength(1);
  });

  it("refuses to subscribe afterwards", async () => {
    const { connection } = await connectOpen();

    connection.close();

    expect(() => connection.subscribe(query("A"), spyHandlers())).toThrow(
      ERROR_CLOSED,
    );
  });

  it("survives a handler that throws on the way out", async () => {
    const warn = vi.fn();
    const { connection } = await connectOpen({ logger: { warn } });
    connection.subscribe(query("A"), {
      onReset: () => {},
      onUpdate: () => {},
      onError: () => {
        throw new Error("handler bug");
      },
    });
    const b = spyHandlers();
    connection.subscribe(query("B"), b);

    connection.close();

    expect(b.onError).toHaveBeenCalledTimes(1);
    expect(warn).toHaveBeenCalled();
  });

  it("lets a handler subscribe again on the connection it reopens", async () => {
    const { transport, connection } = await connectOpen();
    const revived = spyHandlers();
    connection.subscribe(query("A"), {
      onReset: () => {},
      onUpdate: () => {},
      onError: () => {
        connection.connect();
        connection.subscribe(query("A2"), revived);
      },
    });

    connection.close();
    await settle();
    transport.last().openSocket();

    expect(transport.sockets).toHaveLength(2);
    expect(subscribeFrames(transport.last().frames())).toEqual([
      { id: "1", operationName: "A2" },
    ]);
  });

  it("does not resurrect the subscriptions it ended", async () => {
    const { transport, connection } = await connectOpen();
    const a = spyHandlers();
    connection.subscribe(query("A"), a);

    connection.close();
    connection.connect();
    await settle();
    transport.last().openSocket();

    expect(subscribeFrames(transport.last().frames())).toEqual([]);
    expect(a.onReset).toHaveBeenCalledTimes(1);
  });
});
