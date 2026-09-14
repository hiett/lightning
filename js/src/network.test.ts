import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type {
  GraphQLResponse,
  Observable,
  RequestParameters,
} from "relay-runtime";

import { LightningConnection } from "./connection.js";
import {
  ERROR_QUERY_TIMEOUT,
  ERROR_UPLOADABLES,
  createLightningNetwork,
} from "./network.js";
import type { ClientEnvelope } from "./protocol.js";
import { fakeTransport } from "./testing/fakeWebSocket.js";

/**
 * These drive the network layer through Relay's own `execute`, so what is
 * asserted is what Relay would actually be handed: a complete GraphQLResponse
 * per change, assembled from the diffs the server sent.
 */
beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

async function flush(): Promise<void> {
  await vi.advanceTimersByTimeAsync(0);
}

function request(
  name: string,
  operationKind: "query" | "mutation" | "subscription",
  text: string | null = `${operationKind} ${name} { x }`,
): RequestParameters {
  return {
    cacheID: name,
    id: null,
    text,
    name,
    operationKind,
    metadata: {},
  };
}

function build(networkOptions: { queryTimeoutMs?: number } = {}) {
  const transport = fakeTransport();
  const connection = new LightningConnection({
    connect: transport.connect,
    initialReconnectDelayMs: 1_000,
    maxReconnectDelayMs: 1_000,
    pingIntervalMs: 60_000,
  });
  const network = createLightningNetwork({ connection, ...networkOptions });
  return { transport, connection, network };
}

async function buildOpen(networkOptions: { queryTimeoutMs?: number } = {}) {
  const built = build(networkOptions);
  await flush();
  built.transport.last().openSocket();
  return built;
}

interface Collected {
  values: GraphQLResponse[];
  errors: Error[];
  completed: boolean;
  dispose(): void;
}

function collect(observable: Observable<GraphQLResponse>): Collected {
  const collected: Collected = {
    values: [],
    errors: [],
    completed: false,
    dispose: () => {},
  };
  const subscription = observable.subscribe({
    next: (value) => collected.values.push(value),
    error: (error: Error) => collected.errors.push(error),
    complete: () => {
      collected.completed = true;
    },
  });
  collected.dispose = () => subscription.unsubscribe();
  return collected;
}

/** The data of one emission, for the tests that poke at it. */
function dataOf(response: GraphQLResponse): Record<string, unknown> {
  return (response as { data: Record<string, unknown> }).data;
}

function frames(sent: string[]): ClientEnvelope[] {
  return sent.map((frame) => JSON.parse(frame) as ClientEnvelope);
}

describe("a query", () => {
  it("subscribes, takes the first payload, and unsubscribes", async () => {
    const { transport, network } = await buildOpen();

    const pending = network
      .execute(request("Q", "query"), {}, {})
      .toPromise();

    expect(frames(transport.last().sent)).toEqual([
      {
        id: "0",
        type: "subscribe",
        message: {
          query: "query Q { x }",
          operationName: "Q",
          variables: {},
        },
      },
    ]);

    // The first update of a subscription is always a complete snapshot, so one
    // frame is a whole answer; anything after it is a live-query update this
    // caller did not ask for.
    transport.last().emit({
      id: "0",
      type: "update",
      message: [{ users: [{ __key: "1", id: "1", name: "bob" }] }],
    });

    await expect(pending).resolves.toEqual({
      data: { users: [{ id: "1", name: "bob" }] },
    });
    expect(frames(transport.last().sent)).toContainEqual({
      id: "0",
      type: "unsubscribe",
    });
  });

  it("passes the variables through", async () => {
    const { transport, network } = await buildOpen();

    void network.execute(request("Q", "query"), { first: 10 }, {}).toPromise();

    expect(frames(transport.last().sent)[0]).toMatchObject({
      message: { variables: { first: 10 } },
    });
  });

  it("waits for a socket rather than failing when one is not up yet", async () => {
    // Unlike a mutation, a query is safe to replay, so it is held and sent on
    // whatever socket opens next.
    const { transport, network } = build();

    const pending = network.execute(request("Q", "query"), {}, {}).toPromise();
    await flush();
    transport.last().openSocket();

    expect(frames(transport.last().sent)).toHaveLength(1);
    transport.last().emit({ id: "0", type: "update", message: [{ n: 1 }] });
    await expect(pending).resolves.toEqual({ data: { n: 1 } });
  });

  it("rejects rather than hanging for ever when no socket appears", async () => {
    // The transport is down and staying down. A promise that never settles is
    // a spinner with no end and no error, which is the one outcome an
    // application cannot render.
    const { transport, network } = build({ queryTimeoutMs: 1_000 });
    await flush();

    const settled = expect(
      network.execute(request("Q", "query"), {}, {}).toPromise(),
    ).rejects.toThrow(ERROR_QUERY_TIMEOUT);
    await vi.advanceTimersByTimeAsync(1_000);
    await settled;

    // And it let go of the subscription on the way out, so the socket that
    // eventually opens does not replay a query nobody is waiting for any more.
    transport.last().openSocket();
    expect(frames(transport.last().sent)).toEqual([]);
  });

  it("rejects on a server error", async () => {
    const { transport, network } = await buildOpen();

    const pending = network.execute(request("Q", "query"), {}, {}).toPromise();
    transport.last().emit({ id: "0", type: "error", message: "denied" });

    await expect(pending).rejects.toThrow("denied");
  });

  it("rejects a persisted query, which this protocol cannot send", async () => {
    const { network } = await buildOpen();

    await expect(
      network.execute(request("Q", "query", null), {}, {}).toPromise(),
    ).rejects.toThrow(/persisted/i);
  });

  it("reports a payload that is not an object as an error", async () => {
    // {data: null} with no errors is the shape Relay throws on, and its throw
    // says nothing about where the trouble came from.
    const { transport, network } = await buildOpen();

    const pending = network.execute(request("Q", "query"), {}, {}).toPromise();
    transport.last().emit({ id: "0", type: "update", message: [] });

    await expect(pending).resolves.toEqual({
      errors: [{ message: expect.stringContaining("not an object") }],
    });
  });
});

describe("a mutation", () => {
  it("resolves with its payload, unwrapped", async () => {
    const { transport, network } = await buildOpen();

    const pending = network
      .execute(request("M", "mutation"), { id: "1" }, {})
      .toPromise();

    expect(frames(transport.last().sent)).toEqual([
      {
        id: "0",
        type: "mutate",
        message: {
          query: "mutation M { x }",
          operationName: "M",
          variables: { id: "1" },
        },
      },
    ]);

    // A result is a complete payload wrapped as a replacement -- the server
    // diffs it against nothing -- so handing Relay the wrapper would give it a
    // one-element list where the data should be.
    transport.last().emit({
      id: "0",
      type: "result",
      message: [{ setName: { __key: "1", id: "1", name: "alice" } }],
    });

    await expect(pending).resolves.toEqual({
      data: { setName: { id: "1", name: "alice" } },
    });
  });

  it("rejects when the socket is down instead of queueing", async () => {
    const { network } = build();

    await expect(
      network.execute(request("M", "mutation"), {}, {}).toPromise(),
    ).rejects.toThrow("not connected");
  });

  it("rejects on a server error", async () => {
    const { transport, network } = await buildOpen();

    const pending = network
      .execute(request("M", "mutation"), {}, {})
      .toPromise();
    transport.last().emit({ id: "0", type: "error", message: "denied" });

    await expect(pending).rejects.toThrow("denied");
  });

  it("refuses uploadables rather than dropping the files", async () => {
    // commitMutation({uploadables}) would otherwise send the mutation with the
    // files silently missing, and the server would apply it.
    const { transport, network } = await buildOpen();
    const file = new Blob(["x"]);

    await expect(
      network.execute(request("M", "mutation"), {}, {}, { file }).toPromise(),
    ).rejects.toThrow(ERROR_UPLOADABLES);
    expect(transport.last().sent).toEqual([]);
  });

  it("ignores an empty uploadables map", async () => {
    const { transport, network } = await buildOpen();

    void network.execute(request("M", "mutation"), {}, {}, {}).toPromise();

    expect(frames(transport.last().sent)).toHaveLength(1);
  });
});

describe("a live query", () => {
  it("emits a full payload for every diff", async () => {
    const { transport, network } = await buildOpen();

    const emitted = collect(
      network.execute(request("S", "subscription"), {}, {}),
    );
    expect(frames(transport.last().sent)[0]).toMatchObject({
      id: "0",
      type: "subscribe",
    });

    transport.last().emit({
      id: "0",
      type: "update",
      message: [{ users: [{ name: "bob" }, { name: "alice" }] }],
    });
    transport.last().emit({
      id: "0",
      type: "update",
      message: { users: { $: [[0, 2], -1], "2": [{ name: "carol" }] } },
    });

    // Relay is never shown a diff: the second emission is the whole payload,
    // with the appended element merged into what was accumulated.
    expect(emitted.values).toEqual([
      { data: { users: [{ name: "bob" }, { name: "alice" }] } },
      {
        data: {
          users: [{ name: "bob" }, { name: "alice" }, { name: "carol" }],
        },
      },
    ]);
    expect(emitted.errors).toEqual([]);
  });

  it("keeps __key out of what Relay is handed and in the merge state", async () => {
    const { transport, network } = await buildOpen();
    const emitted = collect(
      network.execute(request("S", "subscription"), {}, {}),
    );

    transport.last().emit({
      id: "0",
      type: "update",
      message: [{ user: { __key: "u1", name: "bob", tags: ["a"] } }],
    });
    expect(emitted.values[0]).toEqual({
      data: { user: { name: "bob", tags: ["a"] } },
    });

    // Relay owns what it was handed and may normalize it destructively, so the
    // accumulated payload cannot be the same object.
    const handed = dataOf(emitted.values[0] as GraphQLResponse);
    delete handed["user"];

    transport.last().emit({
      id: "0",
      type: "update",
      message: { user: { name: "alice" } },
    });

    // The diff mentions only `name`, so `tags` can only have come from a merge
    // state that survived both the strip and the caller's meddling.
    expect(emitted.values[1]).toEqual({
      data: { user: { name: "alice", tags: ["a"] } },
    });
  });

  it("starts the payload over after a reconnect", async () => {
    const { transport, network } = await buildOpen();
    const emitted = collect(
      network.execute(request("S", "subscription"), {}, {}),
    );
    transport.last().emit({
      id: "0",
      type: "update",
      message: [{ a: 1, b: 2 }],
    });

    transport.last().drop();
    await vi.advanceTimersByTimeAsync(1_000);
    transport.last().openSocket();

    // The server kept no session, so this is a fresh snapshot against an empty
    // previous value. Merging it onto the old payload would leave `b` behind,
    // describing a state nobody has.
    transport.last().emit({ id: "0", type: "update", message: [{ a: 9 }] });

    expect(emitted.values).toEqual([
      { data: { a: 1, b: 2 } },
      { data: { a: 9 } },
    ]);
  });

  it("ends on a server error instead of retrying quietly", async () => {
    const { transport, network } = await buildOpen();
    const emitted = collect(
      network.execute(request("S", "subscription"), {}, {}),
    );

    transport.last().emit({ id: "0", type: "update", message: [{ a: 1 }] });
    transport.last().emit({ id: "0", type: "error", message: "resolver blew up" });

    expect(emitted.errors.map((error) => error.message)).toEqual([
      "resolver blew up",
    ]);
    expect(emitted.values).toHaveLength(1);
  });

  it("unsubscribes when Relay disposes it", async () => {
    const { transport, network } = await buildOpen();
    const emitted = collect(
      network.execute(request("S", "subscription"), {}, {}),
    );

    emitted.dispose();
    transport.last().emit({ id: "0", type: "update", message: [{ a: 1 }] });

    expect(frames(transport.last().sent)).toContainEqual({
      id: "0",
      type: "unsubscribe",
    });
    expect(emitted.values).toEqual([]);
  });

  it("reports a closed connection instead of hanging", async () => {
    const { connection, network } = await buildOpen();
    connection.close();

    const emitted = collect(
      network.execute(request("S", "subscription"), {}, {}),
    );

    expect(emitted.errors.map((error) => error.message)).toEqual([
      "lightning: connection closed",
    ]);
  });
});
