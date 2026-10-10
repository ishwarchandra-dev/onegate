// Test fixtures: a mock release directory served over a local HTTP server
// (p9.npx-launcher tests). The fixture mirrors scripts/release/build.sh
// output for the CURRENT platform (tar.gz+sh stub on posix, zip+cmd stub
// on windows) so install/e2e tests exercise the real download → verify →
// extract → exec path end-to-end without network access.

"use strict"

const { execFile } = require("child_process")
const fs = require("fs")
const os = require("os")
const path = require("path")
const http = require("http")

const platform = require("../lib/platform")
const checksums = require("../lib/checksums")

// makeRelease(dir, version) writes, under dir/v<version>/:
//   onegate_v<version>_<os>_<arch>.<tar.gz|zip>   real archive (system tar)
//   SHA256SUMS                                     GNU sha256sum format
// and returns { archive, member, archivePath, sumsPath, sums: Map }.
// The archive's single member is a stub "binary": a #!/bin/sh script on
// posix that echoes its args; a batch file on windows (executability of
// stubs is asserted only on posix — see e2e test).
async function makeRelease(dir, version, opts = {}) {
  const target = platform.resolve()
  const archive = platform.archiveName(version, target.os, target.arch)
  const member = platform.memberName(version, target.os, target.arch)
  const tagDir = path.join(dir, `v${version}`)
  await fs.promises.mkdir(tagDir, { recursive: true })

  // stub binary content: prints a fixed marker + forwarded argv
  const marker = `stub-onegate v${version}`
  let stub
  if (target.windows) {
    stub = `@echo off\r\necho ${marker} %*\r\n`
  } else {
    stub = `#!/bin/sh
printf '%s' "${marker} "
for a in "$@"; do printf '%s ' "$a"; done
echo
`
  }

  const work = await fs.promises.mkdtemp(path.join(os.tmpdir(), "ogfixture-"))
  const memberPath = path.join(work, member)
  await fs.promises.writeFile(memberPath, stub, { mode: 0o755 })

  const archivePath = path.join(tagDir, archive)
  await tarCreate(archivePath, member, work)

  // SHA256SUMS in GNU format ("<hex>  <name>"), hex from crypto (same
  // digest sha256sum would print — cross-platform without the tool).
  const buf = await fs.promises.readFile(archivePath)
  const sums = new Map([[archive, checksums.hexDigest(buf)]])
  const sumsText = [...sums.entries()].map(([n, h]) => `${h}  ${n}`).join("\n") + "\n"
  const sumsPath = path.join(tagDir, "SHA256SUMS")
  await fs.promises.writeFile(sumsPath, sumsText)

  await fs.promises.rm(work, { recursive: true, force: true })
  return { archive, member, archivePath, sumsPath, sums, marker, target }
}

// tarCreate archives `member` (in dir) into `out` using the system tar.
function tarCreate(out, member, dir) {
  const args = os.platform() === "win32" ? ["-a", "-cf", out, member] : ["-czf", out, member]
  return new Promise((resolvePromise, reject) => {
    execFile("tar", args, { cwd: dir }, (err) => (err ? reject(err) : resolvePromise()))
  })
}

// serveDir(dir) starts an http server that maps URL paths onto files in
// dir; returns { url, server, hits } — hits counts requests by path so
// tests can prove the cache fast path makes zero network calls.
function serveDir(dir) {
  const hits = new Map()
  const count = (p) => hits.set(p, (hits.get(p) || 0) + 1)
  const server = http.createServer((req, res) => {
    const rel = decodeURIComponent(req.url.split("?")[0]).replace(/^\/+/, "")
    const file = path.join(dir, rel)
    if (!file.startsWith(path.resolve(dir))) {
      res.statusCode = 403
      res.end("forbidden")
      return
    }
    fs.readFile(file, (err, data) => {
      count(rel)
      if (err) {
        res.statusCode = 404
        res.end("not found")
        return
      }
      res.statusCode = 200
      res.end(data)
    })
  })
  return {
    hits,
    url: "", // filled in by start()
    async start() {
      await new Promise((resolvePromise) => server.listen(0, "127.0.0.1", resolvePromise))
      this.url = `http://127.0.0.1:${server.address().port}`
      return this.url
    },
    async stop() {
      await new Promise((resolvePromise) => server.close(() => resolvePromise()))
    },
    // corruptArchive rewrites the served bytes for `name` so the digest
    // no longer matches SHA256SUMS (a flipped byte mid-archive).
    async corruptArchive(name) {
      const p = path.join(dir, name)
      const buf = await fs.promises.readFile(p)
      buf[Math.floor(buf.length / 2)] ^= 0x5a
      await fs.promises.writeFile(p, buf)
    },
  }
}

module.exports = { makeRelease, serveDir }
