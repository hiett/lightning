import { Suspense } from "react";
import { RelayEnvironmentProvider } from "react-relay";
import { environment } from "./environment";
import { TaskList } from "./TaskList";
import { LiveTasks } from "./LiveTasks";

export function App() {
  return (
    <RelayEnvironmentProvider environment={environment}>
      <Suspense fallback={<p>Connecting…</p>}>
        <TaskList />
        <LiveTasks />
      </Suspense>
    </RelayEnvironmentProvider>
  );
}
