#!/usr/bin/env node
// Runs the platform binary that install.js placed next to this file. When npm
// skipped install scripts (--ignore-scripts, or a policy that blocks them),
// the binary is missing, so the first run downloads it to a per-user cache.
const fs = require("fs");
const path = require("path");
const { spawnSync } = require("child_process");
const { install, cacheDir } = require("../install.js");

function run(bin) {
  const result = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" });
  if (result.error) {
    console.error(`webctl: ${result.error.message}; reinstall with \`npm install -g webctl\``);
    process.exit(1);
  }
  process.exit(result.status ?? 1);
}

const local = path.join(__dirname, "webctl");
const cached = path.join(cacheDir(), "webctl");
if (fs.existsSync(local)) {
  run(local);
} else if (fs.existsSync(cached)) {
  run(cached);
} else {
  console.error("webctl: downloading the webctl binary (first run)...");
  install(cacheDir()).then(run, (err) => {
    console.error(`webctl: failed to download the binary: ${err.message}`);
    process.exit(1);
  });
}
