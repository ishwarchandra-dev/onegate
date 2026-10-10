import { defineConfig } from "@playwright/test"
import { join } from "node:path"

/** Repo root (this config lives in web/). */
const ROOT = join(import.meta.dirname, "..")

/**
 * E2E suite for the OneGate dashboard.
 *
 * Two servers boot before the tests (p9.embed-pipeline: the dashboard
 * now ships INSIDE the gateway binary, so the journeys run against the
 * real single-binary artifact — no vite preview hop anymore):
 *   1. the real gateway binary on :7420 (fresh temp data dir) serving
 *      dashboard + /api + proxy on one port
 *   2. a mock provider (openai-style /models) on :9441 for the
 *      connection-probe journey
 */
export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  // Journeys share one gateway (and its session store); run serially.
  workers: 1,
  fullyParallel: false,
  projects: [
    { name: "setup", testMatch: /setup\.spec\.ts/ },
    {
      name: "journeys",
      testMatch: /journeys\.spec\.ts/,
      dependencies: ["setup"],
      use: { storageState: ".auth/state.json" },
    },
  ],
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [["list"]] : [["list"]],
  use: {
    baseURL: "http://127.0.0.1:7420",
    trace: "retain-on-failure",
  },
  webServer: [
    {
      command: `${ROOT}/bin/onegate -data-dir /tmp/onegate-e2e -port 7420 -log-level warn`,
      url: "http://127.0.0.1:7420/healthz",
      reuseExistingServer: false,
      timeout: 30_000,
      stdout: "ignore",
      stderr: "ignore",
    },
    {
      command: "bun run e2e:mock-provider",
      url: "http://127.0.0.1:9441/healthz",
      reuseExistingServer: false,
      timeout: 15_000,
      stdout: "ignore",
    },
  ],
})
