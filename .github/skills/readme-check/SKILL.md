---
name: readme-check
description: "Check that README.md (and demos/README.md) still match the code after a change, and fix any drift. Use when: finishing a feature, adding/removing/renaming Make targets, endpoints, services, dashboard panels, compose settings, ports, deploy steps or skills; before committing or opening a PR; when asked 'is the README up to date'."
argument-hint: "Optional: what changed (e.g. 'new Make target', 'new inspector endpoint')"
---

# README drift check

Compare each README section against its source of truth and update the README in place. Report what changed.

| README section | Source of truth | How to check |
| --- | --- | --- |
| Make targets | `Makefile` | `make help` — every `##`-documented target appears in the table, no removed targets remain |
| Services | `services/*/`, Dockerfiles | Language/runtime versions match base images (`FROM ...`) |
| Inspector API | `services/inspector-api/server.go` `Routes()` | Every registered route is listed; no stale ones |
| Dashboard | `services/ui-gateway/public/index.html` | Each `<section class="card">` panel is described |
| Gateway endpoints | `services/ui-gateway/server.js` | `/api/gateway/*` paths listed in Services row |
| Concept map | `deploy/kustomize/base/*.yaml`, `demos/README.md` | Every K8s object kind used is mapped to where it's visible |
| Quick start / ports | `docker-compose.yml`, `deploy/kind/kind-config.yaml`, overlays | Ports (3000, 8080–8082, 8000, 8088/30080), replica counts and URLs match |
| Docker Hub / architectures | `Makefile` vars `PLATFORMS`, `DOCKERHUB_*`, `LOCAL_PLATFORM` | Defaults and image naming (`<user>/kubeplay-<svc>`) match |
| Troubleshooting | `.github/skills/deploy-kubeplay/SKILL.md` | Same fixes, no contradictions |
| Development / skills list | `.github/skills/*/` | Every skill folder is named |
| CI/CD | `.github/workflows/*.yml` | Triggers, tags, platforms and required secrets/variables match |
| Layout | top-level folders | Every top-level folder is listed |

## Steps
1. Run `make help` and `git diff --stat` (or inspect the changed files) to see what moved.
2. Walk the table above; read only the sources that changed.
3. Edit `README.md` (and `demos/README.md` if commands or behaviour changed). Keep the existing structure and tone; tables use `| --- |` separators and blank lines around headings/code blocks.
4. Check every relative link in the README points to an existing file.
5. Summarize: sections updated, and anything you could not verify.
