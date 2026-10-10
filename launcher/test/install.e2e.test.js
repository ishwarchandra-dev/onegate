// Install + end-to-end launcher tests against a local fixture release
// server (p9.npx-launcher acceptance: "npx onegate works on all matrix
// platforms; corrupt download refuses to run").
//
// Platform note: these tests exercise the CURRENT host (linux/darwin/
// windows CI runners). The fixture builds the real archive type for the
// host (tar.gz + sh stub on posix; zip + cmd stub on windows), so the
// download → verify → extract → place path runs for real everywhere;
// the final exec step asserts output on posix and file presence on
// windows (a stub renamed .exe is not a PE image).
//
// Every test registers its cleanup via t.after() BEFORE asserting, so a
// failed assertion can never leak a server or tmp dir (which would hang
// the runner).

"use strict"

const test = require("node:test")
const assert = require("node:assert/strict")
const fs = require("fs")
const os = require("os")
const path = require("path")

const { makeRelease, serveDir } = require("./fixtures")
const platform = require("../lib/platform")
const install = require("../lib/install")
const http = require("../lib/http")
const { main } = require("../lib/main")

const VERSION = "9.9.9-test"

// testIO builds the io object main() expects in capture mode.
function testIO() {
  const out = []
  const err = []
  return {
    stderr: {
      write: (m) => {
        err.push(m)
        process.stderr.write(m) // tee: visible in CI logs on failure
      },
    },
    captureStdout: out,
    captureStderr: err,
    outText: () => out.join(""),
    errText: () => err.join(""),
  }
}

async function tmpCache() {
  return fs.promises.mkdtemp(path.join(os.tmpdir(), "ogcache-"))
}

// noStagingLeft asserts the cache tree contains no .staging-* dirs and
// no version dir was half-populated (install failures clean up).
async function treeState(cacheDir) {
  const found = { staging: 0, entries: [] }
  async function walk(dir) {
    let dirents
    try {
      dirents = await fs.promises.readdir(dir, { withFileTypes: true })
    } catch {
      return
    }
    for (const d of dirents) {
      if (d.name.startsWith(".staging-")) {
        found.staging += 1
        continue
      }
      const p = path.join(dir, d.name)
      if (d.isDirectory()) await walk(p)
      else found.entries.push(path.relative(cacheDir, p))
    }
  }
  await walk(cacheDir)
  return found
}

// cleanupOf returns a t.after() body that stops the fixture server and
// removes both tmp trees.
function cleanupOf(server, ...dirs) {
  return async () => {
    await server.stop()
    for (const d of dirs) await fs.promises.rm(d, { recursive: true, force: true })
  }
}

test("e2e: install from fixture release, run, and forward args", async (t) => {
  // The fixture lives under serveRoot/v<version>/ (the served tree);
  // ONEGATE_LAUNCHER_CACHE points at a separate dir so a cache hit can
  // never be confused with the served fixture.
  const serveRoot = await tmpCache()
  const rel = await makeRelease(serveRoot, VERSION)
  const cache = await tmpCache()
  const server = serveDir(serveRoot)
  const url = await server.start()
  const io = testIO()
  t.after(cleanupOf(server, cache, serveRoot))

  const code = await main(["--version", "extra-arg"], {
    ONEGATE_VERSION: VERSION,
    ONEGATE_DOWNLOAD_ROOT: url,
    ONEGATE_LAUNCHER_CACHE: cache,
  }, io)

  assert.equal(code, 0, `stderr: ${io.errText()}`)
  if (platform.resolve().windows) {
    // windows stub: .exe-named batch text; assert the placed file exists.
    const t2 = platform.resolve()
    const bin = path.join(cache, `v${VERSION}`, `${t2.os}_${t2.arch}`, "onegate.exe")
    await fs.promises.access(bin)
  } else {
    assert.match(io.outText(), /stub-onegate v9\.9\.9-test --version extra-arg/)
  }
  // exactly two network fetches: SHA256SUMS + archive
  assert.equal(server.hits.get(`v${VERSION}/SHA256SUMS`) || 0, 1)
  assert.equal(server.hits.get(`v${VERSION}/${rel.archive}`) || 0, 1)
})

test("e2e: second run is a cache hit — zero network", async (t) => {
  const serveRoot = await tmpCache()
  await makeRelease(serveRoot, VERSION)
  const cache = await tmpCache()
  const server = serveDir(serveRoot)
  await server.start()
  t.after(cleanupOf(server, cache, serveRoot))

  const first = await main(["--version"], {
    ONEGATE_VERSION: VERSION,
    ONEGATE_DOWNLOAD_ROOT: server.url,
    ONEGATE_LAUNCHER_CACHE: cache,
  }, testIO())
  assert.equal(first, 0)

  const hitsBefore = [...server.hits.values()].reduce((a, b) => a + b, 0)
  await server.stop() // no server anymore: proves no network is needed

  const second = await main(["--version"], {
    ONEGATE_VERSION: VERSION,
    ONEGATE_DOWNLOAD_ROOT: server.url, // dead URL, on purpose
    ONEGATE_LAUNCHER_CACHE: cache,
  }, testIO())
  assert.equal(second, 0)
  assert.equal([...server.hits.values()].reduce((a, b) => a + b, 0), hitsBefore)
})

test("acceptance: corrupt download refuses to run (exit 3, no leftovers)", async (t) => {
  const serveRoot = await tmpCache()
  await makeRelease(serveRoot, VERSION)
  const cache = await tmpCache()
  const server = serveDir(serveRoot)
  await server.start()
  t.after(cleanupOf(server, cache, serveRoot))

  // Corrupt the served archive bytes AFTER the manifest was written:
  // download succeeds, digest differs → must refuse.
  const here = platform.resolve()
  await server.corruptArchive(`v${VERSION}/${platform.archiveName(VERSION, here.os, here.arch)}`)

  const io = testIO()
  const code = await main(["--version"], {
    ONEGATE_VERSION: VERSION,
    ONEGATE_DOWNLOAD_ROOT: server.url,
    ONEGATE_LAUNCHER_CACHE: cache,
  }, io)

  assert.equal(code, 3, `expected exit 3, stderr: ${io.errText()}`)
  assert.match(io.errText(), /checksum mismatch|corrupt/i)

  const state = await treeState(cache)
  assert.equal(state.staging, 0, "staging dirs must be cleaned up")
  assert.deepEqual(state.entries, [], "no binary may be placed from a corrupt download")
})

test("SHA256SUMS without an entry for the archive refuses (exit 3)", async (t) => {
  const serveRoot = await tmpCache()
  await makeRelease(serveRoot, VERSION)
  const cache = await tmpCache()

  // rewrite the manifest with only an unrelated entry
  const sumsPath = path.join(serveRoot, `v${VERSION}`, "SHA256SUMS")
  await fs.promises.writeFile(sumsPath, `${"b".repeat(64)}  onegate_v0.0.0_other_platform.tar.gz\n`)

  const server = serveDir(serveRoot)
  await server.start()
  t.after(cleanupOf(server, cache, serveRoot))

  const io = testIO()
  const code = await main(["--version"], {
    ONEGATE_VERSION: VERSION,
    ONEGATE_DOWNLOAD_ROOT: server.url,
    ONEGATE_LAUNCHER_CACHE: cache,
  }, io)
  assert.equal(code, 3)
  assert.match(io.errText(), /no entry/)
})

test("malformed SHA256SUMS refuses (exit 3)", async (t) => {
  const serveRoot = await tmpCache()
  await makeRelease(serveRoot, VERSION)
  const cache = await tmpCache()
  await fs.promises.writeFile(path.join(serveRoot, `v${VERSION}`, "SHA256SUMS"), "garbage that is not a manifest\n")

  const server = serveDir(serveRoot)
  await server.start()
  t.after(cleanupOf(server, cache, serveRoot))

  const io = testIO()
  const code = await main(["--version"], {
    ONEGATE_VERSION: VERSION,
    ONEGATE_DOWNLOAD_ROOT: server.url,
    ONEGATE_LAUNCHER_CACHE: cache,
  }, io)
  assert.equal(code, 3)
  assert.match(io.errText(), /malformed line/)
})

test("unreachable download root is exit 2, not a hang", async (t) => {
  const cache = await tmpCache()
  t.after(async () => fs.promises.rm(cache, { recursive: true, force: true }))

  const io = testIO()
  const code = await main(["--version"], {
    ONEGATE_VERSION: VERSION,
    ONEGATE_DOWNLOAD_ROOT: "http://127.0.0.1:1", // nothing listens on 1
    ONEGATE_LAUNCHER_CACHE: cache,
  }, io)
  assert.equal(code, 2, `stderr: ${io.errText()}`)
})

test("ONEGATE_BINARY override runs the given binary with no network", async (t) => {
  const dir = await tmpCache()
  t.after(async () => fs.promises.rm(dir, { recursive: true, force: true }))

  const stubName = platform.resolve().windows ? "stub.cmd" : "stub.sh"
  const stubPath = path.join(dir, stubName)
  const here = platform.resolve()
  if (here.windows) {
    await fs.promises.writeFile(stubPath, "@echo off\necho override-ran %*\n")
  } else {
    await fs.promises.writeFile(stubPath, "#!/bin/sh\necho \"override-ran $*\"\n", { mode: 0o755 })
  }

  const io = testIO()
  const code = await main(["hello"], { ONEGATE_BINARY: stubPath }, io)
  assert.equal(code, 0)
  if (!here.windows) assert.match(io.outText(), /override-ran hello/)

  // missing override path → exit 1
  const io2 = testIO()
  const code2 = await main([], { ONEGATE_BINARY: path.join(dir, "does-not-exist") }, io2)
  assert.equal(code2, 1)
  assert.match(io2.errText(), /does not exist/)
})

test("child exit code is forwarded verbatim (42)", { skip: os.platform() === "win32" }, async (t) => {
  const dir = await tmpCache()
  t.after(async () => fs.promises.rm(dir, { recursive: true, force: true }))

  const stub = path.join(dir, "exit42.sh")
  await fs.promises.writeFile(stub, "#!/bin/sh\nexit 42\n", { mode: 0o755 })
  const code = await main([], { ONEGATE_BINARY: stub }, testIO())
  assert.equal(code, 42)
})

test("SIGTERM to the launcher is forwarded to the child", { skip: os.platform() === "win32" }, async (t) => {
  // Run the real bin entry in a subprocess so the LAUNCHER process can be
  // signalled directly (the way a supervisor or `kill` would).
  const dir = await tmpCache()
  t.after(async () => fs.promises.rm(dir, { recursive: true, force: true }))

  const stub = path.join(dir, "trap.sh")
  await fs.promises.writeFile(
    stub,
    ["#!/bin/sh", "trap 'exit 143' TERM", "while true; do sleep 0.1; done"].join("\n"),
    { mode: 0o755 },
  )

  const { spawn } = require("child_process")
  const child = spawn(process.execPath, [path.join(__dirname, "..", "bin", "onegate.js")], {
    env: { ...process.env, ONEGATE_BINARY: stub },
    stdio: ["ignore", "ignore", "ignore"],
  })
  t.after(() => {
    try {
      child.kill("SIGKILL")
    } catch {
      /* already gone */
    }
  })

  await new Promise((r) => setTimeout(r, 500)) // let it boot the stub
  child.kill("SIGTERM")
  const result = await new Promise((r) => child.on("close", (c, s) => r({ c, s })))
  // 128 + SIGTERM(15): forwarded, not orphaned, not swallowed
  assert.equal(result.c, 143, `close: code=${result.c} signal=${result.s}`)
})

test("bad ONEGATE_VERSION is rejected before any network work (exit 1)", async (t) => {
  const cache = await tmpCache()
  t.after(async () => fs.promises.rm(cache, { recursive: true, force: true }))

  const io = testIO()
  const code = await main([], {
    ONEGATE_VERSION: "../../etc/passwd",
    ONEGATE_DOWNLOAD_ROOT: "https://example.invalid",
    ONEGATE_LAUNCHER_CACHE: cache,
  }, io)
  assert.equal(code, 1)
  assert.match(io.errText(), /not a recognizable version/)
})

test("non-http(s) download root is rejected (exit 1)", async (t) => {
  const cache = await tmpCache()
  t.after(async () => fs.promises.rm(cache, { recursive: true, force: true }))

  const io = testIO()
  const code = await main([], {
    ONEGATE_VERSION: VERSION,
    ONEGATE_DOWNLOAD_ROOT: "ftp://example.invalid",
    ONEGATE_LAUNCHER_CACHE: cache,
  }, io)
  assert.equal(code, 1)
  assert.match(io.errText(), /http\(s\) URL/)
})

test("install.ensureInstalled exposes cached:true on second call", async (t) => {
  const serveRoot = await tmpCache()
  await makeRelease(serveRoot, VERSION)
  const cache = await tmpCache()
  const server = serveDir(serveRoot)
  await server.start()
  t.after(cleanupOf(server, cache, serveRoot))

  const target = platform.resolve()
  const first = await install.ensureInstalled({
    root: server.url,
    version: VERSION,
    cacheDir: cache,
    target,
    log: () => {},
  })
  assert.equal(first.cached, false)
  assert.ok(first.binPath.includes(path.join(`v${VERSION}`, `${target.os}_${target.arch}`)))

  const second = await install.ensureInstalled({
    root: server.url,
    version: VERSION,
    cacheDir: cache,
    target,
    log: () => {},
  })
  assert.equal(second.cached, true)
  assert.equal(second.binPath, first.binPath)
})

test("http.js: redirects are followed (allowHttp root), 404 is DownloadError", async (t) => {
  // A dedicated tiny server: /hop1 302→ /hop2 302→ /ok 200; /missing 404.
  const hits = []
  const srv = redirectServer(hits)
  const url = await srv.start()
  t.after(async () => {
    await srv.stop()
  })

  const dir = await tmpCache()
  t.after(async () => fs.promises.rm(dir, { recursive: true, force: true }))
  const dest = path.join(dir, "dl.bin")

  await http.downloadToFile(`${url}/hop1`, dest, { allowHttp: true })
  assert.equal((await fs.promises.readFile(dest)).toString(), "hopped-payload")
  assert.deepEqual(hits, ["/hop1", "/hop2", "/ok"])

  await assert.rejects(() => http.downloadToFile(`${url}/missing`, dest, { allowHttp: true }), (err) => {
    assert.equal(err.name, "DownloadError")
    assert.equal(err.status, 404)
    assert.equal(err.exitCode, 2)
    return true
  })

  // https-only policy for https roots: an http URL is refused outright
  // (no socket is opened — checked by the empty hits list).
  const before = hits.length
  await assert.rejects(() => http.downloadToFile("http://example.invalid/x", dest, { allowHttp: false }), (err) => {
    assert.match(err.message, /refusing http/)
    return true
  })
  assert.equal(hits.length, before)

  function redirectServer(hitsArr) {
    const httpMod = require("http")
    const server = httpMod.createServer((req, res) => {
      hitsArr.push(req.url)
      if (req.url === "/hop1") {
        res.statusCode = 302
        res.setHeader("Location", "/hop2")
        res.end()
      } else if (req.url === "/hop2") {
        res.statusCode = 302
        res.setHeader("Location", "/ok")
        res.end()
      } else if (req.url === "/ok") {
        res.end("hopped-payload")
      } else {
        res.statusCode = 404
        res.end("not found")
      }
    })
    return {
      async start() {
        await new Promise((r) => server.listen(0, "127.0.0.1", r))
        return `http://127.0.0.1:${server.address().port}`
      },
      async stop() {
        await new Promise((r) => server.close(() => r()))
      },
    }
  }
})
