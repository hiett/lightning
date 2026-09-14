// jsdom has no WebSocket, and the lightning network layer needs one.
import WebSocket from "ws";

if (!("WebSocket" in globalThis)) {
  (globalThis as unknown as { WebSocket: unknown }).WebSocket = WebSocket;
}

// React needs to be told this is an act() environment, or every update logs a
// warning and some batching behaves differently.
(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
