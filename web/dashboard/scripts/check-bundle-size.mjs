import { readFile, readdir, stat } from "node:fs/promises";
import { gzipSync } from "node:zlib";
import path from "node:path";

const MAX_CHUNK_BYTES = 500_000;
const MAX_MCP_KANBAN_BYTES = 1_461_000;
const MAX_MCP_KANBAN_GZIP_BYTES = 342_000;
const assetsDirectory = path.resolve(process.argv[2] || "../../internal/cloud/ui/react/assets");
const chunkNames = (await readdir(assetsDirectory)).filter((name) => name.endsWith(".js"));
const chunks = await Promise.all(chunkNames.map(async (name) => ({
  name,
  size: (await stat(path.join(assetsDirectory, name))).size,
})));
const oversized = chunks.filter((chunk) => chunk.size > MAX_CHUNK_BYTES);

if (oversized.length) {
  for (const chunk of oversized) {
    console.error(`${chunk.name}: ${chunk.size} bytes exceeds the ${MAX_CHUNK_BYTES}-byte bundle budget.`);
  }
  process.exitCode = 1;
} else {
  const largest = chunks.sort((left, right) => right.size - left.size)[0];
  console.log(`Bundle budget passed: ${largest?.name || "no JavaScript chunks"}${largest ? ` is ${largest.size} bytes` : ""}.`);
}

const mcpKanbanPath = path.resolve("../../internal/cloud/ui/mcp/kanban-v1.html");
const mcpKanban = await readFile(mcpKanbanPath);
const mcpGzipBytes = gzipSync(mcpKanban).byteLength;
if (mcpKanban.byteLength > MAX_MCP_KANBAN_BYTES || mcpGzipBytes > MAX_MCP_KANBAN_GZIP_BYTES) {
  console.error(`Kanban MCP App: ${mcpKanban.byteLength}/${MAX_MCP_KANBAN_BYTES} bytes, ${mcpGzipBytes}/${MAX_MCP_KANBAN_GZIP_BYTES} gzip bytes.`);
  process.exitCode = 1;
} else {
  console.log(`Kanban MCP App budget passed: ${mcpKanban.byteLength} bytes, ${mcpGzipBytes} gzip bytes.`);
}
