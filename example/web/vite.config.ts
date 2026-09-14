import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";

export default defineConfig({
  plugins: [
    react({
      // babel-plugin-relay turns the graphql`` tags into references to the
      // artifacts relay-compiler generated.
      babel: { plugins: ["relay"] },
    }),
  ],
  resolve: {
    alias: {
      // The example consumes the network layer from source, so that changing
      // it is a reload rather than a publish.
      "@hiett/lightning-relay": fileURLToPath(new URL("../../js/src/index.ts", import.meta.url)),
    },
  },
  server: { port: 5173 },
});
