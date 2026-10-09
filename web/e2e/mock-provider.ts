/**
 * Minimal openai-style mock provider for the E2E connection probe
 * (p6.e2e-dashboard). GET /models answers a model list; /healthz for
 * readiness. Auth is checked so a bad key yields 401 per-protocol.
 */
import { createServer } from "node:http"

createServer((req, res) => {
  const url = new URL(req.url ?? "/", "http://127.0.0.1:9441")
  if (url.pathname === "/healthz") {
    res.writeHead(200).end("ok")
    return
  }
  if (url.pathname === "/models" && req.method === "GET") {
    const auth = req.headers.authorization ?? ""
    if (auth !== "Bearer sk-e2e-mock-key") {
      res.writeHead(401, { "Content-Type": "application/json" })
      res.end(JSON.stringify({ error: { message: "bad key" } }))
      return
    }
    res.writeHead(200, { "Content-Type": "application/json" })
    res.end(JSON.stringify({ data: [{ id: "mock-model" }] }))
    return
  }
  res.writeHead(404).end()
}).listen(9441, "127.0.0.1", () => {
  console.log("mock provider on :9441")
})
