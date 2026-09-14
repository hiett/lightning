import { Suspense, useState } from "react";
import { graphql, usePaginationFragment, useLazyLoadQuery, useMutation } from "react-relay";
import { TaskRow } from "./TaskRow";
import type { TaskListQuery } from "./__generated__/TaskListQuery.graphql";
import type { TaskList_query$key } from "./__generated__/TaskList_query.graphql";
import type { TaskListAddMutation } from "./__generated__/TaskListAddMutation.graphql";

const rootQuery = graphql`
  query TaskListQuery($count: Int!, $cursor: String) {
    viewer {
      id
      displayName
    }
    ...TaskList_query @arguments(count: $count, cursor: $cursor)
  }
`;

// @connection is what makes this a paginated list Relay can append to. It
// requires the field to be a Relay connection — edges { node cursor } and
// pageInfo { hasNextPage endCursor } — with first and after arguments, which
// is exactly what schemabuilder.Paginated generates.
const listFragment = graphql`
  fragment TaskList_query on Query
  @argumentDefinitions(count: { type: "Int", defaultValue: 3 }, cursor: { type: "String" })
  @refetchable(queryName: "TaskListPaginationQuery") {
    tasks(first: $count, after: $cursor) @connection(key: "TaskList_tasks") {
      # __id is Relay's own identifier for the connection record. It is a
      # client-side field — relay-compiler strips it from the query it sends —
      # and it is what @appendNode needs to know which connection to splice
      # into.
      __id
      totalCount
      edges {
        cursor
        node {
          id
          ...TaskRow_task
        }
      }
    }
  }
`;

const addMutation = graphql`
  mutation TaskListAddMutation($title: String!, $ownerId: ID!, $connections: [ID!]!) {
    # @appendNode splices the new task into the connection without refetching
    # it. It needs the node's id, and it needs TaskEdge to be a real edge type
    # in the schema — both of which the generated connection provides.
    addTask(title: $title, ownerId: $ownerId)
      @appendNode(connections: $connections, edgeTypeName: "TaskEdge") {
      id
      ...TaskRow_task
    }
  }
`;

function Tasks({ query, viewerId }: { query: TaskList_query$key; viewerId: string }) {
  const { data, loadNext, hasNext, isLoadingNext } = usePaginationFragment(listFragment, query);
  const [addTask, adding] = useMutation<TaskListAddMutation>(addMutation);
  const [title, setTitle] = useState("");

  const connectionId = data.tasks?.__id;

  return (
    <section>
      <h2>Tasks ({data.tasks?.totalCount ?? "0"})</h2>

      <ul>
        {data.tasks?.edges?.map((edge) =>
          edge?.node ? <TaskRow key={edge.node.id} task={edge.node} /> : null,
        )}
      </ul>

      {hasNext ? (
        <button disabled={isLoadingNext} onClick={() => loadNext(3)}>
          {isLoadingNext ? "Loading…" : "Load more"}
        </button>
      ) : (
        <p>
          <em>That is all of them.</em>
        </p>
      )}

      <form
        onSubmit={(event) => {
          event.preventDefault();
          if (!title.trim() || connectionId == null) return;
          addTask({
            variables: { title, ownerId: viewerId, connections: [connectionId] },
            onCompleted: () => setTitle(""),
            onError: (error) => {
              // Surfacing the failure beats an input that silently does
              // nothing; a real application would show it properly.
              console.error("addTask failed", error);
            },
          });
        }}
      >
        <input
          value={title}
          placeholder="Something to do"
          onChange={(event) => setTitle(event.target.value)}
        />
        <button type="submit" disabled={adding || !title.trim()}>
          Add
        </button>
      </form>
    </section>
  );
}

export function TaskList() {
  const data = useLazyLoadQuery<TaskListQuery>(rootQuery, { count: 3, cursor: null });

  return (
    <main>
      <h1>lightning example</h1>
      <p>Signed in as {data.viewer?.displayName ?? "nobody"}.</p>
      {data.viewer ? (
        <Suspense fallback={<p>Loading tasks…</p>}>
          <Tasks query={data} viewerId={data.viewer.id} />
        </Suspense>
      ) : null}
    </main>
  );
}
