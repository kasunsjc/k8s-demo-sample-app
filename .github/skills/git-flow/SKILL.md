---
name: git-flow
description: "Make any code change using the Git Flow branching strategy: pick the right branch type (feature, release, hotfix, bugfix), create it from the correct base, commit with conventional messages, and merge/PR back to the right targets. Use when: starting any change, fixing a bug, preparing a release, hotfixing production, creating a branch, committing, opening a PR, or asked 'which branch should I use'."
argument-hint: "Short description of the change, e.g. 'add readiness timeout to notes-api'"
---

# Git Flow for KubePlay

## Branches

| Branch | Created from | Merges into | Purpose |
| --- | --- | --- | --- |
| `main` | — | — | Released code. Merged `release/x.y.z` and `hotfix/x.y.z` PRs publish images and create a GitHub Release (`.github/workflows/dockerhub.yml`). |
| `develop` | `main` (once) | `release/*` | Integration branch for the next release. |
| `feature/<short-name>` | `develop` | `develop` | New features, docs, refactors. |
| `bugfix/<short-name>` | `develop` | `develop` | Non-urgent fixes found before release. |
| `release/<x.y.z>` | `develop` | `main` **and** `develop` | Stabilise and version a release. |
| `hotfix/<x.y.z>` | `main` | `main` **and** `develop` | Urgent fix to released code. |

Never commit directly to `main` or `develop`.

## Steps

1. **Classify the change** → feature, bugfix, release or hotfix (table above). Branch names: lowercase, kebab-case, e.g. `feature/notes-search`.
2. **Sync and branch**
   ```sh
   git fetch origin
   git show-ref --verify --quiet refs/remotes/origin/develop || { git switch -c develop origin/main && git push -u origin develop; }
   git switch -c feature/<name> origin/develop     # hotfix: git switch -c hotfix/<x.y.z> origin/main
   ```
3. **Commit** with Conventional Commits: `feat: …`, `fix: …`, `docs: …`, `chore: …`, `ci: …`, `refactor: …`, `test: …`. Keep commits small and focused.
4. **Verify before pushing**: `make test && make lint`; run the `readme-check` skill if user-facing behaviour, Make targets, endpoints or ports changed.
5. **Push and open a PR** to the target in the table (`feature/*`, `bugfix/*` → `develop`; `release/*`, `hotfix/*` → `main`).
6. **Releases**
   ```sh
   git switch -c release/<x.y.z> origin/develop
   # update chart version and deployment image tags to x.y.z; fix only release blockers
   ```
   Merge to `main`; the workflow tests the merge, publishes SemVer/`latest`/SHA image tags, and creates `v<x.y.z>` as a GitHub Release. Then merge `main` back into `develop`.
7. **Hotfixes**: branch from `main`, fix, merge to `main`; the workflow publishes the versioned images and creates `v<x.y.z>`. Then merge `main` back into `develop`.

## Safety
- Ask before force-pushing, deleting remote branches, or rewriting shared history.
- Use `--no-ff` merges for release/hotfix so the merge is visible in history.
- Delete merged feature/bugfix branches only after the PR is merged.
