# Sandbox

Spidey can run agent Bash tool calls inside a Docker container instead of on the host. This is a **workspace fence**, not a security boundary against hostile code.

## What it protects you from

- Accidental destructive commands (`rm -rf`, overwriting files outside the project) leaking beyond the bind-mounted working directories.
- Dependency installs polluting the host (`pip install`, `npm install` stay in the container).
- Builds and tests running against a reproducible toolchain regardless of what's on the host.

## What it does *not* protect you from

- Malicious code that breaks out of a shared-kernel container.
- Writes inside a mounted working directory (the model can still `rm -rf` your project — that's what the mount is for).
- Network-level exfiltration. The container has full outbound network access by default.

For stronger isolation you'd need microVMs (Firecracker, Docker Sandboxes) or a user-namespace sandbox (bubblewrap, Seatbelt) — see `JOURNAL.md` for the research that led to this decision.

## Build

```bash
docker compose build sandbox
```

This produces `spidey-sandbox:latest` from `Dockerfile.sandbox` at repo root. Rebuild any time the Dockerfile changes.

## What's inside

Base: `python:3.12-slim-bookworm`. Tools:

| Category   | Tools |
|------------|-------|
| Shell      | `bash`, standard GNU coreutils |
| Network    | `curl`, `wget`, `ca-certificates` |
| VCS        | `git` |
| Search     | `ripgrep`, `fd` (aliased from `fdfind`) |
| JSON       | `jq` |
| Paging     | `less` |
| Diagnostic | `procps` (ps, top) |
| Build      | `build-essential`, `pkg-config` |
| Languages  | `python3`, `nodejs`, `npm` |

Runs as non-root user `agent` (UID 1000). `WORKDIR` is `/workspace`.

## How it's invoked

`service/sandbox/docker.go` calls `docker run --rm -i -v <dir>:<dir> ... spidey-sandbox:latest sh -c <command>` per Bash tool call. Each working directory is bind-mounted at the same path so commands the agent constructs against host paths work unchanged inside.

Preflight: `sandbox.CheckReady()` verifies the daemon is reachable and the image exists. Called at boot (logs status) and before every `Exec`.

## Enable / disable per thread

Threads have a `sandboxed` boolean in the GraphQL `Thread` type. Default is `false` — new threads run tools directly on the host. Flip to `true` via the thread config popover once the image is built.

## Extending

Edit `Dockerfile.sandbox`, add your packages to the `apt-get install` list, rebuild. The image rebuild is cached per layer, so adding a single package is cheap.

If you need Spidey-specific tooling (e.g., a different Python version per project), create a derived image and point `service/sandbox/docker.go`'s `Image` constant at it. Resist per-thread image selection — it multiplies the build/cache surface and was not worth the complexity when weighed.

## Disable the feature entirely

If you don't want Docker as a dependency:

1. Don't build the image. `sandboxed=false` threads are unaffected.
2. Optionally hide the toggle in the UI (`web/src/components/organisms/ThreadConfigPopover.tsx`).

The preflight will log a one-line "not ready" notice at boot that you can ignore.
