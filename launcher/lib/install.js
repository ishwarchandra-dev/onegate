// Install pipeline: cache lookup → download → verify → extract → atomically
// place the binary (p9.npx-launcher).
//
// Cache layout (ONEGATE_LAUNCHER_CACHE, default ~/.onegate/launcher):
//
//     <cache>/v<version>/<os>_<arch>/onegate[.exe]
//
// A cache hit short-circuits everything: no network, no re-verification
// (the file was verified at install time and the path is
// version-scoped — an upgraded version lands in a different directory).
//
// Install steps (all inside a staging dir on the same filesystem, so the
// final rename is atomic):
//
//     1. download SHA256SUMS           → parse (strict; malformed = refuse)
//     2. look up the archive's entry   → missing = refuse (exit 3)
//     3. download the archive to staging
//     4. sha256(archive) === entry     → mismatch = delete + refuse (exit 3)
//     5. tar -xf <archive> <member>    → into staging
//     6. assert member exists, exec bit on posix
//     7. mkdir -p target dir; rename member → target/onegate
//        (EEXIST means a concurrent run won the race: use theirs)
//
// An install never leaves partial state behind: every failure path
// removes the staging dir; the target path only ever appears via rename.

"use strict"

const fs = require("fs")
const os = require("os")
const path = require("path")

const platform = require("./platform")
const checksums = require("./checksums")
const http = require("./http")

// InstallError is a catch-all for install-time failures not already
// covered by Download/Verify/Extract errors; exit 4.
class InstallError extends Error {
  constructor(message) {
    super(message)
    this.name = "InstallError"
    this.exitCode = 4
  }
}

// defaultCacheDir is ~/.onegate/launcher (mirrors the gateway's own
// ~/.onegate data-dir convention from ADR 002).
function defaultCacheDir() {
  return path.join(os.homedir(), ".onegate", "launcher")
}

// rmTree removes dir recursively; best-effort (used for staging cleanup).
async function rmTree(dir) {
  await fs.promises.rm(dir, { recursive: true, force: true }).catch(() => {})
}

// ensureInstalled resolves the gateway binary for (version, os, arch),
// installing it into cacheDir if needed. Resolves { binPath, cached }.
//
// `log` is a sink for progress lines (main passes console.error); tests
// pass a collector.
async function ensureInstalled(opts) {
  const { root, version, cacheDir, log } = opts
  const { os: goOS, arch } = opts.target // from platform.resolve()
  const archive = platform.archiveName(version, goOS, arch)
  const member = platform.memberName(version, goOS, arch)
  const binName = platform.binaryCacheName(goOS, arch)
  const tagDir = path.join(cacheDir, `v${version}`, `${goOS}_${arch}`)
  const binPath = path.join(tagDir, binName)

  // Fast path: already installed. Existence check only — the file was
  // verified when it was placed (rename-in, never partially written), and
  // the directory is version-scoped.
  try {
    await fs.promises.access(binPath)
    return { binPath, cached: true }
  } catch {
    /* not installed yet */
  }

  await fs.promises.mkdir(tagDir, { recursive: true })
  const staging = await fs.promises.mkdtemp(path.join(tagDir, ".staging-"))

  const allowHttp = root.startsWith("http://")
  try {
    log(`onegate v${version} not cached — downloading from ${root}`)
    // 1. manifest
    const sumsUrl = `${root}/v${version}/SHA256SUMS`
    const sumsPath = path.join(staging, "SHA256SUMS")
    await http.downloadToFile(sumsUrl, sumsPath, { allowHttp })
    const sumsText = await fs.promises.readFile(sumsPath, "utf8")
    const sums = checksums.parseSums(sumsText)

    // 2. expected digest
    const expected = sums.get(archive)
    if (!expected) {
      throw new checksums.VerifyError(
        `SHA256SUMS has no entry for ${archive} — refusing to install an unverified archive`,
      )
    }

    // 3. archive
    const archivePath = path.join(staging, archive)
    await http.downloadToFile(`${root}/v${version}/${archive}`, archivePath, { allowHttp })

    // 4. verify
    const actual = await checksums.sha256File(archivePath)
    if (actual !== expected) {
      throw new checksums.VerifyError(
        `checksum mismatch for ${archive}: expected ${expected}, got ${actual} — ` +
          `the download is corrupt; refusing to run it`,
      )
    }

    // 5. extract the single member
    await platform.extractArchive(archivePath, member, staging)
    const extracted = path.join(staging, member)
    await fs.promises.access(extracted)
    if (os.platform() !== "win32") await fs.promises.chmod(extracted, 0o755)

    // 6. atomic place (concurrent-friendly)
    await fs.promises.mkdir(path.dirname(binPath), { recursive: true })
    try {
      await fs.promises.rename(extracted, binPath)
    } catch (err) {
      if (err.code === "EEXIST") {
        // Another concurrent run installed first; its copy was verified
        // the same way. Drop ours.
        return { binPath, cached: true }
      }
      throw err
    }
    log(`installed ${binPath}`)
    return { binPath, cached: false }
  } finally {
    await rmTree(staging)
  }
}

module.exports = { InstallError, defaultCacheDir, ensureInstalled, rmTree }
