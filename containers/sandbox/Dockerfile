# spidey-sandbox:latest — execution environment for agent tool calls.
#
# Threads with sandboxed=true run every Bash tool call inside this image
# instead of on the host. The host project directory is bind-mounted to
# /workspace at runtime; nothing the model writes escapes the container
# filesystem except through that mount.
#
# Build: docker compose build sandbox
#        (or `docker build -t spidey-sandbox:latest containers/sandbox`)
#
# This is not a security boundary against hostile code — it's a workspace
# fence. Shared kernel, standard container isolation. See SANDBOX.md.

FROM python:3.12-slim-bookworm

ENV DEBIAN_FRONTEND=noninteractive \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

# One apt-get layer: the tools the agent is likely to reach for.
# - shell + core GNU utils are in the base; we add what's missing.
# - ripgrep / fd-find: modern search tools many agents prefer over find/grep.
# - build-essential + pkg-config: any `pip install` that compiles C.
# - nodejs + npm: a lot of tooling assumes node is present.
# - git + ca-certificates: clone, fetch, push.
# - curl + wget + jq: HTTP + JSON in shell.
# - less: paged output when the model reads long files.
# - procps: `ps`, `top` — diagnostic.
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        bash \
        ca-certificates \
        curl \
        wget \
        git \
        jq \
        ripgrep \
        fd-find \
        less \
        procps \
        build-essential \
        pkg-config \
        nodejs \
        npm && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# Debian ships `fd-find` as the binary `fdfind` to avoid a name clash.
# Add an `fd` alias so agents that call `fd` directly still work.
RUN ln -s /usr/bin/fdfind /usr/local/bin/fd

# Non-root user. UID 1000 matches most Linux desktops, which minimises
# bind-mount permission friction on Linux hosts. On Windows/macOS the
# Docker VFS translation handles ownership so the UID choice is moot.
RUN groupadd --system --gid 1000 agent && \
    useradd  --system --uid 1000 --gid 1000 \
             --home-dir /home/agent --create-home \
             --shell /bin/bash agent

WORKDIR /workspace
USER agent

# Default to bash — the Go sandbox layer always passes `sh -c <command>`
# explicitly, so CMD exists only for manual `docker run -it` debugging.
CMD ["/bin/bash"]
