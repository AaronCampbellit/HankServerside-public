import { readFileSync } from "node:fs";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";
import { VitePWA } from "vite-plugin-pwa";
import { thirdPartyNoticesPlugin } from "./scripts/third-party-notices.mjs";

// Optional API proxy for local dev against a running Hank deployment, e.g.:
//   HANK_DEV_API_PROXY=https://hankdemo.campbellservers.com npm run dev
// HANK_DEV_SESSION_TOKEN_FILE can point at a file holding a session token; the
// proxy then attaches it as the session cookie so the app is signed in.
const proxyTarget = process.env.HANK_DEV_API_PROXY;
const sessionTokenFile = process.env.HANK_DEV_SESSION_TOKEN_FILE;
const desktopAcceptanceIdentityFile = process.env.HANK_DEV_DESKTOP_IDENTITY_FILE;

function devSessionCookie(): string {
  if (!sessionTokenFile) return "";
  try {
    return `hank_session=${readFileSync(sessionTokenFile, "utf8").trim()}`;
  } catch {
    return "";
  }
}

export default defineConfig({
  plugins: [react(), thirdPartyNoticesPlugin(), VitePWA({
    strategies: "injectManifest",
    srcDir: "src/pwa",
    filename: "sw.ts",
    registerType: "prompt",
    injectRegister: false,
    includeManifestIcons: false,
    injectManifest: {
      globPatterns: ["**/*.{js,css,png,svg,ico,woff,woff2}", "index.html", "offline.html", "assets/THIRD_PARTY_NOTICES.txt"],
      rollupFormat: "iife",
    },
    manifest: {
      id: "/dashboard",
      name: "Hank",
      short_name: "Hank",
      description: "Hank home assistant, files, notes, automation, and AI.",
      start_url: "/dashboard",
      scope: "/",
      display: "standalone",
      background_color: "#0f141f",
      theme_color: "#0f141f",
      icons: [
        { src: "/assets/hank-icon-192.png", sizes: "192x192", type: "image/png", purpose: "any" },
        { src: "/assets/hank-icon-512.png", sizes: "512x512", type: "image/png", purpose: "any" },
      ],
    },
  }), {
    name: "hank-desktop-acceptance-identity",
    configureServer(server) {
      if (!desktopAcceptanceIdentityFile) return;
      server.middlewares.use("/__hank/desktop-acceptance-identity", (_request, response) => {
        try {
          const state = JSON.parse(readFileSync(desktopAcceptanceIdentityFile, "utf8")) as Record<string, unknown>;
          response.setHeader("Cache-Control", "no-store");
          response.setHeader("Content-Type", "application/json");
          response.end(JSON.stringify({
            device_id: state.operator_device_id,
            private_key_pkcs8: state.operator_private_key_pkcs8,
            public_key_spki: state.operator_public_key_spki,
          }));
        } catch {
          response.statusCode = 404;
          response.end("acceptance identity unavailable");
        }
      });
    },
  }],
  build: {
    outDir: "../../internal/cloud/ui/react",
    emptyOutDir: true,
  },
  server: {
    fs: { allow: [new URL("../..", import.meta.url).pathname] },
    ...(proxyTarget
      ? {
        proxy: {
          "/v1": {
            target: proxyTarget,
            changeOrigin: true,
            ws: true,
            rewriteWsOrigin: true,
            cookieDomainRewrite: "",
            configure(proxy) {
              const appendSession = (proxyReq: import("node:http").ClientRequest) => {
                const session = devSessionCookie();
                if (!session) return;
                const existing = proxyReq.getHeader("cookie");
                const csrf = "hank-dev-csrf";
                const credentials = `${session}; hank_csrf=${csrf}`;
                proxyReq.setHeader("cookie", existing ? `${existing}; ${credentials}` : credentials);
                if (["POST", "PUT", "PATCH", "DELETE"].includes(proxyReq.method || "")) {
                  proxyReq.setHeader("X-Hank-CSRF-Token", csrf);
                }
              };
              proxy.on("proxyReq", appendSession);
              proxy.on("proxyReqWs", appendSession);
            },
          },
          "/ws": {
            target: proxyTarget,
            changeOrigin: true,
            ws: true,
            rewriteWsOrigin: true,
          },
        },
      }
      : {}),
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/setupTests.ts"],
  },
});
