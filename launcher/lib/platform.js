// Platform resolution: Node (process.platform/process.arch) → the release
// matrix (p9.build-matrix: linux/darwin/windows × amd64/arm64) and the
// archive/member naming scheme of scripts/release/build.sh.
//
// build.sh produces, for VERSION=vX.Y.Z:
//   archive:  onegate_vX.Y.Z_<os>_<arch>.tar.gz   (linux, darwin)
//             onegate_vX.Y.Z_<os>_<arch>.zip      (windows)
//   member:   onegate_vX.Y.Z_<os>_<arch>          (posix)
//             onegate_vX.Y.Z_<os>_<arch>.exe      (windows)
//   sums:     SHA256SUMS with `<sha256>  <archive>` lines (GNU sha256sum
//             format; a `*` before the name marks binary mode — both are
//             accepted by lib/checksums.js).
//
// Extraction uses the system `tar`: GNU tar on Linux reads .tar.gz; bsdtar
// (macOS, Windows 10 1803+) reads .tar.gz AND .zip. One tool, all six
// targets — zero npm dependencies.

"use strict"

const { execFile } = require("child_process")

// MATRIX is the single source of truth for supported combinations; the
// error message and the tests both derive from it.
const MATRIX = [
  ["linux", "amd64"],
  ["linux", "arm64"],
  ["darwin", "amd64"],
  ["darwin", "arm64"],
  ["windows", "amd64"],
  ["windows", "arm64"],
]

// PLATFORM_MAP: node (platform, arch) → matrix (os, arch). Unsupported
// combinations fall through to a PlatformError at resolve() time.
const PLATFORM_MAP = {
  "linux+x64": ["linux", "amd64"],
  "linux+arm64": ["linux", "arm64"],
  "darwin+x64": ["darwin", "amd64"],
  "darwin+arm64": ["darwin", "arm64"],
  "win32+x64": ["windows", "amd64"],
  "win32+arm64": ["windows", "arm64"],
}

// PlatformError is thrown for an unsupported host; main() maps it to
// exit code 1 with the supported-matrix listing.
class PlatformError extends Error {
  constructor(nodePlatform, nodeArch) {
    super(
      `unsupported platform: ${nodePlatform}/${nodeArch}. ` +
        `OneGate ships prebuilt binaries for: ${MATRIX.map(([os, arch]) => `${os}/${arch}`).join(", ")}.`,
    )
    this.name = "PlatformError"
    this.exitCode = 1
  }
}

// normalizeVersion strips a leading "v" so `v1.0.0` and `1.0.0` are the
// same release. Returns null for values that are not 1-64 chars of
// [A-Za-z0-9.+-] (guards URL construction from ONEGATE_VERSION).
function normalizeVersion(raw) {
  if (typeof raw !== "string") return null
  const v = raw.trim().replace(/^v/, "")
  if (!/^[A-Za-z0-9.+-]{1,64}$/.test(v)) return null
  return v
}

// resolve maps the running host onto the release matrix.
function resolve() {
  const key = `${process.platform}+${process.arch}`
  const mapped = PLATFORM_MAP[key]
  if (!mapped) throw new PlatformError(process.platform, process.arch)
  return { os: mapped[0], arch: mapped[1], windows: mapped[0] === "windows" }
}

// archiveName builds the release archive file name for a version.
// Windows archives are named after the binary they contain
// (build.sh: member `onegate_v1.0.0_windows_amd64.exe` zips to
// `onegate_v1.0.0_windows_amd64.exe.zip`) — caught by the real-release
// npx smoke on v1.0.0, which looked for a plain `.zip` name.
function archiveName(version, os, arch) {
  if (os === "windows") return memberName(version, os, arch) + ".zip"
  return `onegate_v${version}_${os}_${arch}.tar.gz`
}

// memberName builds the file name inside the archive (the bare binary).
function memberName(version, os, arch) {
  const ext = os === "windows" ? ".exe" : ""
  return `onegate_v${version}_${os}_${arch}${ext}`
}

// binaryCacheName is the installed file name inside the launcher cache.
function binaryCacheName(os, arch) {
  const ext = os === "windows" ? ".exe" : ""
  return `onegate${ext}`
}

// extractArchive extracts a single named member from archive into dir.
// Uses the system tar (see module comment); never a shell.
//
// Runs with cwd=dir and RELATIVE names: an absolute windows path
// (`D:\a\...`) makes bsdtar parse `D:` as a remote host ("Cannot
// connect to D") — found by the windows npx smoke on v1.0.0. Relative
// names carry no colon, so both GNU tar and bsdtar behave identically
// on every platform.
function extractArchive(archive, member, dir) {
  const relArchive = archive.split(/[\\/]/).pop()
  const relMember = member.split(/[\\/]/).pop()
  return new Promise((resolvePromise, reject) => {
    execFile("tar", ["-xf", relArchive, "-C", ".", relMember], { cwd: dir }, (err) => {
      if (err) {
        const e = new Error(`tar -xf failed: ${err.message}`)
        e.name = "ExtractError"
        e.exitCode = 4
        reject(e)
        return
      }
      resolvePromise()
    })
  })
}

module.exports = {
  MATRIX,
  PlatformError,
  normalizeVersion,
  resolve,
  archiveName,
  memberName,
  binaryCacheName,
  extractArchive,
}
