import { useEffect, useState } from "react";
import { graphql, useRelayEnvironment } from "react-relay";
import { requestSubscription } from "relay-runtime";
import type { LiveTasksSubscription } from "./__generated__/LiveTasksSubscription.graphql";

// A subscription in lightning is a live query: the server re-executes it
// whenever a resource it read is invalidated, and pushes the whole result. The
// operation itself is an ordinary GraphQL subscription, so relay-compiler and
// the Relay store treat it like any other.
const liveSubscription = graphql`
  subscription LiveTasksSubscription {
    tasks(first: 100) {
      totalCount
      edges {
        node {
          id
          title
          done
        }
      }
    }
  }
`;

export function LiveTasks() {
  const environment = useRelayEnvironment();
  const [pushes, setPushes] = useState(0);
  const [outstanding, setOutstanding] = useState<string[]>([]);

  useEffect(() => {
    const subscription = requestSubscription<LiveTasksSubscription>(environment, {
      subscription: liveSubscription,
      variables: {},
      onNext: (response) => {
        setPushes((count) => count + 1);
        const edges = response?.tasks?.edges ?? [];
        setOutstanding(
          edges
            .filter((edge) => edge?.node && !edge.node.done)
            .map((edge) => edge!.node!.title),
        );
      },
    });

    return () => subscription.dispose();
  }, [environment]);

  return (
    <aside>
      <h2>Live</h2>
      <p>
        {pushes} push{pushes === 1 ? "" : "es"} from the server.
      </p>
      <ul>
        {outstanding.map((title) => (
          <li key={title}>{title}</li>
        ))}
      </ul>
    </aside>
  );
}
