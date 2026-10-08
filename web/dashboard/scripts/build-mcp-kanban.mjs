import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { gzipSync } from "node:zlib";
import { browserThirdPartyNotices } from "./third-party-notices.mjs";

// First production build (2026-08-11) plus 20% headroom.
const MAX_UNCOMPRESSED_BYTES = 1_461_000;
const MAX_GZIP_BYTES = 342_000;

const dashboardRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const outputRoot = resolve(dashboardRoot, ".mcp-kanban-dist");
const embeddedPath = resolve(dashboardRoot, "../../internal/cloud/ui/mcp/kanban-v1.html");

execFileSync(process.execPath, [resolve(dashboardRoot, "node_modules/vite/bin/vite.js"), "build", "--config", resolve(dashboardRoot, "vite.mcp-kanban.config.ts")], {
  cwd: dashboardRoot,
  env: { ...process.env, NODE_ENV: "production" },
  stdio: "inherit",
});

const files = readdirSync(outputRoot);
const scripts = files.filter((file) => file.endsWith(".js"));
const styles = files.filter((file) => file.endsWith(".css"));
if (scripts.length !== 1 || styles.length !== 1) {
  throw new Error(`Expected one JavaScript and one CSS asset, received ${JSON.stringify(files)}`);
}
const script = readFileSync(resolve(outputRoot, scripts[0]), "utf8").replaceAll("</script", "<\\/script").replace(/^[ \t]+$/gm, "");
const style = readFileSync(resolve(outputRoot, styles[0]), "utf8").replaceAll("</style", "<\\/style").replace(/^[ \t]+$/gm, "");
const thirdPartyNotices = JSON.stringify(browserThirdPartyNotices()).replaceAll("<", "\\u003c");
const html = `<!doctype html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Hank Kanban</title>
  <script type="application/json" id="hank-third-party-notices">${thirdPartyNotices}</script>
  <style>${style}</style>
</head>
<body>
  <div id="hank-kanban-root"><main class="kanban-app kanban-centered" role="status">Opening Hank Kanban…</main></div>
  <script type="module">${script}</script>
</body>
</html>
`;
const htmlBytes = Buffer.byteLength(html);
const gzipBytes = gzipSync(html).byteLength;
if (htmlBytes > MAX_UNCOMPRESSED_BYTES || gzipBytes > MAX_GZIP_BYTES) {
  throw new Error(`Kanban MCP App bundle exceeds budget: ${htmlBytes}/${MAX_UNCOMPRESSED_BYTES} bytes, ${gzipBytes}/${MAX_GZIP_BYTES} gzip bytes`);
}
if (/<script[^>]+src\s*=|<link[^>]+href\s*=/i.test(`<style>${style}</style><script type="module"></script>`)) {
  throw new Error("Kanban MCP App template contains an external asset reference");
}
mkdirSync(dirname(embeddedPath), { recursive: true });
writeFileSync(embeddedPath, html, "utf8");
rmSync(outputRoot, { recursive: true, force: true });
console.log(`Built ${embeddedPath} (${htmlBytes} bytes, ${gzipBytes} gzip bytes)`);
