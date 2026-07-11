# Security

## Threat model

Grudge runs an LLM-driven agent that issues shell commands and writes to the
filesystem. The threat model assumes the **model itself may be hostile** —
prompt injection from corpus content, a compromised provider, or a
malformed tool response can all attempt to coerce the agent into actions
the user did not authorize.

Concrete protections:

- **Sandbox boundary.** Threads with `sandboxed=true` route every Bash call
  and every file-tool operation through a Docker container with a
  bind-mounted workspace. See [`SANDBOX.md`](SANDBOX.md). The boundary
  contains workspace access, not malicious kernel-level code — the host
  shares the kernel. For stronger isolation use microVMs (Firecracker)
  or a user-namespace sandbox (bubblewrap).
- **Per-tool approval.** Every tool call has a default permission
  (`allow` / `ask` / `deny`). Write tools default to `ask`; read tools to
  `allow`. Users can override per-tool in Settings.
- **Path traversal defense.** Thread IDs feeding filesystem paths are
  validated against `^thread-\d+$` before interpolation
  (`service/datadir`, the single owner of the data-dir layout).
- **Path containment.** Sandboxed file tools resolve every path through
  `sandbox.ResolveWorkspacePath`; anything outside the workspace is
  rejected.

What Grudge does **not** protect against:

- Models tricked into exfiltrating data through allowed network calls
  (the sandbox does not restrict outbound network).
- Malicious code that escapes the container via a kernel vulnerability.
- A user opting out of the sandbox (`sandboxed=false`).
- API keys stored in the user's keychain — protection is at the OS
  keychain layer, not within Grudge.

## Reporting a vulnerability

**Please don't file security issues as public GitHub issues.**

Use GitHub's [private security advisories](https://github.com/elijahmontenegro/grudge-ai/security/advisories/new)
to report. Include a description, reproduction, and impact. We'll acknowledge
within 7 days.

If GitHub security advisories aren't appropriate (e.g., for an upstream
disclosure coordination question), open a regular issue with the subject
`SECURITY: contact request` and we'll move the conversation to a private
channel.
