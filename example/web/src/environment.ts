import { Environment, RecordSource, Store } from "relay-runtime";
import { createLightningNetwork } from "@hiett/lightning-relay";

// The example server serves lightning's diff-pushing live-query protocol at
// /graphql/live. A query, a mutation and a subscription all travel over that
// one socket.
const url = import.meta.env.VITE_LIGHTNING_URL ?? "ws://localhost:8080/graphql/live";

export const environment = new Environment({
  network: createLightningNetwork({ url }),
  store: new Store(new RecordSource()),
});
