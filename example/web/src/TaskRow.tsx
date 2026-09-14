import { useFragment, useMutation, graphql } from "react-relay";
import type { TaskRow_task$key } from "./__generated__/TaskRow_task.graphql";
import type { TaskRowSetDoneMutation } from "./__generated__/TaskRowSetDoneMutation.graphql";

// The fragment is @refetchable, which is what requires Task to implement Node
// and the schema to expose node(id: ID!): relay-compiler generates a refetch
// query that looks the task up by its global id.
const taskFragment = graphql`
  fragment TaskRow_task on Task @refetchable(queryName: "TaskRowRefetchQuery") {
    id
    title
    done
    owner {
      __typename
      displayName
      ... on User {
        email
      }
      ... on Team {
        members
      }
    }
  }
`;

const setDoneMutation = graphql`
  mutation TaskRowSetDoneMutation($id: ID!, $done: Boolean!) {
    setTaskDone(id: $id, done: $done) {
      id
      done
    }
  }
`;

export function TaskRow({ task }: { task: TaskRow_task$key }) {
  const data = useFragment(taskFragment, task);
  const [setDone, inFlight] = useMutation<TaskRowSetDoneMutation>(setDoneMutation);

  const owner = data.owner;

  return (
    <li>
      <label>
        <input
          type="checkbox"
          checked={data.done}
          disabled={inFlight}
          onChange={(event) =>
            setDone({
              variables: { id: data.id, done: event.target.checked },
              // The mutation selects id and done, so Relay updates the store
              // from the response without any manual updater.
            })
          }
        />
        <span>{data.title}</span>
      </label>
      {owner ? (
        <small>
          {" — "}
          {owner.displayName}
          {owner.__typename === "User" && owner.email ? ` <${owner.email}>` : null}
          {owner.__typename === "Team" && owner.members != null ? ` (${owner.members} people)` : null}
        </small>
      ) : null}
    </li>
  );
}
