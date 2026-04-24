// Fake data for Spidey's RRC-native UI.
// Key idea: a thread is an immutable CORPUS. Each turn has a SELECTION
// (the subset of the corpus the model actually saw).

const CORPUS_AUTH = [
  { id: "m1",  pos: 1,  role: "user",      thread: "t-auth", text: "Let's build the auth module with OAuth 2.0. Three endpoints to start: authorize, token, refresh." },
  { id: "m2",  pos: 2,  role: "assistant", thread: "t-auth", text: "I'll set up the OAuth flow with PKCE. We'll need a code verifier, challenge computation, and token exchange. I'll scaffold service/auth/handler.go and service/auth/pkce.go." },
  { id: "m3",  pos: 3,  role: "user",      thread: "t-auth", text: "Use SHA-256 for the PKCE challenge. S256 not plain." },
  { id: "m4",  pos: 4,  role: "assistant", thread: "t-auth", text: "Confirmed. code_challenge = BASE64URL(SHA256(code_verifier)). Plain method disabled on the authorization server." },
  { id: "m5",  pos: 5,  role: "user",      thread: "t-auth", text: "What's the retry policy for token refresh?" },
  { id: "m6",  pos: 6,  role: "assistant", thread: "t-auth", text: "Token refresh retries 3 times with exponential backoff: 1s, 2s, 4s. After the third failure, surface an error and require re-authorization." },
  { id: "m7",  pos: 7,  role: "user",      thread: "t-auth", text: "Log the failures with the retry attempt number." },
  { id: "m8",  pos: 8,  role: "assistant", thread: "t-auth", text: "Added structured logs: attempt, status_code, latency_ms, next_retry_at. Logs tagged with trace_id for correlation." },
  { id: "m9",  pos: 9,  role: "user",      thread: "t-auth", text: "Does that retry policy apply to the initial OAuth flow too?" },
  { id: "m10", pos: 10, role: "assistant", thread: "t-auth", text: "No — the initial authorization exchange (/authorize → /token) uses a single attempt. Retries would confuse the user-agent redirect. Only /refresh retries. Want me to document this distinction in an ADR?" },
  { id: "m11", pos: 11, role: "user",      thread: "t-auth", text: "Yes. And add the ADR link to the README of the auth module." },
  { id: "m12", pos: 12, role: "assistant", thread: "t-auth", text: "ADR-0007 written: \"Retry policy: /refresh only\". Linked from service/auth/README.adoc." },
  { id: "m13", pos: 13, role: "user",      thread: "t-auth", text: "How do we handle clock skew on token expiry?" },
  { id: "m14", pos: 14, role: "assistant", thread: "t-auth", text: "Token validation allows a 30s clock skew on both iat and exp. Refresh triggers 60s before expiry to avoid the window edge." },
  { id: "m15", pos: 15, role: "user",      thread: "t-auth", text: "What happens if /refresh is called with an already-used refresh token?" },
  { id: "m16", pos: 16, role: "assistant", thread: "t-auth", text: "Refresh token rotation is enabled. Replay of a used refresh token is treated as a breach: all tokens for that session are revoked, the user is required to re-authenticate. Standard OAuth 2.0 BCP." },
  { id: "m17", pos: 17, role: "user",      thread: "t-auth", text: "Good. Now let's wire this into the session middleware. Should sessions carry the refresh token or a reference?" },
  { id: "m18", pos: 18, role: "assistant", thread: "t-auth", text: "Reference. Store the refresh token server-side, keyed by session id. The session cookie only carries an opaque id. Keeps the token out of JS reach and lets us revoke without touching the cookie." },
  { id: "m19", pos: 19, role: "user",      thread: "t-auth", text: "What cookie flags?" },
  { id: "m20", pos: 20, role: "assistant", thread: "t-auth", text: "HttpOnly, Secure, SameSite=Lax. Path=/. Max-Age matches session TTL (not token TTL). No Domain attribute unless we have subdomain sharing." },
  { id: "m21", pos: 21, role: "user",      thread: "t-auth", text: "Let's add rate limiting to /token. 10 requests per minute per client_id." },
  { id: "m22", pos: 22, role: "assistant", thread: "t-auth", text: "Added. Token bucket, 10rpm per client_id. 429 on exhaustion with Retry-After header. Separate bucket for /refresh at 30rpm — more aggressive since it's supposed to be frequent." },
  { id: "m23", pos: 23, role: "user",      thread: "t-auth", text: "I forgot — does our retry logic play nicely with the rate limit? If the refresh fails because of 429, does the client retry immediately?" },
  { id: "m24", pos: 24, role: "assistant", thread: "t-auth", text: "Good catch. Current logic retries on any non-2xx. I'll special-case 429: honor Retry-After, don't count it against the 3-attempt budget. Otherwise we could easily burn through retries during a rate-limit spike." },
  { id: "m25", pos: 25, role: "user",      thread: "t-auth", text: "And for the token refresh retries specifically — if we hit 429 three times in a row, do we surface the rate-limit error or a generic auth error?" },
];

// The selection for each turn (which messages were RRC-chosen).
// Keyed by the position of the USER prompt that triggered the selection.
const SELECTIONS = {
  1: { selected: [], excluded: [] }, // first message — novel, nothing to select
  3: { selected: [
        { id: "m2", score: 0.92, source: "cross-encoder", hop: 1, ce: 0.94, qud: 0, temp: 0.5 },
        { id: "m1", score: 0.76, source: "cross-encoder", hop: 2, ce: 0.82, qud: 0, temp: 0.33 },
      ], excluded: [] },
  5: { selected: [
        { id: "m4", score: 0.71, source: "cross-encoder", hop: 1, ce: 0.78, qud: 0, temp: 0.5 },
        { id: "m2", score: 0.58, source: "cross-encoder", hop: 2, ce: 0.67, qud: 0, temp: 0.25 },
      ], excluded: [
        { id: "m1", reason: "transitive-reduction", score: 0.48 },
        { id: "m3", reason: "below-threshold", score: 0.31 },
      ] },
  7: { selected: [
        { id: "m6", score: 0.89, source: "cross-encoder", hop: 1, ce: 0.94, qud: 0, temp: 0.5 },
        { id: "m5", score: 0.72, source: "both", hop: 2, ce: 0.71, qud: 1, temp: 0.33 },
      ], excluded: [] },
  9: { selected: [
        { id: "m6", score: 0.87, source: "both", hop: 1, ce: 0.88, qud: 1, temp: 0.33 },
        { id: "m4", score: 0.64, source: "cross-encoder", hop: 2, ce: 0.69, qud: 0, temp: 0.2 },
        { id: "m2", score: 0.59, source: "qud", hop: 2, ce: 0.42, qud: 1, temp: 0.14 },
      ], excluded: [
        { id: "m1", reason: "transitive-reduction", score: 0.51 },
        { id: "m5", reason: "transitive-reduction", score: 0.44 },
        { id: "m7", reason: "below-threshold", score: 0.28 },
        { id: "m8", reason: "below-threshold", score: 0.22 },
      ] },
  11: { selected: [
        { id: "m10", score: 0.93, source: "cross-encoder", hop: 1, ce: 0.96, qud: 0, temp: 0.5 },
      ], excluded: [] },
  13: { selected: [
        { id: "m6", score: 0.78, source: "both", hop: 1, ce: 0.82, qud: 1, temp: 0.14 },
        { id: "m2", score: 0.51, source: "qud", hop: 2, ce: 0.38, qud: 1, temp: 0.09 },
      ], excluded: [
        { id: "m10", reason: "below-threshold", score: 0.34 },
      ] },
  15: { selected: [
        { id: "m6", score: 0.81, source: "both", hop: 1, ce: 0.85, qud: 1, temp: 0.11 },
        { id: "m14", score: 0.64, source: "cross-encoder", hop: 1, ce: 0.7, qud: 0, temp: 0.5 },
      ], excluded: [] },
  17: { selected: [
        { id: "m16", score: 0.86, source: "both", hop: 1, ce: 0.88, qud: 1, temp: 0.5 },
        { id: "m6", score: 0.68, source: "qud", hop: 2, ce: 0.54, qud: 1, temp: 0.09 },
        { id: "m2", score: 0.52, source: "qud", hop: 3, ce: 0.44, qud: 1, temp: 0.06 },
      ], excluded: [
        { id: "m1", reason: "transitive-reduction", score: 0.41 },
      ] },
  19: { selected: [
        { id: "m18", score: 0.91, source: "cross-encoder", hop: 1, ce: 0.96, qud: 0, temp: 0.5 },
      ], excluded: [] },
  21: { selected: [
        { id: "m16", score: 0.64, source: "qud", hop: 1, ce: 0.52, qud: 1, temp: 0.2 },
        { id: "m6", score: 0.58, source: "both", hop: 2, ce: 0.62, qud: 1, temp: 0.06 },
      ], excluded: [
        { id: "m18", reason: "below-threshold", score: 0.38 },
        { id: "m2", reason: "below-threshold", score: 0.31 },
      ] },
  23: { selected: [
        { id: "m22", score: 0.87, source: "both", hop: 1, ce: 0.89, qud: 1, temp: 0.5 },
        { id: "m6", score: 0.76, source: "both", hop: 1, ce: 0.79, qud: 1, temp: 0.06 },
        { id: "m24", score: 0.0, source: "cross-encoder", hop: 0, ce: 0, qud: 0, temp: 0, phantom: true },
      ], excluded: [] },
  25: { selected: [
        { id: "m24", score: 0.94, source: "both", hop: 1, ce: 0.97, qud: 1, temp: 0.5 },
        { id: "m22", score: 0.81, source: "both", hop: 2, ce: 0.79, qud: 1, temp: 0.33 },
        { id: "m6", score: 0.72, source: "both", hop: 2, ce: 0.75, qud: 1, temp: 0.07 },
        { id: "m-x1", score: 0.61, source: "cross-encoder", hop: 1, ce: 0.68, qud: 0, temp: 0, crossThread: true, threadName: "API Design", snippet: "We decided rate limits return 429 with Retry-After in seconds, not HTTP-date format." },
      ], excluded: [
        { id: "m16", reason: "transitive-reduction", score: 0.42 },
        { id: "m23", reason: "below-threshold", score: 0.38 },
      ] },
};

// Tool calls and thinking blocks tucked into some responses
const RESPONSE_ENRICHMENT = {
  m2: {
    thinking: "User wants OAuth 2.0 with three endpoints. I should reach for PKCE by default — RFC 8252 strongly recommends it for all clients. Let me scaffold the handler and a PKCE helper.",
    tools: [
      { name: "FileWrite", args: "service/auth/handler.go", result: "wrote 128 lines · package auth · defines three handlers", status: "ok" },
      { name: "FileWrite", args: "service/auth/pkce.go", result: "wrote 42 lines · generates verifier + S256 challenge", status: "ok" },
    ]
  },
  m8: {
    tools: [
      { name: "FileEdit", args: "service/auth/refresh.go", result: "added structured logger · 14 insertions", status: "ok" },
    ]
  },
  m10: {
    thinking: "The question is whether the 3-attempt exponential backoff extends to /authorize. It shouldn't — /authorize is a redirect-driven flow, retries are the user-agent's job, not ours. Let me propose splitting retry policy per endpoint and write the ADR the user keeps gesturing at.",
    plan: {
      rationale: "Retry policy differs by endpoint. Lock it down with an ADR + code changes before the inconsistency bites us.",
      steps: [
        { text: "Write ADR-0007 documenting: /refresh retries 3× with backoff; /authorize and /token are single-attempt.", writes: "docs/adr/0007-auth-retry-policy.md" },
        { text: "Extract RetryPolicy interface; implement Single and ExponentialBackoff variants.",                                   writes: "service/auth/retry.go" },
        { text: "Wire Single into /authorize and /token handlers. Wire ExponentialBackoff into /refresh.",                             writes: "service/auth/handler.go" },
        { text: "Add a test asserting /token does not retry on transient 5xx." },
        { text: "Link the ADR from service/auth/README.adoc." },
      ],
    },
  },
  m22: {
    thinking: "Rate limiting at the /token endpoint. Token bucket is the right shape — bursts are fine, sustained abuse isn't. I'll use the existing middleware.",
    tools: [
      { name: "Grep", args: "ratelimit.*middleware", result: "found service/middleware/ratelimit.go · 1 match", status: "ok" },
      { name: "FileEdit", args: "service/auth/handler.go", result: "registered rate-limit middleware · 8 insertions", status: "ok" },
    ]
  },
  m24: {
    thinking: "The user caught a real bug. If we retry on 429 without honoring Retry-After, we'll burn the budget during rate-limit spikes. Need to special-case 429 in the retry logic.",
    subagent: {
      task: "Review all retry call sites for 429 handling and identify other places with the same bug",
      rounds: 4,
      status: "completed",
      result: "Found 2 other call sites: service/webhook/delivery.go and service/mcp/client.go. Both retry on any non-2xx. service/webhook/delivery.go has explicit Retry-After handling — OK. service/mcp/client.go has the same bug as auth — retries on 429 without checking Retry-After."
    },
    tools: [
      { name: "FileEdit", args: "service/auth/refresh.go", result: "special-cased 429 · honors Retry-After · doesn't consume retry budget", status: "ok" },
    ]
  },
};

const THREADS = [
  { id: "t-auth", name: "Auth Module", state: "running", pinned: true, lastActive: "now", elapsed: "12m", msgCount: 25, corpus: CORPUS_AUTH },
  { id: "t-api", name: "API Design", state: "idle", pinned: true, lastActive: "1h", msgCount: 312 },
  { id: "t-auth-alt", name: "Auth Module · alt retry", state: "idle", lastActive: "31m", msgCount: 14, parentId: "t-auth", branchAt: 14 },
  { id: "t-infra", name: "Infrastructure", state: "paused", lastActive: "8m", msgCount: 127, elapsed: "autonomous · 2h14m left" },
  { id: "t-schema", name: "Schema Migration", state: "idle", lastActive: "3h", msgCount: 42 },
  { id: "t-new", name: "Fresh start", state: "idle", lastActive: "—", msgCount: 0 },
  // Older / archived — here to demonstrate scale
  { id: "t-billing", name: "Billing webhooks", state: "idle", lastActive: "yesterday", msgCount: 89 },
  { id: "t-mcp", name: "MCP client refactor", state: "idle", lastActive: "yesterday", msgCount: 61 },
  { id: "t-onboard", name: "Onboarding copy", state: "idle", lastActive: "2d", msgCount: 28 },
  { id: "t-perf", name: "Perf regression · p99", state: "idle", lastActive: "3d", msgCount: 54 },
  { id: "t-obs", name: "Observability stack", state: "idle", lastActive: "4d", msgCount: 143, archived: true },
  { id: "t-hire", name: "Hiring rubric draft", state: "idle", lastActive: "1w", msgCount: 17, archived: true },
  { id: "t-cli", name: "CLI ergonomics", state: "idle", lastActive: "1w", msgCount: 76, archived: true },
  { id: "t-api-v1", name: "API Design · v1 RFC", state: "idle", lastActive: "2w", msgCount: 201, parentId: "t-api", branchAt: 120, archived: true },
];

const ACTIVITY = [
  { when: "just now",   what: <>Selection · turn 13 of <b>Auth Module</b> · <em>3 prereqs</em>, 1 cross-thread from <b>API Design</b></> },
  { when: "2m",    what: <>Carry-forward · QUD <em>qud-4</em> partially addressed by m-22</> },
  { when: "8m",    what: <>Autonomous round 7 · <b>Infrastructure</b> · paused for review</> },
  { when: "14m",   what: <>Subagent completed · reviewed 2 retry sites in <b>Auth Module</b></> },
  { when: "31m",   what: <>Thread branched · <b>Auth Module · alt retry</b> from position 14</> },
  { when: "1h",    what: <>Cross-thread edge · <b>API Design</b> msg-247 → <b>Auth Module</b> msg-22 · rate-limit decision</> },
];

const _DATA_IS_MAC = typeof navigator !== "undefined" &&
  /mac|iphone|ipad|ipod/i.test(navigator.platform || navigator.userAgent || "");

const PALETTE_ITEMS = [
  { kind: "action",  label: "New thread",                   hint: _DATA_IS_MAC ? "⌘N"   : "Ctrl+N" },
  { kind: "action",  label: "Switch scope to all-threads",  hint: _DATA_IS_MAC ? "⌘⇧A"  : "Ctrl+Shift+A" },
  { kind: "action",  label: "Enter plan mode",              hint: _DATA_IS_MAC ? "⌘P"   : "Ctrl+P" },
  { kind: "action",  label: "Start autonomous run",         hint: _DATA_IS_MAC ? "⌘⇧↵" : "Ctrl+Shift+↵" },
  { kind: "thread",  label: "Auth Module",                  hint: "active · now" },
  { kind: "thread",  label: "API Design",                   hint: "active · 1h" },
  { kind: "thread",  label: "Infrastructure",               hint: "autonomous · 2h14m left" },
  { kind: "skill",   label: "review-pr",                    hint: "skill · walks through a PR against style guide" },
  { kind: "skill",   label: "adr-write",                    hint: "skill · writes architectural decision records" },
  { kind: "search",  label: "rate limit retry budget",      hint: "search · 6 results" },
  { kind: "setting", label: "Configure providers",          hint: "settings" },
];

const QUDS = [
  { id: "qud-1", q: "How should the auth module be built?",                    parent: null,       by: "m1",  status: "partial",  addressedBy: ["m2"] },
  { id: "qud-2", q: "What is the PKCE challenge method?",                      parent: "qud-1",    by: "m3",  status: "resolved", addressedBy: ["m4"] },
  { id: "qud-3", q: "What is the retry policy for token refresh?",             parent: "qud-1",    by: "m5",  status: "resolved", addressedBy: ["m6"] },
  { id: "qud-4", q: "Does the retry policy apply to the initial OAuth flow?",  parent: "qud-3",    by: "m9",  status: "resolved", addressedBy: ["m10"] },
  { id: "qud-5", q: "How does retry interact with rate limiting?",             parent: "qud-3",    by: "m23", status: "resolved", addressedBy: ["m24"] },
  { id: "qud-6", q: "What error does refresh surface on repeated 429?",        parent: "qud-5",    by: "m25", status: "open",     addressedBy: [] },
];

const USER = {
  name: "John Doe",
  handle: "john",
  initials: "JD",
  host: "spidey.localhost:8420",
};

Object.assign(window, {
  CORPUS_AUTH, SELECTIONS, RESPONSE_ENRICHMENT, THREADS, ACTIVITY, PALETTE_ITEMS, QUDS, USER,
});
