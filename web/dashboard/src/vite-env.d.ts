/// <reference types="vite/client" />
/// <reference types="vite-plugin-pwa/client" />

declare module "node:fs" {
  export function readFileSync(path: string, encoding: string): string;
}
