# Third-party notices

The repository's original-code license does not replace third-party terms.
The following license copies preserve the inspected dependencies' own notices.
This inventory is dated October 8, 2026. It covers the inspected application dependencies; complete container/base-image and release compliance requires its own review.

## Source repository and browser code

The tracked `internal/cloud/ui/mcp/kanban-v1.html` embeds React, MCP SDK/ext-apps,
Zod, Rolldown runtime helpers, and OXC runtime helpers. Its build now embeds the
complete inspected browser-library/font notices as inert JSON in the HTML. The
dashboard additionally uses React DOM, xterm.js and Workbox; its build emits
`assets/THIRD_PARTY_NOTICES.txt`, links it from the HTML, and preserves it in the
PWA cache. Keep that file alongside the browser bundle when redistributing it.

The table conservatively includes installed runtime-related dependency versions;
some server-side SDK modules can be removed from a particular browser bundle by
tree shaking. Package versions and locked tarball URLs identify the inspected
releases; this does not assert every package in the table ships in every bundle.

| Component | Version | Declared license | Source | Preserved text |
| --- | --- | --- | --- | --- |
| `@hono/node-server` | `2.1.0` | MIT | [upstream](https://github.com/honojs/node-server) / [locked release](https://registry.npmjs.org/@hono/node-server/-/node-server-2.1.0.tgz) | [LICENSE](third-party-licenses/npm/__hono__node-server/2.1.0/LICENSE) |
| `@modelcontextprotocol/ext-apps` | `1.7.5` | MIT | [upstream](https://github.com/modelcontextprotocol/ext-apps) / [locked release](https://registry.npmjs.org/@modelcontextprotocol/ext-apps/-/ext-apps-1.7.5.tgz) | [LICENSE](third-party-licenses/npm/__modelcontextprotocol__ext-apps/1.7.5/LICENSE) |
| `@modelcontextprotocol/sdk` | `1.31.0` | MIT | [upstream](https://github.com/modelcontextprotocol/typescript-sdk) / [locked release](https://registry.npmjs.org/@modelcontextprotocol/sdk/-/sdk-1.31.0.tgz) | [LICENSE](third-party-licenses/npm/__modelcontextprotocol__sdk/1.31.0/LICENSE) |
| `@standard-schema/spec` | `1.1.0` | MIT | [upstream](https://github.com/standard-schema/standard-schema) / [locked release](https://registry.npmjs.org/@standard-schema/spec/-/spec-1.1.0.tgz) | [LICENSE](third-party-licenses/npm/__standard-schema__spec/1.1.0/LICENSE) |
| `@xterm/addon-fit` | `0.11.0` | MIT | [upstream](https://github.com/xtermjs/xterm.js/tree/master/addons/addon-fit) / [locked release](https://registry.npmjs.org/@xterm/addon-fit/-/addon-fit-0.11.0.tgz) | [LICENSE](third-party-licenses/npm/__xterm__addon-fit/0.11.0/LICENSE) |
| `@xterm/xterm` | `6.0.0` | MIT | [upstream](https://github.com/xtermjs/xterm.js) / [locked release](https://registry.npmjs.org/@xterm/xterm/-/xterm-6.0.0.tgz) | [LICENSE](third-party-licenses/npm/__xterm__xterm/6.0.0/LICENSE) |
| `accepts` | `2.0.0` | MIT | [upstream](https://github.com/jshttp/accepts) / [locked release](https://registry.npmjs.org/accepts/-/accepts-2.0.0.tgz) | [LICENSE](third-party-licenses/npm/accepts/2.0.0/LICENSE) |
| `ajv` | `8.20.0` | MIT | [upstream](https://github.com/ajv-validator/ajv) / [locked release](https://registry.npmjs.org/ajv/-/ajv-8.20.0.tgz) | [LICENSE](third-party-licenses/npm/ajv/8.20.0/LICENSE) |
| `ajv-formats` | `3.0.1` | MIT | [upstream](https://github.com/ajv-validator/ajv-formats) / [locked release](https://registry.npmjs.org/ajv-formats/-/ajv-formats-3.0.1.tgz) | [LICENSE](third-party-licenses/npm/ajv-formats/3.0.1/LICENSE) |
| `body-parser` | `2.3.0` | MIT | [upstream](https://github.com/expressjs/body-parser) / [locked release](https://registry.npmjs.org/body-parser/-/body-parser-2.3.0.tgz) | [LICENSE](third-party-licenses/npm/body-parser/2.3.0/LICENSE) |
| `bytes` | `3.1.2` | MIT | [upstream](https://github.com/visionmedia/bytes.js) / [locked release](https://registry.npmjs.org/bytes/-/bytes-3.1.2.tgz) | [LICENSE](third-party-licenses/npm/bytes/3.1.2/LICENSE) |
| `call-bind-apply-helpers` | `1.0.2` | MIT | [upstream](https://github.com/ljharb/call-bind-apply-helpers) / [locked release](https://registry.npmjs.org/call-bind-apply-helpers/-/call-bind-apply-helpers-1.0.2.tgz) | [LICENSE](third-party-licenses/npm/call-bind-apply-helpers/1.0.2/LICENSE) |
| `call-bound` | `1.0.4` | MIT | [upstream](https://github.com/ljharb/call-bound) / [locked release](https://registry.npmjs.org/call-bound/-/call-bound-1.0.4.tgz) | [LICENSE](third-party-licenses/npm/call-bound/1.0.4/LICENSE) |
| `content-disposition` | `1.1.0` | MIT | [upstream](https://github.com/jshttp/content-disposition) / [locked release](https://registry.npmjs.org/content-disposition/-/content-disposition-1.1.0.tgz) | [LICENSE](third-party-licenses/npm/content-disposition/1.1.0/LICENSE) |
| `content-type` | `2.0.0` | MIT | [upstream](https://github.com/jshttp/content-type) / [locked release](https://registry.npmjs.org/content-type/-/content-type-2.0.0.tgz) | [LICENSE](third-party-licenses/npm/content-type/2.0.0/LICENSE) |
| `cookie` | `0.7.2` | MIT | [upstream](https://github.com/jshttp/cookie) / [locked release](https://registry.npmjs.org/cookie/-/cookie-0.7.2.tgz) | [LICENSE](third-party-licenses/npm/cookie/0.7.2/LICENSE) |
| `cookie-signature` | `1.2.2` | MIT | [upstream](https://github.com/visionmedia/node-cookie-signature) / [locked release](https://registry.npmjs.org/cookie-signature/-/cookie-signature-1.2.2.tgz) | [LICENSE](third-party-licenses/npm/cookie-signature/1.2.2/LICENSE) |
| `cors` | `2.8.6` | MIT | [upstream](https://github.com/expressjs/cors) / [locked release](https://registry.npmjs.org/cors/-/cors-2.8.6.tgz) | [LICENSE](third-party-licenses/npm/cors/2.8.6/LICENSE) |
| `cross-spawn` | `7.0.6` | MIT | [upstream](https://github.com/moxystudio/node-cross-spawn) / [locked release](https://registry.npmjs.org/cross-spawn/-/cross-spawn-7.0.6.tgz) | [LICENSE](third-party-licenses/npm/cross-spawn/7.0.6/LICENSE) |
| `debug` | `4.4.3` | MIT | [upstream](https://github.com/debug-js/debug) / [locked release](https://registry.npmjs.org/debug/-/debug-4.4.3.tgz) | [LICENSE](third-party-licenses/npm/debug/4.4.3/LICENSE) |
| `depd` | `2.0.0` | MIT | [upstream](https://github.com/dougwilson/nodejs-depd) / [locked release](https://registry.npmjs.org/depd/-/depd-2.0.0.tgz) | [LICENSE](third-party-licenses/npm/depd/2.0.0/LICENSE) |
| `dunder-proto` | `1.0.1` | MIT | [upstream](https://github.com/es-shims/dunder-proto) / [locked release](https://registry.npmjs.org/dunder-proto/-/dunder-proto-1.0.1.tgz) | [LICENSE](third-party-licenses/npm/dunder-proto/1.0.1/LICENSE) |
| `ee-first` | `1.1.1` | MIT | [upstream](https://github.com/jonathanong/ee-first) / [locked release](https://registry.npmjs.org/ee-first/-/ee-first-1.1.1.tgz) | [LICENSE](third-party-licenses/npm/ee-first/1.1.1/LICENSE) |
| `encodeurl` | `2.0.0` | MIT | [upstream](https://github.com/pillarjs/encodeurl) / [locked release](https://registry.npmjs.org/encodeurl/-/encodeurl-2.0.0.tgz) | [LICENSE](third-party-licenses/npm/encodeurl/2.0.0/LICENSE) |
| `es-define-property` | `1.0.1` | MIT | [upstream](https://github.com/ljharb/es-define-property) / [locked release](https://registry.npmjs.org/es-define-property/-/es-define-property-1.0.1.tgz) | [LICENSE](third-party-licenses/npm/es-define-property/1.0.1/LICENSE) |
| `es-errors` | `1.3.0` | MIT | [upstream](https://github.com/ljharb/es-errors) / [locked release](https://registry.npmjs.org/es-errors/-/es-errors-1.3.0.tgz) | [LICENSE](third-party-licenses/npm/es-errors/1.3.0/LICENSE) |
| `es-object-atoms` | `1.1.2` | MIT | [upstream](https://github.com/ljharb/es-object-atoms) / [locked release](https://registry.npmjs.org/es-object-atoms/-/es-object-atoms-1.1.2.tgz) | [LICENSE](third-party-licenses/npm/es-object-atoms/1.1.2/LICENSE) |
| `escape-html` | `1.0.3` | MIT | [upstream](https://github.com/component/escape-html) / [locked release](https://registry.npmjs.org/escape-html/-/escape-html-1.0.3.tgz) | [LICENSE](third-party-licenses/npm/escape-html/1.0.3/LICENSE) |
| `etag` | `1.8.1` | MIT | [upstream](https://github.com/jshttp/etag) / [locked release](https://registry.npmjs.org/etag/-/etag-1.8.1.tgz) | [LICENSE](third-party-licenses/npm/etag/1.8.1/LICENSE) |
| `eventsource` | `3.0.7` | MIT | [upstream](https://github.com/EventSource/eventsource) / [locked release](https://registry.npmjs.org/eventsource/-/eventsource-3.0.7.tgz) | [LICENSE](third-party-licenses/npm/eventsource/3.0.7/LICENSE) |
| `eventsource-parser` | `3.1.1` | MIT | [upstream](https://github.com/rexxars/eventsource-parser) / [locked release](https://registry.npmjs.org/eventsource-parser/-/eventsource-parser-3.1.1.tgz) | [LICENSE](third-party-licenses/npm/eventsource-parser/3.1.1/LICENSE) |
| `express` | `5.2.1` | MIT | [upstream](https://github.com/expressjs/express) / [locked release](https://registry.npmjs.org/express/-/express-5.2.1.tgz) | [LICENSE](third-party-licenses/npm/express/5.2.1/LICENSE) |
| `express-rate-limit` | `8.6.2` | MIT | [upstream](https://github.com/express-rate-limit/express-rate-limit) / [locked release](https://registry.npmjs.org/express-rate-limit/-/express-rate-limit-8.6.2.tgz) | [license.md](third-party-licenses/npm/express-rate-limit/8.6.2/license.md) |
| `fast-deep-equal` | `3.1.3` | MIT | [upstream](https://github.com/epoberezkin/fast-deep-equal) / [locked release](https://registry.npmjs.org/fast-deep-equal/-/fast-deep-equal-3.1.3.tgz) | [LICENSE](third-party-licenses/npm/fast-deep-equal/3.1.3/LICENSE) |
| `fast-uri` | `3.1.8` | BSD-3-Clause | [upstream](https://github.com/fastify/fast-uri) / [locked release](https://registry.npmjs.org/fast-uri/-/fast-uri-3.1.8.tgz) | [LICENSE](third-party-licenses/npm/fast-uri/3.1.8/LICENSE) |
| `finalhandler` | `2.1.1` | MIT | [upstream](https://github.com/pillarjs/finalhandler) / [locked release](https://registry.npmjs.org/finalhandler/-/finalhandler-2.1.1.tgz) | [LICENSE](third-party-licenses/npm/finalhandler/2.1.1/LICENSE) |
| `forwarded` | `0.2.0` | MIT | [upstream](https://github.com/jshttp/forwarded) / [locked release](https://registry.npmjs.org/forwarded/-/forwarded-0.2.0.tgz) | [LICENSE](third-party-licenses/npm/forwarded/0.2.0/LICENSE) |
| `fresh` | `2.0.0` | MIT | [upstream](https://github.com/jshttp/fresh) / [locked release](https://registry.npmjs.org/fresh/-/fresh-2.0.0.tgz) | [LICENSE](third-party-licenses/npm/fresh/2.0.0/LICENSE) |
| `function-bind` | `1.1.2` | MIT | [upstream](https://github.com/Raynos/function-bind) / [locked release](https://registry.npmjs.org/function-bind/-/function-bind-1.1.2.tgz) | [LICENSE](third-party-licenses/npm/function-bind/1.1.2/LICENSE) |
| `get-intrinsic` | `1.3.0` | MIT | [upstream](https://github.com/ljharb/get-intrinsic) / [locked release](https://registry.npmjs.org/get-intrinsic/-/get-intrinsic-1.3.0.tgz) | [LICENSE](third-party-licenses/npm/get-intrinsic/1.3.0/LICENSE) |
| `get-proto` | `1.0.1` | MIT | [upstream](https://github.com/ljharb/get-proto) / [locked release](https://registry.npmjs.org/get-proto/-/get-proto-1.0.1.tgz) | [LICENSE](third-party-licenses/npm/get-proto/1.0.1/LICENSE) |
| `gopd` | `1.2.0` | MIT | [upstream](https://github.com/ljharb/gopd) / [locked release](https://registry.npmjs.org/gopd/-/gopd-1.2.0.tgz) | [LICENSE](third-party-licenses/npm/gopd/1.2.0/LICENSE) |
| `has-symbols` | `1.1.0` | MIT | [upstream](https://github.com/inspect-js/has-symbols) / [locked release](https://registry.npmjs.org/has-symbols/-/has-symbols-1.1.0.tgz) | [LICENSE](third-party-licenses/npm/has-symbols/1.1.0/LICENSE) |
| `hasown` | `2.0.4` | MIT | [upstream](https://github.com/inspect-js/hasOwn) / [locked release](https://registry.npmjs.org/hasown/-/hasown-2.0.4.tgz) | [LICENSE](third-party-licenses/npm/hasown/2.0.4/LICENSE) |
| `hono` | `4.13.13` | MIT | [upstream](https://github.com/honojs/hono) / [locked release](https://registry.npmjs.org/hono/-/hono-4.13.13.tgz) | [LICENSE](third-party-licenses/npm/hono/4.13.13/LICENSE) |
| `http-errors` | `2.0.1` | MIT | [upstream](https://github.com/jshttp/http-errors) / [locked release](https://registry.npmjs.org/http-errors/-/http-errors-2.0.1.tgz) | [LICENSE](third-party-licenses/npm/http-errors/2.0.1/LICENSE) |
| `iconv-lite` | `0.7.3` | MIT | [upstream](https://github.com/pillarjs/iconv-lite) / [locked release](https://registry.npmjs.org/iconv-lite/-/iconv-lite-0.7.3.tgz) | [LICENSE](third-party-licenses/npm/iconv-lite/0.7.3/LICENSE) |
| `inherits` | `2.0.4` | ISC | [upstream](https://github.com/isaacs/inherits) / [locked release](https://registry.npmjs.org/inherits/-/inherits-2.0.4.tgz) | [LICENSE](third-party-licenses/npm/inherits/2.0.4/LICENSE) |
| `ip-address` | `10.7.3` | MIT | [upstream](https://github.com/beaugunderson/ip-address) / [locked release](https://registry.npmjs.org/ip-address/-/ip-address-10.7.3.tgz) | [LICENSE](third-party-licenses/npm/ip-address/10.7.3/LICENSE) |
| `ipaddr.js` | `1.9.1` | MIT | [upstream](https://github.com/whitequark/ipaddr.js) / [locked release](https://registry.npmjs.org/ipaddr.js/-/ipaddr.js-1.9.1.tgz) | [LICENSE](third-party-licenses/npm/ipaddr.js/1.9.1/LICENSE) |
| `is-promise` | `4.0.0` | MIT | [upstream](https://github.com/then/is-promise) / [locked release](https://registry.npmjs.org/is-promise/-/is-promise-4.0.0.tgz) | [LICENSE](third-party-licenses/npm/is-promise/4.0.0/LICENSE) |
| `isexe` | `2.0.0` | ISC | [upstream](https://github.com/isaacs/isexe) / [locked release](https://registry.npmjs.org/isexe/-/isexe-2.0.0.tgz) | [LICENSE](third-party-licenses/npm/isexe/2.0.0/LICENSE) |
| `jose` | `6.2.8` | MIT | [upstream](https://github.com/panva/jose) / [locked release](https://registry.npmjs.org/jose/-/jose-6.2.8.tgz) | [LICENSE.md](third-party-licenses/npm/jose/6.2.8/LICENSE.md) |
| `json-schema-traverse` | `1.0.0` | MIT | [upstream](https://github.com/epoberezkin/json-schema-traverse) / [locked release](https://registry.npmjs.org/json-schema-traverse/-/json-schema-traverse-1.0.0.tgz) | [LICENSE](third-party-licenses/npm/json-schema-traverse/1.0.0/LICENSE) |
| `json-schema-typed` | `8.0.2` | BSD-2-Clause | [upstream](https://github.com/RemyRylan/json-schema-typed) / [locked release](https://registry.npmjs.org/json-schema-typed/-/json-schema-typed-8.0.2.tgz) | [LICENSE.md](third-party-licenses/npm/json-schema-typed/8.0.2/LICENSE.md) |
| `math-intrinsics` | `1.1.0` | MIT | [upstream](https://github.com/es-shims/math-intrinsics) / [locked release](https://registry.npmjs.org/math-intrinsics/-/math-intrinsics-1.1.0.tgz) | [LICENSE](third-party-licenses/npm/math-intrinsics/1.1.0/LICENSE) |
| `media-typer` | `1.1.1` | MIT | [upstream](https://github.com/jshttp/media-typer) / [locked release](https://registry.npmjs.org/media-typer/-/media-typer-1.1.1.tgz) | [LICENSE](third-party-licenses/npm/media-typer/1.1.1/LICENSE) |
| `merge-descriptors` | `2.0.0` | MIT | [upstream](https://github.com/sindresorhus/merge-descriptors) / [locked release](https://registry.npmjs.org/merge-descriptors/-/merge-descriptors-2.0.0.tgz) | [license](third-party-licenses/npm/merge-descriptors/2.0.0/license) |
| `mime-db` | `1.54.0` | MIT | [upstream](https://github.com/jshttp/mime-db) / [locked release](https://registry.npmjs.org/mime-db/-/mime-db-1.54.0.tgz) | [LICENSE](third-party-licenses/npm/mime-db/1.54.0/LICENSE) |
| `mime-types` | `3.0.2` | MIT | [upstream](https://github.com/jshttp/mime-types) / [locked release](https://registry.npmjs.org/mime-types/-/mime-types-3.0.2.tgz) | [LICENSE](third-party-licenses/npm/mime-types/3.0.2/LICENSE) |
| `ms` | `2.1.3` | MIT | [upstream](https://github.com/vercel/ms) / [locked release](https://registry.npmjs.org/ms/-/ms-2.1.3.tgz) | [license.md](third-party-licenses/npm/ms/2.1.3/license.md) |
| `negotiator` | `1.0.0` | MIT | [upstream](https://github.com/jshttp/negotiator) / [locked release](https://registry.npmjs.org/negotiator/-/negotiator-1.0.0.tgz) | [LICENSE](third-party-licenses/npm/negotiator/1.0.0/LICENSE) |
| `object-assign` | `4.1.1` | MIT | [upstream](https://github.com/sindresorhus/object-assign) / [locked release](https://registry.npmjs.org/object-assign/-/object-assign-4.1.1.tgz) | [license](third-party-licenses/npm/object-assign/4.1.1/license) |
| `object-inspect` | `1.13.4` | MIT | [upstream](https://github.com/inspect-js/object-inspect) / [locked release](https://registry.npmjs.org/object-inspect/-/object-inspect-1.13.4.tgz) | [LICENSE](third-party-licenses/npm/object-inspect/1.13.4/LICENSE) |
| `on-finished` | `2.4.1` | MIT | [upstream](https://github.com/jshttp/on-finished) / [locked release](https://registry.npmjs.org/on-finished/-/on-finished-2.4.1.tgz) | [LICENSE](third-party-licenses/npm/on-finished/2.4.1/LICENSE) |
| `once` | `1.4.0` | ISC | [upstream](https://github.com/isaacs/once) / [locked release](https://registry.npmjs.org/once/-/once-1.4.0.tgz) | [LICENSE](third-party-licenses/npm/once/1.4.0/LICENSE) |
| `parseurl` | `1.3.3` | MIT | [upstream](https://github.com/pillarjs/parseurl) / [locked release](https://registry.npmjs.org/parseurl/-/parseurl-1.3.3.tgz) | [LICENSE](third-party-licenses/npm/parseurl/1.3.3/LICENSE) |
| `path-key` | `3.1.1` | MIT | [upstream](https://github.com/sindresorhus/path-key) / [locked release](https://registry.npmjs.org/path-key/-/path-key-3.1.1.tgz) | [license](third-party-licenses/npm/path-key/3.1.1/license) |
| `path-to-regexp` | `8.4.2` | MIT | [upstream](https://github.com/pillarjs/path-to-regexp) / [locked release](https://registry.npmjs.org/path-to-regexp/-/path-to-regexp-8.4.2.tgz) | [LICENSE](third-party-licenses/npm/path-to-regexp/8.4.2/LICENSE) |
| `pkce-challenge` | `5.0.1` | MIT | [upstream](https://github.com/crouchcd/pkce-challenge) / [locked release](https://registry.npmjs.org/pkce-challenge/-/pkce-challenge-5.0.1.tgz) | [LICENSE](third-party-licenses/npm/pkce-challenge/5.0.1/LICENSE) |
| `proxy-addr` | `2.0.8` | MIT | [upstream](https://github.com/jshttp/proxy-addr) / [locked release](https://registry.npmjs.org/proxy-addr/-/proxy-addr-2.0.8.tgz) | [LICENSE](third-party-licenses/npm/proxy-addr/2.0.8/LICENSE) |
| `qs` | `6.16.0` | BSD-3-Clause | [upstream](https://github.com/ljharb/qs) / [locked release](https://registry.npmjs.org/qs/-/qs-6.16.0.tgz) | [LICENSE.md](third-party-licenses/npm/qs/6.16.0/LICENSE.md) |
| `range-parser` | `1.3.0` | MIT | [upstream](https://github.com/jshttp/range-parser) / [locked release](https://registry.npmjs.org/range-parser/-/range-parser-1.3.0.tgz) | [LICENSE](third-party-licenses/npm/range-parser/1.3.0/LICENSE) |
| `raw-body` | `3.0.2` | MIT | [upstream](https://github.com/stream-utils/raw-body) / [locked release](https://registry.npmjs.org/raw-body/-/raw-body-3.0.2.tgz) | [LICENSE](third-party-licenses/npm/raw-body/3.0.2/LICENSE) |
| `react` | `19.2.7` | MIT | [upstream](https://github.com/facebook/react) / [locked release](https://registry.npmjs.org/react/-/react-19.2.7.tgz) | [LICENSE](third-party-licenses/npm/react/19.2.7/LICENSE) |
| `react-dom` | `19.2.7` | MIT | [upstream](https://github.com/facebook/react) / [locked release](https://registry.npmjs.org/react-dom/-/react-dom-19.2.7.tgz) | [LICENSE](third-party-licenses/npm/react-dom/19.2.7/LICENSE) |
| `require-from-string` | `2.0.2` | MIT | [upstream](https://github.com/floatdrop/require-from-string) / [locked release](https://registry.npmjs.org/require-from-string/-/require-from-string-2.0.2.tgz) | [license](third-party-licenses/npm/require-from-string/2.0.2/license) |
| `rolldown` | `1.1.3` | MIT | [upstream](https://github.com/rolldown/rolldown) / [locked release](https://registry.npmjs.org/rolldown/-/rolldown-1.1.3.tgz) | [LICENSE](third-party-licenses/npm/rolldown/1.1.3/LICENSE) |
| `router` | `2.2.0` | MIT | [upstream](https://github.com/pillarjs/router) / [locked release](https://registry.npmjs.org/router/-/router-2.2.0.tgz) | [LICENSE](third-party-licenses/npm/router/2.2.0/LICENSE) |
| `safer-buffer` | `2.1.2` | MIT | [upstream](https://github.com/ChALkeR/safer-buffer) / [locked release](https://registry.npmjs.org/safer-buffer/-/safer-buffer-2.1.2.tgz) | [LICENSE](third-party-licenses/npm/safer-buffer/2.1.2/LICENSE) |
| `scheduler` | `0.27.0` | MIT | [upstream](https://github.com/facebook/react) / [locked release](https://registry.npmjs.org/scheduler/-/scheduler-0.27.0.tgz) | [LICENSE](third-party-licenses/npm/scheduler/0.27.0/LICENSE) |
| `send` | `1.2.1` | MIT | [upstream](https://github.com/pillarjs/send) / [locked release](https://registry.npmjs.org/send/-/send-1.2.1.tgz) | [LICENSE](third-party-licenses/npm/send/1.2.1/LICENSE) |
| `serve-static` | `2.2.1` | MIT | [upstream](https://github.com/expressjs/serve-static) / [locked release](https://registry.npmjs.org/serve-static/-/serve-static-2.2.1.tgz) | [LICENSE](third-party-licenses/npm/serve-static/2.2.1/LICENSE) |
| `setprototypeof` | `1.2.0` | ISC | [upstream](https://github.com/wesleytodd/setprototypeof) / [locked release](https://registry.npmjs.org/setprototypeof/-/setprototypeof-1.2.0.tgz) | [LICENSE](third-party-licenses/npm/setprototypeof/1.2.0/LICENSE) |
| `shebang-command` | `2.0.0` | MIT | [upstream](https://github.com/kevva/shebang-command) / [locked release](https://registry.npmjs.org/shebang-command/-/shebang-command-2.0.0.tgz) | [license](third-party-licenses/npm/shebang-command/2.0.0/license) |
| `shebang-regex` | `3.0.0` | MIT | [upstream](https://github.com/sindresorhus/shebang-regex) / [locked release](https://registry.npmjs.org/shebang-regex/-/shebang-regex-3.0.0.tgz) | [license](third-party-licenses/npm/shebang-regex/3.0.0/license) |
| `side-channel` | `1.1.1` | MIT | [upstream](https://github.com/ljharb/side-channel) / [locked release](https://registry.npmjs.org/side-channel/-/side-channel-1.1.1.tgz) | [LICENSE](third-party-licenses/npm/side-channel/1.1.1/LICENSE) |
| `side-channel-list` | `1.0.1` | MIT | [upstream](https://github.com/ljharb/side-channel-list) / [locked release](https://registry.npmjs.org/side-channel-list/-/side-channel-list-1.0.1.tgz) | [LICENSE](third-party-licenses/npm/side-channel-list/1.0.1/LICENSE) |
| `side-channel-map` | `1.0.1` | MIT | [upstream](https://github.com/ljharb/side-channel-map) / [locked release](https://registry.npmjs.org/side-channel-map/-/side-channel-map-1.0.1.tgz) | [LICENSE](third-party-licenses/npm/side-channel-map/1.0.1/LICENSE) |
| `side-channel-weakmap` | `1.0.2` | MIT | [upstream](https://github.com/ljharb/side-channel-weakmap) / [locked release](https://registry.npmjs.org/side-channel-weakmap/-/side-channel-weakmap-1.0.2.tgz) | [LICENSE](third-party-licenses/npm/side-channel-weakmap/1.0.2/LICENSE) |
| `statuses` | `2.0.2` | MIT | [upstream](https://github.com/jshttp/statuses) / [locked release](https://registry.npmjs.org/statuses/-/statuses-2.0.2.tgz) | [LICENSE](third-party-licenses/npm/statuses/2.0.2/LICENSE) |
| `toidentifier` | `1.0.1` | MIT | [upstream](https://github.com/component/toidentifier) / [locked release](https://registry.npmjs.org/toidentifier/-/toidentifier-1.0.1.tgz) | [LICENSE](third-party-licenses/npm/toidentifier/1.0.1/LICENSE) |
| `type-is` | `2.1.0` | MIT | [upstream](https://github.com/jshttp/type-is) / [locked release](https://registry.npmjs.org/type-is/-/type-is-2.1.0.tgz) | [LICENSE](third-party-licenses/npm/type-is/2.1.0/LICENSE) |
| `unpipe` | `1.0.0` | MIT | [upstream](https://github.com/stream-utils/unpipe) / [locked release](https://registry.npmjs.org/unpipe/-/unpipe-1.0.0.tgz) | [LICENSE](third-party-licenses/npm/unpipe/1.0.0/LICENSE) |
| `vary` | `1.1.2` | MIT | [upstream](https://github.com/jshttp/vary) / [locked release](https://registry.npmjs.org/vary/-/vary-1.1.2.tgz) | [LICENSE](third-party-licenses/npm/vary/1.1.2/LICENSE) |
| `which` | `2.0.2` | ISC | [upstream](https://github.com/isaacs/node-which) / [locked release](https://registry.npmjs.org/which/-/which-2.0.2.tgz) | [LICENSE](third-party-licenses/npm/which/2.0.2/LICENSE) |
| `workbox-core` | `7.4.1` | MIT | [upstream](https://github.com/googlechrome/workbox) / [locked release](https://registry.npmjs.org/workbox-core/-/workbox-core-7.4.1.tgz) | [LICENSE](third-party-licenses/npm/workbox-core/7.4.1/LICENSE) |
| `workbox-precaching` | `7.4.1` | MIT | [upstream](https://github.com/googlechrome/workbox) / [locked release](https://registry.npmjs.org/workbox-precaching/-/workbox-precaching-7.4.1.tgz) | [LICENSE](third-party-licenses/npm/workbox-precaching/7.4.1/LICENSE) |
| `workbox-routing` | `7.4.1` | MIT | [upstream](https://github.com/googlechrome/workbox) / [locked release](https://registry.npmjs.org/workbox-routing/-/workbox-routing-7.4.1.tgz) | [LICENSE](third-party-licenses/npm/workbox-routing/7.4.1/LICENSE) |
| `workbox-strategies` | `7.4.1` | MIT | [upstream](https://github.com/googlechrome/workbox) / [locked release](https://registry.npmjs.org/workbox-strategies/-/workbox-strategies-7.4.1.tgz) | [LICENSE](third-party-licenses/npm/workbox-strategies/7.4.1/LICENSE) |
| `wrappy` | `1.0.2` | ISC | [upstream](https://github.com/npm/wrappy) / [locked release](https://registry.npmjs.org/wrappy/-/wrappy-1.0.2.tgz) | [LICENSE](third-party-licenses/npm/wrappy/1.0.2/LICENSE) |
| `zod` | `4.4.3` | MIT | [upstream](https://github.com/colinhacks/zod) / [locked release](https://registry.npmjs.org/zod/-/zod-4.4.3.tgz) | [LICENSE](third-party-licenses/npm/zod/4.4.3/LICENSE) |
| `zod-to-json-schema` | `3.25.2` | ISC | [upstream](https://github.com/StefanTerdell/zod-to-json-schema) / [locked release](https://registry.npmjs.org/zod-to-json-schema/-/zod-to-json-schema-3.25.2.tgz) | [LICENSE](third-party-licenses/npm/zod-to-json-schema/3.25.2/LICENSE) |
| `@oxc-project/runtime` | `0.137.0` | MIT | [exact release](https://registry.npmjs.org/@oxc-project/runtime/-/runtime-0.137.0.tgz) / [upstream](https://github.com/oxc-project/oxc/tree/main/npm/runtime) | [LICENSE](third-party-licenses/npm/__oxc-project__runtime/0.137.0/LICENSE) |

Bundler-injected `@oxc-project/runtime` 0.137.0 was identified from the tracked
asset's module markers and verified against the publisher's exact npm tarball
integrity. It is separate from the project's lockfile package list.

## Go dependencies and binary distribution

The following modules occur in the Linux server/agent/database-operations
command import graph. Modules remain external downloads rather than copied
source in this repository. Carry their notices into any distributed binary,
container, installer or other package; listing them here does not arrange that
release packaging automatically.

| Module | Version | Inspected terms | Exact source | Preserved text |
| --- | --- | --- | --- | --- |
| `filippo.io/age` | `v1.3.2` | BSD-3-Clause | [source](https://github.com/FiloSottile/age/tree/v1.3.2) | [LICENSE](third-party-licenses/go/filippo.io__age/v1.3.2/LICENSE) |
| `filippo.io/hpke` | `v0.4.0` | BSD-3-Clause | [source](https://github.com/FiloSottile/hpke/tree/v0.4.0) | [LICENSE](third-party-licenses/go/filippo.io__hpke/v0.4.0/LICENSE) |
| `github.com/SherClockHolmes/webpush-go` | `v1.4.0` | MIT | [source](https://github.com/SherClockHolmes/webpush-go/tree/v1.4.0) | [LICENSE](third-party-licenses/go/github.com__SherClockHolmes__webpush-go/v1.4.0/LICENSE) |
| `github.com/cloudsoda/go-smb2` | `v0.0.0-20260609183447-7b96c35f5f4b` with [bounded local patch](third_party/go-smb2/HANK-FORK.md) | BSD-2-Clause | [source](https://github.com/cloudsoda/go-smb2/tree/7b96c35f5f4b), [preserved fork](third_party/go-smb2) | [LICENSE](third-party-licenses/go/github.com__cloudsoda__go-smb2/v0.0.0-20260609183447-7b96c35f5f4b/LICENSE) |
| `github.com/coder/websocket` | `v1.8.14` | ISC | [source](https://github.com/coder/websocket/tree/v1.8.14) | [LICENSE.txt](third-party-licenses/go/github.com__coder__websocket/v1.8.14/LICENSE.txt) |
| `github.com/coreos/go-oidc/v3` | `v3.17.0` | Apache-2.0 | [source](https://github.com/coreos/go-oidc/tree/v3.17.0) | [LICENSE](third-party-licenses/go/github.com__coreos__go-oidc__v3/v3.17.0/LICENSE), [NOTICE](third-party-licenses/go/github.com__coreos__go-oidc__v3/v3.17.0/NOTICE) |
| `github.com/creack/pty` | `v1.1.24` | MIT | [source](https://github.com/creack/pty/tree/v1.1.24) | [LICENSE](third-party-licenses/go/github.com__creack__pty/v1.1.24/LICENSE) |
| `github.com/geoffgarside/ber` | `v1.1.0` | BSD-3-Clause | [source](https://github.com/geoffgarside/ber/tree/v1.1.0) | [LICENSE](third-party-licenses/go/github.com__geoffgarside__ber/v1.1.0/LICENSE) |
| `github.com/go-jose/go-jose/v4` | `v4.1.4` | Apache-2.0 | [source](https://github.com/go-jose/go-jose/tree/v4.1.4) | [LICENSE](third-party-licenses/go/github.com__go-jose__go-jose__v4/v4.1.4/LICENSE) |
| `github.com/golang-jwt/jwt/v5` | `v5.3.1` | MIT | [source](https://github.com/golang-jwt/jwt/tree/v5.3.1) | [LICENSE](third-party-licenses/go/github.com__golang-jwt__jwt__v5/v5.3.1/LICENSE) |
| `github.com/google/jsonschema-go` | `v0.4.3` | MIT | [source](https://github.com/google/jsonschema-go/tree/v0.4.3) | [LICENSE](third-party-licenses/go/github.com__google__jsonschema-go/v0.4.3/LICENSE) |
| `github.com/hashicorp/go-uuid` | `v1.0.3` | MPL-2.0 | [source](https://github.com/hashicorp/go-uuid/tree/v1.0.3) | [LICENSE](third-party-licenses/go/github.com__hashicorp__go-uuid/v1.0.3/LICENSE) |
| `github.com/jackc/pgpassfile` | `v1.0.0` | MIT | [source](https://github.com/jackc/pgpassfile/tree/v1.0.0) | [LICENSE](third-party-licenses/go/github.com__jackc__pgpassfile/v1.0.0/LICENSE) |
| `github.com/jackc/pgservicefile` | `v0.0.0-20240606120523-5a60cdf6a761` | MIT | [source](https://github.com/jackc/pgservicefile/tree/5a60cdf6a761) | [LICENSE](third-party-licenses/go/github.com__jackc__pgservicefile/v0.0.0-20240606120523-5a60cdf6a761/LICENSE) |
| `github.com/jackc/pgx/v5` | `v5.9.2` | MIT | [source](https://github.com/jackc/pgx/tree/v5.9.2) | [LICENSE](third-party-licenses/go/github.com__jackc__pgx__v5/v5.9.2/LICENSE) |
| `github.com/jackc/puddle/v2` | `v2.2.2` | MIT | [source](https://github.com/jackc/puddle/tree/v2.2.2) | [LICENSE](third-party-licenses/go/github.com__jackc__puddle__v2/v2.2.2/LICENSE) |
| `github.com/jcmturner/aescts/v2` | `v2.0.0` | Apache-2.0 | [source](https://github.com/jcmturner/aescts/tree/v2.0.0) | [LICENSE](third-party-licenses/go/github.com__jcmturner__aescts__v2/v2.0.0/LICENSE) |
| `github.com/jcmturner/dnsutils/v2` | `v2.0.0` | Apache-2.0 | [source](https://github.com/jcmturner/dnsutils/tree/v2.0.0) | [LICENSE](third-party-licenses/go/github.com__jcmturner__dnsutils__v2/v2.0.0/LICENSE) |
| `github.com/jcmturner/gofork` | `v1.7.6` | BSD-3-Clause | [source](https://github.com/jcmturner/gofork/tree/v1.7.6) | [LICENSE](third-party-licenses/go/github.com__jcmturner__gofork/v1.7.6/LICENSE) |
| `github.com/jcmturner/goidentity/v6` | `v6.0.1` | Apache-2.0 | [source](https://github.com/jcmturner/goidentity/tree/v6.0.1) | [LICENSE](third-party-licenses/go/github.com__jcmturner__goidentity__v6/v6.0.1/LICENSE) |
| `github.com/jcmturner/gokrb5/v8` | `v8.4.4` | Apache-2.0 | [source](https://github.com/jcmturner/gokrb5/tree/v8.4.4) | [LICENSE](third-party-licenses/go/github.com__jcmturner__gokrb5__v8/v8.4.4/LICENSE) |
| `github.com/jcmturner/rpc/v2` | `v2.0.3` | Apache-2.0 | [source](https://github.com/jcmturner/rpc/tree/v2.0.3) | [LICENSE](third-party-licenses/go/github.com__jcmturner__rpc__v2/v2.0.3/LICENSE) |
| `github.com/modelcontextprotocol/go-sdk` | `v1.7.0` | Apache-2.0 | [source](https://github.com/modelcontextprotocol/go-sdk/tree/v1.7.0) | [LICENSE](third-party-licenses/go/github.com__modelcontextprotocol__go-sdk/v1.7.0/LICENSE) |
| `github.com/segmentio/asm` | `v1.1.3` | MIT | [source](https://github.com/segmentio/asm/tree/v1.1.3) | [LICENSE](third-party-licenses/go/github.com__segmentio__asm/v1.1.3/LICENSE) |
| `github.com/segmentio/encoding` | `v0.5.4` | MIT | [source](https://github.com/segmentio/encoding/tree/v0.5.4) | [LICENSE](third-party-licenses/go/github.com__segmentio__encoding/v0.5.4/LICENSE) |
| `github.com/yosida95/uritemplate/v3` | `v3.0.2` | BSD-3-Clause | [source](https://github.com/yosida95/uritemplate/tree/v3.0.2) | [LICENSE](third-party-licenses/go/github.com__yosida95__uritemplate__v3/v3.0.2/LICENSE) |
| `golang.org/x/crypto` | `v0.56.0` | BSD-3-Clause | [source](https://github.com/golang/crypto/tree/v0.56.0) | [LICENSE](third-party-licenses/go/golang.org__x__crypto/v0.56.0/LICENSE) |
| `golang.org/x/image` | `v0.45.0` | BSD-3-Clause | [source](https://github.com/golang/image/tree/v0.45.0) | [LICENSE](third-party-licenses/go/golang.org__x__image/v0.45.0/LICENSE) |
| `golang.org/x/net` | `v0.57.0` | BSD-3-Clause | [source](https://github.com/golang/net/tree/v0.57.0) | [LICENSE](third-party-licenses/go/golang.org__x__net/v0.57.0/LICENSE) |
| `golang.org/x/oauth2` | `v0.36.0` | BSD-3-Clause | [source](https://github.com/golang/oauth2/tree/v0.36.0) | [LICENSE](third-party-licenses/go/golang.org__x__oauth2/v0.36.0/LICENSE) |
| `golang.org/x/sync` | `v0.22.0` | BSD-3-Clause | [source](https://github.com/golang/sync/tree/v0.22.0) | [LICENSE](third-party-licenses/go/golang.org__x__sync/v0.22.0/LICENSE) |
| `golang.org/x/sys` | `v0.47.0` | BSD-3-Clause | [source](https://github.com/golang/sys/tree/v0.47.0) | [LICENSE](third-party-licenses/go/golang.org__x__sys/v0.47.0/LICENSE) |
| `golang.org/x/text` | `v0.41.0` | BSD-3-Clause | [source](https://github.com/golang/text/tree/v0.41.0) | [LICENSE](third-party-licenses/go/golang.org__x__text/v0.41.0/LICENSE) |
| `golang.org/x/time` | `v0.15.0` | BSD-3-Clause | [source](https://github.com/golang/time/tree/v0.15.0) | [LICENSE](third-party-licenses/go/golang.org__x__time/v0.15.0/LICENSE) |

The Go toolchain/runtime license is preserved in
[Go-go1.26.8-LICENSE](third-party-licenses/go/Go-go1.26.8-LICENSE).

### SMB dependency scope and MPL source access

The BSD-licensed SMB dependency is preserved as [local source](third_party/go-smb2/HANK-FORK.md)
at the exact upstream pin with a reproducible bounded patch. Its unused parsed
security-descriptor and enriched-directory APIs are removed; raw operations,
authentication, transport, path handling and ordinary file APIs retain their
upstream implementations. No Hank reader uses the removed APIs. Production
imports and binaries therefore exclude `cloudsoda/sddl`; its conflicting LGPL
LICENSE/MIT README metadata is no longer a combined-binary release dependency.
The local fork keeps BSD terms, while separate original Hank code remains rights
reserved. Refresh the patch and security validation when updating upstream.

`github.com/hashicorp/go-uuid` 1.0.3 is MPL-2.0. Its exact unmodified covered
[source and license](third-party-source/go/github.com__hashicorp__go-uuid/v1.0.3)
are included in this repository and distributions. Binary recipients can find
that source in `third-party-source/go/github.com__hashicorp__go-uuid/v1.0.3/`
or [upstream](https://github.com/hashicorp/go-uuid/tree/v1.0.3). Modified covered
files must retain MPL terms. Separate original files keep their own terms;
recipients' MPL rights are unaffected.

### Distribution packaging

`make distribution` produces the three Go binaries in `dist/` together with
`LICENSE`, this inventory, all preserved dependency/font notices, an explicit
`THIRD_PARTY_SOURCE.txt` source-access notice, and the exact MPL-covered source.
Both Go application Dockerfiles copy the same notice/source material to `/app/`;
the server image's code-reference snapshot also includes its local SMB source
replacement. Individual binaries built with a direct `go build` still need
those accompanying materials before distribution. This packaging does not clear
all OS packages, optional tools, or future upstream releases.

## Build tools, data and fonts

`lightningcss` 1.32.0 is an MPL-2.0 CSS compiler, and `caniuse-lite`
1.0.30001809 contains CC-BY-4.0 data. Their inspected texts are preserved under
`third-party-licenses/tooling/`. They are build-tool/data dependencies; ordinary
transformed CSS output is not, by itself, a copy of the compiler's covered
source. Redistributing the tools or data has its own MPL/source-notice or
CC-BY attribution requirements. Do not assume these files clear a complete
build-tool image or all target-specific optional binaries.

The dashboard requests IBM Plex Sans/Mono from Google Fonts; no font binaries
are checked in. [IBM's SIL Open Font License text](third-party-licenses/fonts/IBM-Plex-OFL.txt) preserves the
upstream font terms. Bundled font software must retain its OFL copyright and
license; screenshots or documents merely rendered with those fonts are not
relicensed under OFL.

## Scope and remaining checks

This is a bounded inventory from selected imports, the lockfile, installed
licenses, and inspected bundled-code markers. It is not a proof of historical
code or artwork authorship, complete container contents, every file-specific
upstream license, or compliance of a future release. Hank icon assets have
no explicit creator metadata in this repository; no copied third-party art was
identified. New portfolio screenshots have their own capture provenance.
