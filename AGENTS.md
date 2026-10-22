# Agent Instructions

## Host Execution Model — Bare Metal, and When a VM Is Allowed

The live cluster in this repository runs **directly on the physical Fedora
host**. It is not in a VM, not in Vagrant, not in QEMU/KVM/libvirt, not in
VirtualBox or VMware, and not in a nested/`k3d` container. That is a deliberate
architectural choice with one exception, described below.

Verified on this host: `systemd-detect-virt` → `none`; `amdgpu` is loaded and
drives `card1` at PCI `0000:01:00.0` with DP/DVI connectors attached.

Two reasons, both expensive to rediscover:

1. **Cilium's eBPF datapath needs the host kernel.** The earlier Docker-backed
   `k3d` profile could not run it and fell back to iptables masquerading and
   host-legacy routing (`bb93f8e`), which is exactly why that phase ended.
2. **The GPU belongs to the host, and the host is using it.** The Track C
   device plugin hands pods `/dev/dri/renderD128`, a node bound to the host's
   `amdgpu` driver. A VM with GPU passthrough rebinds that card to `vfio-pci`
   **on the host**, removing it from the host session: no display, no Vulkan,
   and it stays gone until the binding is undone and the machine reboots. On a
   single-GPU desktop this is indistinguishable from losing the card.

**Agents MUST NOT**, for or around this cluster:

- Pass the GPU to a guest, add `vfio-pci` ids to the kernel command line,
  `modprobe`/`blacklist` GPU drivers, `virsh nodedev-detach`, or otherwise unbind
  `amdgpu` from a device. **This one has no exception**: it is the action that
  takes the card off the desktop.
- Run the cluster in a container runtime (Docker/Podman) or re-enable the `k3d`
  profile as the live platform. `config/k3d/` is archived Phase-1 history.

**Exception — a VM is fine when no GPU access is needed.** The constraint above
exists *because of the GPU*, not as a principle. If a task does not touch
`/dev/dri`, the GPU device plugin, Vulkan, or the models' GPU path, then wrapping
the cluster in a VM (Vagrant, QEMU/KVM, a cloud VM) is an acceptable
environment — that is how the platform can be tried on a machine without an AMD
card, and how the GCE/GKE hosting path is developed.

Decide with one question: **does this work require `/dev/dri/renderD128`?**

- **No** → a VM is fine. Do not attach a GPU to it, and do not touch the host's
  driver bindings.
- **Yes, or you are unsure** → bare metal, and ask the user before doing
  anything else.

**Agents MAY** inspect, read-only: `systemd-detect-virt`, `lsmod`, `ls -l
/dev/dri`, `ls /sys/class/drm/`, `kubectl`, and the manifests.

## Public Repository and Commit Safety

- This is a public repository. Never commit credentials, tokens, private keys,
  kubeconfigs, certificates, `.env` files, or other secrets.
- Keep potential secret-bearing local files ignored. If a secret must be managed
  declaratively in the future, use Sealed Secrets (or another reviewed secret
  encryption mechanism) and commit only the encrypted manifest.
- Make atomic commits: each commit must contain one coherent, independently
  reviewable change and include the related `bd` issue identifier in its message.

## Repository Layout

The top level stays intentionally small and its meaning is fixed:

- `/config` — deployable platform configuration. `config/k3d/` is the archived
  Phase-1 profile; `config/k3s/` is the live one.
- `/apps` — executable software (assistant, models, Worm lab, GPU device plugin).
- `/research` and `/evolution` — **tracked** design notes; `evolution/` holds the
  per-phase narrative and commit evidence.
- `/benchmark` — measured experiments with recorded baseline commits.
- `/blog` — long-form write-ups.
- `/scripts` — host-side helpers.
- `/docs` and `/cv` — **gitignored**, machine-specific and personal material.
- `ROADMAP.md`, `README.md`, `Tiltfile*` and `start.sh` at the root.

Add new material to an existing directory above rather than creating a new
top-level tree. Never commit anything from `/docs` or `/cv`.

## Testing

```bash
cd apps/assistant && python3 -m pytest -q     # unit tests, no cluster or model needed
bash config/ci/validate.sh                    # Helm charts + kubeconform for config/ and apps/
```

Do not claim a test run passed unless you actually ran it in this session.

## Argo CD Authentication and Exposure

- Argo CD runs with **anonymous read-only** access: the admin account is
  disabled, `users.anonymous.enabled: "true"`, and `policy.default:
  role:readonly` (`g, system:unauthenticated, role:readonly`). Anonymous
  callers may view Applications, manifests and status trees, but must never be
  able to sync, update, or delete anything.
- **NEVER set `server.disable.auth: "true"`.** That grants every anonymous
  caller the Admin role. It is a hard prohibition, not a preference.
- Argo CD is exposed at a pinned Cilium LoadBalancer IP (`192.168.0.247`).
  Any later public exposure must preserve the read-only role.
- These settings live in `config/argocd/values.yaml` so a Helm reinstall cannot
  silently downgrade them.

## Argo CD Applications — Contextual targetRevision

- Argo CD Applications must **NEVER target `main`**. `main` is frozen Phase-1
  history. An Application reads the branch its work actually lives on —
  currently `cluster-evolution` (the unified branch) or a pinned full commit —
  never a shared default. Argo is contextual.
- When adding an Application, set `targetRevision:` to the current working
  branch. Promoting the existing set from `native-k3s-gpu-models` / `trackc` to
  `cluster-evolution` is a deliberate, reviewed step (see `ROADMAP.md` §4), not
  something an agent flips on its own initiative.

## Tilt Runtime Control

- Agents are forbidden from running `tilt up` or `tilt down`.
- Agents must not trigger, stop, kill, restart, or otherwise control any Tilt
  process or runtime. Only the user operates Tilt.

## Git Worktrees — DO NOT TOUCH

- NEVER create, move, remove, prune, or re-point a git worktree. No exceptions.
- NEVER change a worktree's location. Ever.
- NEVER use a worktree as a workaround for anything (locks, permissions, write
  scope, sandbox limits, "it's blocked").
- Work directly in this repository checkout, on a branch, like a normal human.

This project uses **bd** (beads) for issue tracking. Run `bd onboard` to get started.

## Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work atomically
bd close <id>         # Complete work
bd sync               # Sync with git
```

## Non-Interactive Shell Commands

**ALWAYS use non-interactive flags** with file operations to avoid hanging on confirmation prompts.

Shell commands like `cp`, `mv`, and `rm` may be aliased to include `-i` (interactive) mode on some systems, causing the agent to hang indefinitely waiting for y/n input.

**Use these forms instead:**
```bash
# Force overwrite without prompting
cp -f source dest           # NOT: cp source dest
mv -f source dest           # NOT: mv source dest
rm -f file                  # NOT: rm file

# For recursive operations
rm -rf directory            # NOT: rm -r directory
cp -rf source dest          # NOT: cp -r source dest
```

**Other commands that may prompt:**
- `scp` - use `-o BatchMode=yes` for non-interactive
- `ssh` - use `-o BatchMode=yes` to fail instead of prompting
- `apt-get` - use `-y` flag
- `brew` - use `HOMEBREW_NO_AUTO_UPDATE=1` env var

<!-- BEGIN BEADS INTEGRATION -->
## Issue Tracking with bd (beads)

**IMPORTANT**: This project uses **bd (beads)** for ALL issue tracking. Do NOT use markdown TODOs, task lists, or other tracking methods.

### Why bd?

- Dependency-aware: Track blockers and relationships between issues
- Git-friendly: Auto-syncs to JSONL for version control
- Agent-optimized: JSON output, ready work detection, discovered-from links
- Prevents duplicate tracking systems and confusion

### Quick Start

**Check for ready work:**

```bash
bd ready --json
```

**Create new issues:**

```bash
bd create "Issue title" --description="Detailed context" -t bug|feature|task -p 0-4 --json
bd create "Issue title" --description="What this issue is about" -p 1 --deps discovered-from:bd-123 --json
```

**Claim and update:**

```bash
bd update <id> --claim --json
bd update bd-42 --priority 1 --json
```

**Complete work:**

```bash
bd close bd-42 --reason "Completed" --json
```

### Issue Types

- `bug` - Something broken
- `feature` - New functionality
- `task` - Work item (tests, docs, refactoring)
- `epic` - Large feature with subtasks
- `chore` - Maintenance (dependencies, tooling)

### Priorities

- `0` - Critical (security, data loss, broken builds)
- `1` - High (major features, important bugs)
- `2` - Medium (default, nice-to-have)
- `3` - Low (polish, optimization)
- `4` - Backlog (future ideas)

### Workflow for AI Agents

1. **Check ready work**: `bd ready` shows unblocked issues
2. **Claim your task atomically**: `bd update <id> --claim`
3. **Work on it**: Implement, test, document
4. **Discover new work?** Create linked issue:
   - `bd create "Found bug" --description="Details about what was found" -p 1 --deps discovered-from:<parent-id>`
5. **Complete**: `bd close <id> --reason "Done"`

### Auto-Sync

bd automatically syncs with git:

- Exports to `.beads/issues.jsonl` after changes (5s debounce)
- Imports from JSONL when newer (e.g., after `git pull`)
- No manual export/import needed!

### Important Rules

- ✅ Use bd for ALL task tracking
- ✅ Always use `--json` flag for programmatic use
- ✅ Link discovered work with `discovered-from` dependencies
- ✅ Check `bd ready` before asking "what should I work on?"
- ❌ Do NOT create markdown TODO lists
- ❌ Do NOT use external issue trackers
- ❌ Do NOT duplicate tracking systems

For more details, see README.md and docs/QUICKSTART.md.

<!-- END BEADS INTEGRATION -->

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd sync
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds

<!-- BEGIN BEADS CODEX SETUP: generated by bd setup codex -->
## Beads Issue Tracker

Use Beads (`bd`) for durable task tracking in repositories that include it. Use the `beads` skill at `.agents/skills/beads/SKILL.md` (project install) or `~/.agents/skills/beads/SKILL.md` (global install) for Beads workflow guidance, then use the `bd` CLI for issue operations.

### Quick Reference

```bash
bd ready                # Find available work
bd show <id>            # View issue details
bd update <id> --claim  # Claim work
bd close <id>           # Complete work
bd prime                # Refresh Beads context
```

### Rules

- Use `bd` for all task tracking; do not create markdown TODO lists.
- Run `bd prime` when Beads context is missing or stale. Codex 0.129.0+ can load Beads context automatically through native hooks; use `/hooks` to inspect or toggle them.
- Keep persistent project memory in Beads via `bd remember`; do not create ad hoc memory files.

**Architecture in one line:** issues live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote; `.beads/issues.jsonl` is a passive export. See https://github.com/gastownhall/beads/blob/main/docs/core-concepts/sync-concepts.md for details and anti-patterns.
<!-- END BEADS CODEX SETUP -->
