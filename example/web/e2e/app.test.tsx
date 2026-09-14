/**
 * The end-to-end acceptance test for the whole refactor.
 *
 * It mounts the real Relay components — the ones relay-compiler compiled
 * against the exported schema.graphql — in jsdom, points them at a running
 * lightning server over the live-query websocket, and drives them the way a
 * person would: read the list, load another page, refetch a node by its global
 * id, add a task, and watch a live query notice.
 *
 * Nothing here is mocked. The server is the example server, the network layer
 * is @hiett/lightning-relay talking the diff protocol, and the queries are the
 * generated artifacts.
 *
 * Run it with the server up:
 *
 *   cd example && go run ./cmd/server      # terminal one
 *   cd example/web && npm run e2e          # terminal two
 */

import { StrictMode, Suspense } from "react";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { RelayEnvironmentProvider, fetchQuery } from "react-relay";
import { Environment, RecordSource, Store, graphql } from "relay-runtime";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";

import { createLightningNetwork } from "@hiett/lightning-relay";
import { TaskList } from "../src/TaskList";
import { LiveTasks } from "../src/LiveTasks";

const HTTP_URL = process.env.LIGHTNING_HTTP ?? "http://localhost:8080/graphql";
const WS_URL = process.env.LIGHTNING_WS ?? "ws://localhost:8080/graphql/live";

/** Runs a query over plain HTTP, for setting up and checking state. */
async function http(query: string, variables: Record<string, unknown> = {}) {
  const response = await fetch(HTTP_URL, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ query, variables }),
  });
  const body = (await response.json()) as {
    data?: Record<string, unknown>;
    errors?: { message: string }[];
  };
  if (body.errors?.length) {
    throw new Error(body.errors.map((e) => e.message).join("; "));
  }
  return body.data ?? {};
}

/** Looks up a task's global id by title. */
async function globalIDOf(title: string): Promise<string> {
  const data = (await http(`{ tasks(first: 100) { edges { node { id title } } } }`)) as {
    tasks: { edges: { node: { id: string; title: string } }[] };
  };
  const found = data.tasks.edges.find((edge) => edge.node.title === title);
  if (!found) {
    throw new Error(`no task titled ${title}`);
  }
  return found.node.id;
}

function makeEnvironment() {
  return new Environment({
    network: createLightningNetwork({ url: WS_URL }),
    store: new Store(new RecordSource()),
  });
}

/** Waits for a condition, re-checking on the macrotask queue. */
async function until(what: string, check: () => boolean, timeoutMs = 15_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (check()) return;
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 50));
    });
  }
  throw new Error(`timed out waiting for ${what}`);
}

describe("the example Relay app", () => {
  let container: HTMLDivElement;
  let root: Root;
  let environment: Environment;

  beforeAll(async () => {
    // Fail loudly and early if the server is not up, rather than timing out
    // inside a render.
    await http(`{ __typename }`).catch((cause) => {
      throw new Error(
        `the example server is not answering at ${HTTP_URL}. Start it with ` +
          `"cd example && go run ./cmd/server".`,
        { cause },
      );
    });
  });

  afterEach(() => {
    if (root) {
      act(() => root.unmount());
    }
    container?.remove();
  });

  afterAll(() => {
    // jsdom keeps the process alive while a socket is open.
    environment?.getNetwork();
  });

  async function mount(node: React.ReactNode) {
    container = document.createElement("div");
    document.body.appendChild(container);
    environment = makeEnvironment();
    root = createRoot(container);

    await act(async () => {
      root.render(
        <StrictMode>
          <RelayEnvironmentProvider environment={environment}>
            <Suspense fallback={<p>loading</p>}>{node}</Suspense>
          </RelayEnvironmentProvider>
        </StrictMode>,
      );
    });
  }

  const text = () => container.textContent ?? "";

  it("renders a paginated list, loads more, and appends after a mutation", async () => {
    await mount(<TaskList />);

    // The root query resolved and the viewer rendered.
    await until("the list to render", () => text().includes("Signed in as Ada"));

    // usePaginationFragment asked for the first page only.
    await until("the first page", () => text().includes("Write the schema"));
    expect(text()).not.toContain("Make it live");

    const loadMore = container.querySelector("button");
    expect(loadMore?.textContent).toBe("Load more");

    // Paginating forward fetches the next page and appends it.
    await act(async () => {
      loadMore!.click();
    });
    await until("the second page", () => text().includes("Make it live"));

    // The interface resolved to concrete types: a User owner shows an email, a
    // Team owner shows a member count.
    expect(text()).toContain("ada@example.com");
    expect(text()).toContain("people)");

    // Adding a task splices it into the connection through @appendNode, with no
    // refetch of the list.
    const title = `e2e ${Date.now()}`;
    const input = container.querySelector("input:not([type=checkbox])") as HTMLInputElement;
    const form = container.querySelector("form") as HTMLFormElement;

    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(
        window.HTMLInputElement.prototype,
        "value",
      )!.set!;
      setter.call(input, title);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });

    // The Add button is enabled only once React has the typed title, which is
    // also the check that the input event reached the component.
    const add = Array.from(container.querySelectorAll("button")).find(
      (button) => button.textContent === "Add",
    ) as HTMLButtonElement;
    await until("the Add button to become enabled", () => !add.disabled);

    await act(async () => {
      form.requestSubmit(add);
    });

    await until("the new task to appear in the list", () => text().includes(title));
  });

  it("toggles a task through a mutation and shows the new state", async () => {
    // The third seeded task is not done, and is on the first page, so this does
    // not depend on how many tasks earlier runs left behind.
    const title = "Wire up relay-compiler";
    await http(
      `mutation Reset($id: ID!) { setTaskDone(id: $id, done: false) { id } }`,
      { id: await globalIDOf(title) },
    );

    await mount(<TaskList />);
    await until("the list to render", () => text().includes("Signed in as Ada"));
    await until("the task to be on screen", () => text().includes(title));

    // Find its checkbox by walking up from the label that holds the title.
    const row = Array.from(container.querySelectorAll("li")).find((li) =>
      li.textContent?.includes(title),
    );
    expect(row).toBeTruthy();

    const checkbox = row!.querySelector("input[type=checkbox]") as HTMLInputElement;
    expect(checkbox.checked).toBe(false);

    await act(async () => {
      checkbox.click();
    });

    await until("the checkbox to reflect the mutation", () => {
      const current = Array.from(container.querySelectorAll("li")).find((li) =>
        li.textContent?.includes(title),
      );
      return (current?.querySelector("input[type=checkbox]") as HTMLInputElement)?.checked === true;
    });

    // And the server agrees, not just the store.
    const server = (await http(`{ tasks(first: 100) { edges { node { title done } } } }`)) as {
      tasks: { edges: { node: { title: string; done: boolean } }[] };
    };
    const saved = server.tasks.edges.find((e) => e.node.title === title);
    expect(saved?.node.done).toBe(true);
  });

  it("refetches a node by its global id", async () => {
    await mount(<TaskList />);
    await until("the list to render", () => text().includes("Write the schema"));

    // Take a global id straight from the server and refetch it through the
    // generated @refetchable query, which is what requires Node and node(id:).
    const first = (await http(`{ tasks(first: 1) { edges { node { id title } } } }`)) as {
      tasks: { edges: { node: { id: string; title: string } }[] };
    };
    const { id, title } = first.tasks.edges[0].node;

    const refetchQuery = graphql`
      query appTaskRefetchQuery($id: ID!) {
        node(id: $id) {
          __typename
          id
          ... on Task {
            title
            done
          }
        }
      }
    `;

    const result = await new Promise<Record<string, unknown>>((resolve, reject) => {
      fetchQuery(environment, refetchQuery, { id }).subscribe({
        next: (data) => resolve(data as Record<string, unknown>),
        error: reject,
      });
    });

    const node = result.node as { __typename: string; id: string; title: string };
    expect(node.__typename).toBe("Task");
    expect(node.id).toBe(id);
    expect(node.title).toBe(title);
  });

  it("updates live when the data changes behind it", async () => {
    await mount(<LiveTasks />);

    const pushesNow = () => Number(/(\d+) push/.exec(text())?.[1] ?? 0);

    // The subscription's first payload is a complete snapshot.
    await until("the first live payload", () => pushesNow() > 0);

    const before = pushesNow();
    expect(before).toBeGreaterThan(0);

    // Change the data over plain HTTP — a different connection entirely, so
    // nothing but the server's own invalidation can carry the news.
    const title = `e2e live ${Date.now()}`;
    const viewer = (await http(`{ viewer { id } }`)).viewer as { id: string };
    await http(
      `mutation Add($title: String!, $owner: ID!) { addTask(title: $title, ownerId: $owner) { id } }`,
      { title, owner: viewer.id },
    );

    await until("the live query to be pushed the new task", () => text().includes(title));
    expect(pushesNow()).toBeGreaterThan(before);
  });
});
