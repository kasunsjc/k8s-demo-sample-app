---
description: "Node.js conventions for the ui-gateway service and dashboard"
applyTo: "services/ui-gateway/**"
---
# ui-gateway (Node.js)

- Zero dependencies: only `node:` built-ins on the server, vanilla JS/CSS in `public/`. No build step.
- Proxy targets come only from env (`INSPECTOR_URL`, `NOTES_URL`). Build upstream URLs by setting `pathname`/`search` on a `URL` of the configured base — never concatenate client input into a host (SSRF).
- Static files: keep the traversal check (`filePath.startsWith(PUBLIC_DIR + path.sep)`) and security headers (CSP `default-src 'self'`, so no inline scripts or CDNs).
- Frontend: render data with `textContent`/`el()` — never `innerHTML` with API data. Validate colours with `COLOR_RE` before using them in styles.
- New chaos buttons use `data-method`/`data-url` attributes; the generic handler logs pod and `effect`.
- Tests use `node:test` in `test/`; run `node --test`.
