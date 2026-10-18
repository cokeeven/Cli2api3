---
id: cli2api-chat-diagnostics
title: WorkBuddy and Trae chat diagnostics
scope: [chat, diagnostics, maintenance]
status: implementation
read-when: WorkBuddy or Trae output limits, incomplete streams, timeouts, cache usage or maintenance need diagnosis
summary: Local adapter termination, timeout, usage and maintenance rules; evidence limits and optional source pseudonyms.
related: [docs/DEVELOPMENT.md, docs/ARCHITECTURE_SUMMARY.md]
last-updated: 2026-10-18
---

# WorkBuddy / Trae diagnostics

These rules describe the local implementation. They do not establish why an old
request failed, that an upstream honors every requested limit, or that cached
tokens were actually reported for a particular account.

## Completion and usage

- Trae requires a real `done` event for success. Read failures, premature EOF and
  business errors do not produce a synthetic successful `stop` / `[DONE]` pair.
- An explicit `done.finish_reason` wins over `output.finish_reason`. If `done`
  omits the reason, the output reason survives. A real done with no reason uses
  `stop` as an adapter compatibility fallback, recorded as such. Tool-only
  completion uses `tool_calls` unless the upstream reports another material
  reason such as `length`. Token count equality never invents `length`.
- Client disconnection after confirmed completion can have request/attempt
  success with an after-completion disconnect diagnostic. Mid-stream cancellation
  stays canceled. Error/EOF with partial non-stream output or reported usage is
  not replayed on another account. Confirmed usage survives failures; an unknown
  total is not settled as zero. Lease release and usage settlement are one-shot.
- Cache-read priority is `cache_read_tokens`, then
  `prompt_tokens_details.cached_tokens`, then `prompt_cache_hit_tokens`, then
  `cache_read_input_tokens` (also recognized by the existing worker). Write
  priority is `cache_write_tokens`, then explicit `cache_creation_input_tokens`.
  These two input aliases were observed in authorized Trae tests; this establishes
  current compatibility, not their historical presence. Unverified `_total`
  variants remain response extensions and are not interpreted as cache usage.
  Missing/null falls through; explicit zero wins. Aliases are not added, cached
  input is not added to prompt or total tokens, and uncached input is not a cache
  write. Final cumulative usage replaces earlier snapshots rather than adding
  them. Numeric upstream details (including reasoning token details) remain in
  the non-stream response; raw text and response blobs are not diagnostic data.
- New request/attempt/stream error strings omit structured upstream echoes,
  oversized details and credential/header patterns. Classification and timeout
  phase remain separate. Existing historical records are preserved, not scrubbed
  or reconstructed by a migration.
- `content_length` describes the HTTP response. Unknown length remains unknown.
  Content/reasoning characters count Unicode code points, not tokens. Tool events
  count deltas, not tool executions. A tool-only answer can legitimately have no
  content. Reasoning is never moved into content.

## Caller parameters

`max_tokens` and `max_completion_tokens` accept a positive integer; one absent or
null field leaves the upstream default in control. Providing both fields is
rejected as ambiguous, including a null alongside the other field. WorkBuddy and
Trae map `max_completion_tokens` to upstream `max_tokens` with an explicit source
in diagnostics. Known catalog output ceilings are checked; unknown ceilings are
not guessed. No implicit 128000-token budget is inserted.

An explicit reasoning effort must be allowed by the known catalog. Unsupported
explicit efforts are rejected instead of silently reduced; absent efforts follow
the existing console/catalog defaults. Caller system/developer order and tool
history remain intact through required provider role conversion. Account prompt
stripping is only the existing explicit account setting; its per-request decision
is nullable in logs. This does not control hidden upstream instructions.

## Trae output-budget observations

The dated Trae CN evidence is scoped to `/api/agent/v3/llm_utils_chat` on client
3.4.4 / build 2.3.93438 / app 1.107.1. It covers `glm-5.3`, `kimi-k3`, and
`minimax-m3` on `chat_v3` using comparable `completion_tokens`, plus
`kimi-k2.6` on `solo_work_lite` using `completion_tokens`. `qwen3.8-flash` and
`Doubao-Seed-Code` use a content-token ceiling; the current usage path does not
carry a comparable content-token count, so request-level enforcement is marked
non-comparable. Other models/scenes, including `glm-5.3-flash`, remain untested
unless a matching case is added to the evidence set.

The public Trae model entry includes this scoped evidence and its known effective
catalog ceiling. Request diagnostics classify only comparable upstream usage:
`observed_ignored` means this request's completion exceeded the caller limit;
`not_exceeded` means only that this request did not exceed it; `unknown`,
`non_comparable`, `not_requested`, and `not_tested` preserve missing evidence
without guessing. These observations do not rewrite the outgoing budget, usage,
or finish reason and do not establish behavior for other models, regions, client
versions, or providers. Historical requests remain unknown.

Other advanced parameters are not newly declared supported by this work. In
particular this is not a full OpenAI protocol rewrite or a response cache.

## Time limits

The following integer environment settings accept 1–3600 seconds. Invalid values
fail startup. They do not require changing account or production configuration.

| Setting | Default | Meaning |
| --- | ---: | --- |
| `CLI2API_CHAT_HEADERS_TIMEOUT_SECONDS` | 45 | Connect and response headers |
| `CLI2API_CHAT_FIRST_EVENT_TIMEOUT_SECONDS` | 120 | Headers to first effective SSE event |
| `CLI2API_CHAT_IDLE_TIMEOUT_SECONDS` | 90 | Gap between effective SSE events |
| `CLI2API_CHAT_TOTAL_TIMEOUT_SECONDS` | 300 | Executor resource lifetime |

The first three apply to WorkBuddy and Trae chat. The executor total bound remains
shared. Content, reasoning, tools and terminal events are effective; comments,
heartbeat, metadata, role-only and usage-only frames do not reset output waits.
Continuous reasoning resets idle time but remains separate from first content.
The total bound still applies to a continuously producing stream.

Phase codes are `request_timeout_upstream_connect`,
`request_timeout_upstream_headers`, `request_timeout_first_event`,
`request_timeout_stream_idle`, and `request_timeout_total`. They use HTTP 504
before response headers, or a structured stream error after HTTP 200 has started.
Request and attempt are canceled with request scope. They do not trigger account
cooldown or a chain of failovers. Connect/header phase is observed through the
transport; if no connection callback is available, connect is the known phase.
First-event/content timings are measured at raw upstream SSE reads, before Trae
rewriting/lookahead. Unknown timings remain absent. Total duration is measured
from gateway request entry; executor lifetime starts after preparation.

Keep `X-Client-Request-ID` (or `X-Request-ID`) to a short safe identifier. The
gateway returns its own `X-Request-ID`. Associate the client ID, request, attempts
and diagnostics before attributing a timeout. A timeout with no matching record
still has an unknown cause.

Trae `3004` is explicitly classified as rate limiting independently of message
language. HTTP `Retry-After` is passed to the existing executor parser (header
before body hint, 30-second rate-limit floor, 10-minute parsed-hint ceiling).
Existing repeated model cooldowns continue to back off with a six-hour ceiling;
the model/prefix/platform policy remains intact. No
automatic replay starts after streaming output or tool events. Cooldown display
continues to use the existing account/model cooldown fields.

## Account failover diagnostics

For WorkBuddy and Trae, the attempt budget is fixed from initially ready,
model-eligible, non-exhausted, non-cooling and unsaturated candidates in the
permitted platform set. It is capped by `QODER_MAX_RETRY_ACCOUNTS` (default 64,
valid maximum 64) and the existing total request deadline. Each rejected account
is tried at most once. Explicitly configuring 4 still limits attempts to 4.
Routes involving other platforms keep their existing four-attempt ceiling.
Unknown quota does not mean zero. Free-model eligibility still requires catalog
evidence. Permission, region, protocol and usage guards are never bypassed.

Local concurrency saturation is reported as `concurrency_capacity`; it does not
set account cooldown or overwrite the account's last upstream failure. The
executor may wait in a FIFO queue for the same routing identity and eligible
route set. `CLI2API_CHAT_CAPACITY_QUEUE_MAX_WAITERS` (default 64, valid 0–256)
sets the process-wide maximum number of queued requests, and
`CLI2API_CHAT_CAPACITY_QUEUE_WAIT_SECONDS` (default 5, valid 0–60) sets each
waiter's maximum wait. Setting either to 0 disables waiting. Queue full, wait
timeout and request cancellation remain local-capacity outcomes; they do not
mark accounts unavailable. Request details record the queue outcome, initial
position and wait duration when diagnostics are available. This queue does not
wait through upstream cooldowns and does not replay a request after provider
execution begins. The HTTP `Retry-After` on local-capacity responses is a
five-second local retry hint; it is not an upstream reset time.
Application shutdown closes the queue and returns a distinct
`application_shutdown` result, waking queued requests without reserving account
slots.

Confirmed account authentication/quota rejection invalidates its failed session
binding by version comparison. Successful failover can migrate the session;
temporary capacity borrowing keeps its existing behavior. Request errors and
platform-wide limits do not trigger blind account rotation. Model and account
rate-limit scopes retain their existing cooldown boundaries. No replay starts
after content, reasoning, tool events or confirmed usage.

Active cooldown evidence is stored per restriction scope. First observation is
retained while the same local cooldown episode remains active; last observation
tracks the latest classified event that installs a cooldown. The applied scope
(`account` or `model`) is separate from any reported upstream scope. Evidence
source names the adapter/runtime boundary, not a raw packet capture. The local
retry deadline is the Pool's enforced gate; `upstream_reset_at` is populated
only when an adapter explicitly supplies a reset instant. A retry duration,
local five-minute fallback, or migration timestamp is not promoted to an
upstream reset or historical observation time. Old cooldown rows keep missing
evidence fields unknown. Route diagnostics expose only bounded fields (time,
code, source, scope and deadlines), not the stored error message.

The existing request detail now stores a bounded scheduling snapshot: initial
candidate count, attempt budget/count, per-account exclusion reason and snapshot
times, last upstream classification, and final scheduling stop reason. These
are separate facts: an earlier authentication rejection remains in its attempt
and diagnostic even when final selection fails with `route_no_candidates` or
`route_candidates_exhausted`. An explicit retry cap uses `route_attempt_limit`.
Runtime readiness, quota collection time and scheduling status are displayed
separately; readiness alone does not prove a new upstream login check. Concurrent
changes can make the final snapshot differ from the initial candidate count.
Snapshot-completeness is explicit if configured-account enumeration fails.

## Optional source fingerprint

`CLI2API_SOURCE_FINGERPRINT_ENABLED=true` enables an IP HMAC pseudonym, disabled
by default. It does not record raw IP, User-Agent, URL, prompt, tools or auth.
The random HMAC key exists only in the process, so fingerprints change after a
restart. Source ports do not affect it. Existing request-log retention applies:
7 days / at most 20000 records, with the existing hourly purge cadence.

`CLI2API_TRUSTED_PROXY_CIDRS` is an optional comma-separated CIDR list. Only when
the socket peer is trusted is `X-Forwarded-For` inspected, walking right to left
until an untrusted hop. No proxy is trusted by default. Other forwarding headers
are not interpreted. Invalid CIDRs fail startup. Configure only actual proxies;
trusting a client network permits clients in that network to choose forwarded
addresses. Oversized chains produce no fingerprint.

## Maintenance

The existing 30-second maintenance loop, account-wide persistent task lease and
maintenance history are reused. No second scheduler is installed. History kinds
separate check-in, credential checks, quota refresh and existing daily activities.
Standalone check-in uses the same lease while retaining its existing history row.

System settings store `maintenance_schedule` with independent credential/quota/
daily enable switches, credential interval 5–1440 minutes (default 15), and daily
start time `HH:mm` (default 22:00, host timezone). The existing quota interval is
5–1440 minutes (default 30), with stable 0–10% jitter. Existing check-in account
opt-in/time settings are unchanged. The automation master switch gates scheduled
work; manual supported actions remain available while it is paused. Disabling a
kind stops future scheduling, while the master switch also cancels active
automatic contexts. Schedule edits affect subsequent due checks/results; they
do not rewrite historical next-eligible times.

Credential checks do not depend on check-in opt-in and do not force a token
exchange or perform chat. Success means the existing expiry-aware path accepted
the credential, not that a new token was issued. Revoked refresh credentials can
still require login. Provider credential locks and compare-and-swap persistence
protect newer login state from late refresh results.

Quota requests share a four-slot cap and same-account deduplication; manual
refresh can join an active request and bypass failure backoff. Failure retains
the prior snapshot, records a sanitized result and next eligible retry, with
30-second exponential backoff up to one hour plus explicit upstream Retry-After.
The failure ladder survives elapsed waits and resets on success. The persistent
task history also preserves scheduled retry state across restart. Busy account
leases defer automatic work. No quota result clears manual disable/auth limits.
Disabled or invalid quota schedules do not register an in-flight operation.
After a successful credential/quota run, the next claim uses its completion
time and the current configured interval, so shorter and longer period changes
take effect without rewriting history. A failed run keeps its persisted backoff;
changing the normal period does not bypass a failure retry.

Maintenance tasks and settings are available in the existing Logs maintenance
panel. The account filter chooses manual action targets. A snapshot is shown as
stale beyond interval plus maximum jitter, or when its latest refresh failed;
missing time/status remains unknown. A fresh snapshot proves when collection
occurred locally, not that its values explain a historical complaint. Existing
daily activities retain their own eligibility and confirmation rules.

## Schema and local verification

Migrations 028–036 add diagnostic fields, timeout/content timing, an optional
source fingerprint, task kind, nullable routing diagnostics and the scoped output
budget result. Shipped 001–035 SQL remains byte-for-byte unchanged. Old rows
retain unknown diagnostic values and the existing daily-task kind; account
credentials, keys, settings and history are not reconstructed.

Use temporary databases, synthetic credentials, isolated HTTP fault fixtures and
local-only listeners for regression tests. Real-account tests require separate
authorization and a fingerprinted modified build; synthetic tests cannot prove
real cache hit rate, natural renewal or historical timeout causes. Server
inspection, deployment and outbound alerts require their own authorization.
