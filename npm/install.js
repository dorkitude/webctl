// Downloads the webctl release binary matching this package's version and
// platform from GitHub Releases and checks it against the release's
// checksums.txt. npm runs this file on install (postinstall) to put the
// binary in bin/; bin/webctl.js calls install() on first run when npm skipped
// install scripts, putting it in a per-user cache instead.
const crypto = require("crypto");
const fs = require("fs");
const os = require("os");
const path = require("path");
const https = require("https");
const { execFileSync } = require("child_process");

const { version } = require("./package.json");
const platform = { darwin: "darwin", linux: "linux" }[process.platform];
const arch = { x64: "amd64", arm64: "arm64" }[process.arch];
const base = `https://github.com/dorkitude/webctl/releases/download/v${version}`;
const asset = `webctl_${version}_${platform}_${arch}.tar.gz`;

// cacheDir is where bin/webctl.js keeps a binary it had to download itself.
function cacheDir() {
  const root = process.env.XDG_CACHE_HOME || path.join(os.homedir(), ".cache");
  return path.join(root, "webctl", version);
}

function get(u, redirects = 0) {
  return new Promise((resolve, reject) => {
    https.get(u, { headers: { "User-Agent": "webctl-npm" } }, (res) => {
      if ([301, 302, 303, 307, 308].includes(res.statusCode) && res.headers.location && redirects < 5) {
        res.resume();
        return resolve(get(res.headers.location, redirects + 1));
      }
      if (res.statusCode !== 200) {
        res.resume();
        return reject(new Error(`HTTP ${res.statusCode} for ${u}`));
      }
      const chunks = [];
      res.on("data", (c) => chunks.push(c));
      res.on("end", () => resolve(Buffer.concat(chunks)));
      res.on("error", reject);
    }).on("error", reject);
  });
}

// install downloads the binary into dir and resolves to its path. The file is
// written under a temporary name and renamed into place, so a concurrent run
// never sees a partial binary.
async function install(dir) {
  if (!platform || !arch) {
    throw new Error(`no prebuilt binary for ${process.platform}/${process.arch}; use go install github.com/dorkitude/webctl/cmd/webctl@latest`);
  }
  const [tarball, sums] = await Promise.all([get(`${base}/${asset}`), get(`${base}/checksums.txt`)]);
  const want = sums.toString().split("\n").map((l) => l.trim().split(/\s+/)).find((f) => f[1] === asset);
  const got = crypto.createHash("sha256").update(tarball).digest("hex");
  if (!want || want[0] !== got) {
    throw new Error(`checksum mismatch for ${asset}`);
  }
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "webctl-"));
  try {
    fs.writeFileSync(path.join(tmp, asset), tarball);
    execFileSync("tar", ["-xzf", path.join(tmp, asset), "-C", tmp, "webctl"]);
    fs.mkdirSync(dir, { recursive: true });
    const dest = path.join(dir, "webctl");
    const part = `${dest}.${process.pid}.part`;
    fs.copyFileSync(path.join(tmp, "webctl"), part);
    fs.chmodSync(part, 0o755);
    fs.renameSync(part, dest);
    return dest;
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

module.exports = { install, cacheDir };

if (require.main === module) {
  install(path.join(__dirname, "bin")).catch((err) => {
    console.error(`webctl: failed to install the binary: ${err.message}`);
    process.exit(1);
  });
}
