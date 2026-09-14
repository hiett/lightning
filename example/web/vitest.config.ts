import { defineConfig, mergeConfig } from "vitest/config";
import viteConfig from "./vite.config";

// The e2e test mounts the real components, so it needs the same transform the
// dev server uses: babel-plugin-relay turns the graphql`` tags into references
// to the artifacts relay-compiler generated.
export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      environment: "jsdom",
      include: ["e2e/**/*.test.tsx"],
      setupFiles: ["./e2e/setup.ts"],
      testTimeout: 30_000,
      hookTimeout: 30_000,
    },
  }),
);
