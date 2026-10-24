---
description: "Go conventions for the inspector-api service"
applyTo: "services/inspector-api/**"
---
# inspector-api (Go)

- Standard library only; `go.mod` must keep zero requirements.
- Register routes in `Server.Routes()` using Go 1.22+ method patterns (`"GET /whoami"`, `"/status/{code}"`). Metrics labels come from `r.Pattern`, so pattern strings are user-visible.
- Respond with `writeJSON`. Include `"pod": s.podName()` or `"servedBy"` in responses.
- Chaos/stress handlers: parse with `queryInt` (bounded), return 400 on bad input, add an `"effect"` string describing what Kubernetes will do.
- Keep probe semantics: `/startupz` (startup delay), `/readyz` (started && ready), `/livez` (live). Shutdown drains by failing readiness before `srv.Shutdown`.
- Mask anything secret-like (`isSensitive` / `mask`).
- Add a table-style test in `server_test.go` using `newTestServer` and `do`; never call `os.Exit` in tests (`s.exit` is stubbed).
- Run `go vet ./... && go test ./...`.
