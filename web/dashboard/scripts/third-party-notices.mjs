import { readFileSync, readdirSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../../..");

function filesBelow(directory) {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = resolve(directory, entry.name);
    return entry.isDirectory() ? filesBelow(path) : [path];
  }).sort();
}

// Conservatively preserve the inspected browser-library and font notices. The
// same data travels with the standalone MCP HTML instead of requiring a link.
export function browserThirdPartyNotices() {
  const sections = [
    "Hank browser code — third-party notices\n\nOriginal Hank material remains rights reserved. Third-party code retains its own terms below.\nSource and exact version inventory: https://github.com/AaronCampbellit/HankServerside-public/blob/main/THIRD_PARTY_NOTICES.md\n",
  ];
  for (const scope of ["npm", "fonts"]) {
    for (const path of filesBelow(resolve(repositoryRoot, "third-party-licenses", scope))) {
      sections.push(`\n===== ${path.slice(repositoryRoot.length + 1)} =====\n${readFileSync(path, "utf8")}`);
    }
  }
  return sections.join("\n");
}

export function thirdPartyNoticesPlugin() {
  return {
    name: "hank-third-party-notices",
    apply: "build",
    generateBundle() {
      this.emitFile({ type: "asset", fileName: "assets/THIRD_PARTY_NOTICES.txt", source: browserThirdPartyNotices() });
    },
    transformIndexHtml() {
      return [{ tag: "link", attrs: { rel: "license", href: "/assets/THIRD_PARTY_NOTICES.txt" }, injectTo: "head" }];
    },
  };
}
