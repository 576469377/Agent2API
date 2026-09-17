# Agent2API

A local reverse-proxy gateway that translates AI agent platforms' private protocols into standard LLM APIs.

Claude Code, Codex CLI, and any OpenAI / Anthropic client work **without modification**: it exposes Chat Completions, Responses, and Anthropic Messages, with a built-in multi-platform Hub that routes each request by model name — the bundled platform today is **WorkBuddy / CodeBuddy**.

[简体中文](README.md) · **English** · [Changelog](CHANGELOG.md) · [Disclaimer](DISCLAIMER.md)

> [!WARNING]
> **A personal learning and research project — not production software.** For use only with **your own authorized accounts**, on your own machine or a private environment, at your own risk.
> It reads the **credentials** your desktop client is already logged in with (credentials = your account — never share them), and its operation **may not comply with upstream terms of service**. The author does not encourage or support commercial use or exposing it as a public service.

## Contents

**Start here**: [Background](#background) · [Quick Start](#quick-start) · [Web Console](#web-console) · [Multi-Account Pool](#multi-account-pool)

**Reference**: [Supported Endpoints](#supported-endpoints) · [Configuration](#configuration) · [Security](#security) · [Architecture](#architecture)

**Elsewhere**: [Testing](#testing) · [FAQ](#faq) · [Known Limitations](#known-limitations) · [Roadmap](#roadmap) · [Contributing](#contributing) · [License](#license)

---

## Background

Clients like Claude Code and Codex CLI only speak the standard OpenAI / Anthropic protocols, while AI agent platforms expose only private upstreams. So the quota you paid for can't reach the clients you actually like.

This project runs a protocol-translation layer on your machine: it doesn't fake a protocol, it translates the standard ones into the upstream's private one.

```
Claude Code / Codex / any OpenAI client
        │  /v1/chat/completions · /v1/responses · /v1/messages
        ▼
   ┌────────────────────────────────────────┐
   │  Agent2API                             │
   │  downstream protocols ─ IR ─ adapters  │
   └────────────────────────────────────────┘
        │  https://copilot.tencent.com/v2/chat/completions
        ▼
   WorkBuddy upstream
```

With multiple platforms, the Hub routes each request by its `model` field to the upstream that owns it (see [Multi-platform config](#multi-platform-config-upstreamplatforms)).

**Highlights**

- **All three protocols at once**: Chat Completions / Responses / Anthropic Messages, streaming and non-streaming
- **Reasoning**: the upstream's `reasoning_content` is passed through as each protocol's thinking output
- **Tool calls**: native `tool_calls` channel, with fragment reassembly
- **Credential reuse**: reads your desktop client's existing login — no re-authentication
- **Multi-account pool**: add accounts when quota runs short; routed by account health, failed requests fail over automatically
- **Multi-platform hub**: declare several upstreams via `upstream.platforms`; they share one process and are routed by model name. With zero config, every built-in platform is auto-integrated
- **Content sanitization**: keeps client boilerplate from tripping upstream keyword review (required for Claude Code / Codex)
- **Built-in console**: compiled into the single binary, no frontend build step, i18n + light/dark themes

---

## Quick Start

```bash
# Option 1: install directly (Go 1.23+)
go install github.com/576469377/Agent2API/cmd/agent2api@latest

# Option 2: build from source
git clone https://github.com/576469377/Agent2API && cd Agent2API
make build                     # produces bin/agent2api
```

**No configuration required.** The gateway auto-detects your desktop client's existing login; without one, use `agent2api login` for device-code login.

```bash
agent2api                      # starts on 127.0.0.1:8787
agent2api models               # list available models
```

The banner prints the console URL — open it in a browser to manage the gateway. You can also confirm the gateway is alive with one command:

```bash
curl -s http://127.0.0.1:8787/health
# {"platform":"workbuddy","platforms":["workbuddy"],"status":"ok","time":…}
```

> [!WARNING]
> **Listening on `0.0.0.0` without `-api-key` means anyone on your network can spend your quota.**

### Connecting clients

**Claude Code**

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8787
export ANTHROPIC_API_KEY=anything    # not validated when no gateway key is set
```

**Codex CLI**: set base_url to `http://127.0.0.1:8787` and the API key to any value (it uses `/v1/responses`).

**Any OpenAI SDK**: set base_url to `http://127.0.0.1:8787/v1`.

> [!TIP]
> **Do not disable sanitization** when using Claude Code / Codex: their system templates contain security wording that upstream review misclassifies.

### Examples

The same prompt, once per protocol:

```bash
# OpenAI Chat Completions
curl http://127.0.0.1:8787/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hello"}]}'

# Anthropic Messages
curl http://127.0.0.1:8787/v1/messages \
  -H "Content-Type: application/json" -H "anthropic-version: 2023-06-01" \
  -d '{"model":"deepseek-v4-flash","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}'

# OpenAI Responses
curl http://127.0.0.1:8787/v1/responses \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-v4-flash","input":"hello"}'
```

> [!NOTE]
> Swap `deepseek-v4-flash` in the examples for any model listed by `agent2api models`; unknown model names pass through to the default platform for the upstream to decide.

---

## Web Console

The console is compiled in with `go:embed`: no frontend build, no CDN dependencies (charts are native SVG). Visit `http://127.0.0.1:8787` after starting.

| Page | What it shows |
|---|---|
| **Overview** | Status wall, request trend, token usage, per-protocol/model/account rankings; auto-refresh with countdown |
| **Accounts** | Pool management: scheduling and login state, credential expiry, per-account usage; enable/disable, reset cooldown, re-login, add account |
| **Chat** | Talk to the gateway directly to verify protocols and models |
| **Platforms** | Upstream URL, account count, model count, health |
| **Models** | Model list with capability tags; switchable account × model matrix |
| **Request Log** | Last 200 requests (annotated with the account actually used), failed rows highlighted |
| **Settings** | Sanitization toggle (hot reload), model refetch, access key, effective config |

> Only the Chat page's **math rendering** lazily loads KaTeX from a CDN on first use; offline it falls back to plain monospace text. Everything else has zero external dependencies.

---

## Multi-Account Pool

One account not enough quota? Put several credentials in a pool directory — the gateway routes across them automatically and fails over on errors.

```bash
agent2api login -out auths/account-a.json    # log in one by one
agent2api login -out auths/account-b.json
agent2api -accounts-dir auths                 # start (auto-enabled if ./auths exists)
```

You can also **add accounts** from the console's Accounts page (device-code login): credentials are written to disk and picked up by hot reload, which scans the directory every 10 seconds — no restart needed.

### Scheduling semantics

| Situation | Behavior |
|---|---|
| Normal routing | Requests are picked by **weighted random** over account health — healthier accounts are chosen more often but never hog traffic. Sessions **stick to one account**, so long contexts don't drift between accounts |
| Concurrency pressure | Each account has a **concurrency limit** (default 4, `max_concurrency_per_account`): a saturated account stops being selected and requests **queue for a slot** instead of failing (local backpressure). In-flight counts show up in the console matrix header as "2/4" |
| Rate limited (429) **with** an exact reset time from upstream | Only **that model on that account** cools down until the stated time (capped at 24h), then the request fails over; other models on the same account keep working |
| Rate limited (429) **without** a reset time | **No lockout**: the account's health score drops and the request fails over — a single ordinary rate limit must not suspend a whole account/model (real-world logs showed "one question, both accounts cooling") |
| Quota exhausted (14018) | Also **no lockout**: the upstream `credits` field is a cost multiplier, not an account balance, and the upstream gives no recovery time. Health drops, request fails over, and free/cheap models on the same account keep working |
| Auth failed (401 after refresh) | The whole account is marked **blocked** (terminal — it does not auto-recover). This is a dead account, not a rate limit; retrying is pointless. Recover via the console's **re-login** or **reset cooldown** |
| Request fault (context too long, …) | Fail fast — switching accounts can't help |
| Transport error / 5xx | Fail over but don't cool down (could be a global blip), with pool-level backoff and jitter |
| Model unservable pool-wide | Return 429 with the **earliest** thaw time, suggesting a different model; if everything is auth-failed instead, return 401 (retrying can never succeed) |

Health = success-rate EWMA×0.6 + latency EWMA×0.4, with a penalty for consecutive failures and shrinkage toward neutral on few samples. Exact parameters live in [`internal/adapter/pool.go`](internal/adapter/pool.go).

The Accounts page lets you intervene at runtime — no restart needed:

- **Disable / enable**: pull an account out of rotation temporarily
- **Reset cooldown**: unfreeze manually when upstream recovers early (also the recovery path for blocked accounts)
- **Re-login**: device-code login for a dropped account, writing back to the same file
- Per-account scheduling state, login state, credential expiry and usage (persisted across restarts)

> Desktop-client credentials and pool files can coexist — each account card is labeled with its source. The desktop client going offline **does not affect** the gateway: credentials are read once at startup and refreshed by the gateway itself.

---

## Supported Endpoints

| Endpoint | Protocol | Streaming | Non-streaming |
|---|:-:|:-:|:-:|
| `POST /v1/chat/completions` | OpenAI Chat Completions | ✅ | ✅ |
| `POST /v1/responses` | OpenAI Responses | ✅ | ✅ |
| `POST /v1/messages` | Anthropic Messages | ✅ | ✅ |
| `GET /v1/models` | Model list (includes aliases declared in `models.aliases`) | — | ✅ |
| `GET /metrics` | Prometheus text-format metrics (same source as the console; API key required) | — | ✅ |
| `GET /health` | Health check (reports status and platform IDs only; does not probe upstream) | — | ✅ |

Supported features: text, reasoning, tool calls, multi-turn conversation, system prompts, sampling parameters, image input.

The management and debugging UI lives at `/` (see [Web Console](#web-console)) — it is not an API.

---

## Configuration

Precedence: CLI flags > environment variables > config file > built-in defaults. Example: [`config.example.json`](config.example.json).

| Flag | Environment variable | Description |
|---|---|---|
| `-config` | — | Config file path (JSON) |
| `-port` | `AGENT2API_PORT` | Listen port, default 8787 |
| `-host` | `AGENT2API_HOST` | Listen address, default 127.0.0.1 |
| `-api-key` | `AGENT2API_API_KEY` | Gateway access key; empty disables auth |
| `-credential` | `AGENT2API_CREDENTIAL_PATH` | Credential file path; auto-detected when empty |
| `-accounts-dir` | — | Pool directory; every `*.json` counts as one account |
| `-base-url` | `AGENT2API_BASE_URL` | Upstream URL |
| `-metrics-file` | — | Metrics persistence path; defaults to `metrics.json` next to the config file, or `agent2api-metrics.json` in the working directory without one |
| `-no-persist` | — | Disable metrics persistence |
| `-no-sanitize` | — | Disable content sanitization |
| `-platform` | — | Pin a single platform (only `workbuddy` today); by default all built-in platforms are auto-integrated |

### Multi-platform config (upstream.platforms)

To aggregate several upstream platforms in one gateway, declare them under `upstream.platforms`:

```json
{
  "upstream": {
    "platforms": [
      { "id": "workbuddy", "accounts_dir": "auths", "sanitize": true }
    ]
  }
}
```

- **Routing**: `/v1/models` merges every platform's catalog (each entry carries its `platform`); a request's `model` goes to the platform whose catalog contains it, and **unknown model names pass through to the default platform** (the first in the list) — preserving the single-platform "let upstream decide" semantics
- **Field fallback**: a platform's `credential_path` / `accounts_dir` / `base_url` / timeout entries fall back to the top-level fields when omitted; platforms are fully isolated (separate pools, cooldowns, login sessions)
- **⚠️ The `sanitize` exception**: a boolean can't distinguish "omitted" from "false" — when platforms are declared explicitly, **each one must state `"sanitize": true`** (required for Claude Code / Codex); omitting it turns sanitization off for that platform
- **Zero config (default)**: all built-in platforms are auto-integrated (just workbuddy today); platforms that can't log in are skipped with a warning

Subcommands:

| Command | Purpose |
|---|---|
| `agent2api` | Start the gateway |
| `agent2api login` | Device-code login |
| `agent2api models` | List available models (merged across platforms; `-platform` filters to one) |
| `agent2api dedupe` | Remove duplicate/invalid credentials from a directory; previews by default, deletes with `-yes` |

> [!NOTE]
> `server.write_timeout_sec` and `log.level` / `log.format` are not read at the moment (streaming responses deliberately have no write timeout; log level/format are not implemented yet) — kept for backward compatibility.

### Model aliases (models.aliases)

Clients send fixed model names (Claude Code asks for `claude-sonnet-4-5-*`, Codex for `gpt-5-*`) that often are not in your account's catalogue. The alias table maps them onto models your accounts actually have, so clients work **unmodified**:

```json
{
  "models": {
    "aliases": {
      "claude-sonnet-4-5-20250929": "glm-5.3",
      "gpt-4o": "workbuddy/deepseek-v4-pro"
    }
  }
}
```

- Two forms: `"target-model"` (same platform) or `"platform-id/target-model"` (different platform **and** model)
- Aliases are listed by `/v1/models` and on the console's Models page (clients usually only use names they can discover)
- **No recursion** (the target must be a real model); a typo in the platform id degrades to a plain model name and logs a warning instead of breaking the gateway
- Aliases only affect routing and the model name sent upstream — session affinity, pool scheduling and sanitization are untouched
- The call log records both the requested model and the model actually used

### Concurrency limit (max_concurrency_per_account)

```json
{ "upstream": { "max_concurrency_per_account": 4 } }
```

Clients fire requests in parallel, and a dozen in-flight requests on one account is the main source of upstream 429s. The limit **queues requests in front of the account** (local queueing, bounded by the client's own timeout) and prefers accounts that still have headroom:

- Default **4**; set `0` to disable (unlimited)
- This is **backpressure, not failure**: queued requests neither fail over nor mark an account as cooling
- Queueing only happens when *every* account is saturated — as long as one has room, requests go straight through
- Platforms may override it via `upstream.platforms[].max_concurrency_per_account`

### Prometheus metrics (/metrics)

`/metrics` exposes the same metrics as the console in Prometheus text format (`agent2api_requests_total`, `agent2api_in_flight_requests`, per-model/protocol/account counters, `agent2api_tokens_*_total`, plus `agent2api_build_info{version,go}`).

```yaml
scrape_configs:
  - job_name: agent2api
    authorization:
      credentials: <api_key>
    static_configs:
      - targets: ["127.0.0.1:8787"]
```

> The average decode-speed metric is **omitted** when there are no samples — 0 tok/s and "never measured" are different things, and emitting 0 triggers false alerts.

---

## Security

1. **Credential source**: the gateway never asks for your password — it reads the credential file your desktop client is already logged in with, effectively lending that login to a local process.
2. **The credential file is your account**: never copy it elsewhere or commit it. `.gitignore` already excludes the relevant paths.
3. **Binds to `127.0.0.1` by default**: switching to `0.0.0.0` without `-api-key` lets anyone on your network spend your quota. Shared environments must set `-api-key` and authenticate at a reverse proxy in front.
4. **Logs and metrics** may record model names and token counts; `-no-persist` disables persistence.

See [SECURITY.md](SECURITY.md) to report vulnerabilities.

---

## Architecture

```
internal/
├── llm/          IR layer (zero deps): request/response/error representations
├── adapter/      Upstream platform seam: the Adapter interface
│   └── workbuddy/  WorkBuddy implementation: auth / request / SSE / models / sanitize
├── api/          Downstream protocol codecs
│   ├── common/     SSE writing, error payloads (shared by all three)
│   ├── openai/chat/ · openai/responses/ · anthropic/messages/
├── app/          HTTP routing and orchestration (hub: multi-platform runtime)
├── obs/          Metrics collection and persistence
├── web/          Built-in console (go:embed)
└── config/       Configuration
```

**Key design point**: `api/*` and `adapter/*` never import each other — they communicate only through `internal/llm`, collapsing "N platforms × M protocols" from N×M into N+M. Adding a platform requires no changes to the three downstream protocols.

Multi-platform orchestration lives in the **Hub** ([`internal/app/hub.go`](internal/app/hub.go)): at startup each platform gets its own adapter / pool / login session, a "model ID → platform" index is built, requests are routed by model name, and unknown names pass through to the default platform. To the Hub, a `Pool` is just an ordinary Adapter — the multi-account and multi-platform layers don't know about each other.

The interfaces are deliberately tiny: `Adapter` has just 3 methods (`Stream` / `ListModels` / `Name`) and `ResponseStream` just 1 (`Recv`). A fake adapter for tests costs about ten lines.

Further reading:

- Full design — [`docs/design/01-架构设计.md`](docs/design/01-架构设计.md)
- Upstream protocol teardown — [`docs/research/02-WorkBuddy上游协议逆向.md`](docs/research/02-WorkBuddy上游协议逆向.md)

---

## Testing

```bash
make check        # gofmt + go vet + go test
make cover        # coverage
go test ./... -race
```

Protocol translation uses **golden-frame replay**: sanitized real upstream samples live in [`fixtures/`](fixtures/) and are fed to the parser frame by frame, asserting the resulting **IR event sequence** rather than bytes — fully offline, never hitting upstream.

12 packages, roughly 120 test cases. Core package coverage (`make cover`, numbers drift with code): `common` 94%, `obs` 94%, `llm` 84%, `adapter` 64%, `config` 58%, `app` 38%, `workbuddy` 55%.

---

## FAQ

**Startup fails with "all platforms failed to initialize" or `no_credential`?**
No usable credentials on this machine. If the WorkBuddy / CodeBuddy desktop client is installed and logged in, it is detected automatically; otherwise run `agent2api login` first.

**Claude Code connects, but every request gets blocked?**
Do not disable sanitization (drop `-no-sanitize`). The client's fixed system-template security wording trips upstream keyword review.

**What can `model` be?**
Check `agent2api models` or the console's Models page for the models your account can actually use; unknown model names pass through to the default platform for the upstream to decide.

**Why does a non-streaming request take as long to first byte as streaming?**
The upstream only supports streaming; non-streaming responses are aggregated proxy-side, so there is nothing to return earlier.

**How do I change the port / listen address?**
`-port` / `-host` (or the matching environment variables, see [Configuration](#configuration)). If you expose it to your LAN, set `-api-key` as well.

**Session affinity doesn't seem to work / one session lands on different accounts?**
Affinity derives its routing key in this order: **headers** (`session_id`, `X-Session-Id`) → `metadata.user_id` / `user` in the body → a hash of "system prompt + first user message". If you put Nginx in front, note that it **drops headers containing underscores by default** (`session_id` is one of them) — add `underscores_in_headers on;` in the `http` or `server` block, otherwise that signal never reaches the gateway.

**Upstream rate limits as soon as I run things in parallel?**
Each account allows 4 concurrent requests by default (`upstream.max_concurrency_per_account`); anything beyond that queues inside the gateway instead of hitting upstream. Lower it if you still see frequent 429s, or set `0` to disable queueing and let requests leave immediately. The "in-flight / limit" reading in the console matrix header tells you whether the pool is saturated or the limit is simply too tight.

---

## Known Limitations

**By design**

- **Upstream does not support non-streaming requests**: non-streaming responses are aggregated proxy-side, so TTFB matches streaming
- **Single process, single instance**: cooldown state, session affinity and concurrency slots all live in memory; multiple instances act independently and will hammer upstream (shared state needs external storage — see Roadmap)
- **The concurrency limit is per process**: with multiple instances each one counts on its own, so there is no global limit
- **Quota query not implemented**: that upstream route requires enterprise privileges (403)
- **Reasoning is one-way**: the upstream sends no thinking signature — Anthropic `thinking` blocks always carry an empty signature, and each turn re-reasons from scratch
- **DSML text-mode tool-call fallback not implemented**: upstream currently uses native `tool_calls`; it must be added if upstream ever regresses

**Other behavior**

- **Metrics persistence is on by default**: it writes `metrics.json` next to the config file (`agent2api-metrics.json` in the working directory without a config file), containing usage stats (model names, accounts, platforms, token counts). It is in `.gitignore`, but exclude it manually when packaging
- **"Add account" in the console needs a pool directory**: without one, credentials go to `~/.workbuddy`; configure `-accounts-dir` (or create `auths/`) and they join the pool automatically

---

## Roadmap

- [ ] Pool circuit breaking and quota queries
- [ ] A second platform adapter (Hub routing is ready; only the adapter is missing)
- [ ] `cmd/probe` protocol drift detection
- [ ] Multi API key distribution with per-key usage/limits (today: a single key plus in-process limits)
- [ ] Shared state (cooldowns / affinity / concurrency slots) to support multi-instance deployments
- [x] Model alias routing (`models.aliases`, sub2api's composite groups)
- [x] Per-account concurrency limit with in-flight visibility (sub2api's per-account concurrency limit)
- [x] Prometheus text metrics (`/metrics`)

---

## Contributing

Issues and PRs are welcome — please read [CONTRIBUTING.md](CONTRIBUTING.md) first.

> [!IMPORTANT]
> **Never paste real credentials, tokens, or account nicknames** in an issue.

---

## License

**TBD** — the repository ships no LICENSE file, so all rights are reserved by default. Do not use commercially or redistribute until a license is chosen.

This is a personal learning and research project, for use only with your own authorized accounts on your own machine or a private environment, at your own risk. Full terms in [DISCLAIMER.md](DISCLAIMER.md).
