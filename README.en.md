# Agent2API

A local reverse-proxy gateway that translates AI agent platforms' private protocols into standard LLM APIs.

**WorkBuddy / CodeBuddy** is supported today. It exposes OpenAI Chat Completions, OpenAI Responses, and Anthropic Messages — so Claude Code, Codex CLI, and any OpenAI/Anthropic client work **without modification**.

[简体中文](README.md) · **English** · [Changelog](CHANGELOG.md) · [Disclaimer](DISCLAIMER.md)

---

> [!WARNING]
> **A personal learning and research project — not production software.** For use only with **your own authorized accounts**, on your own machine or a private environment, at your own risk.
> It reads the **credentials** your desktop client is already logged in with (credentials = your account — never share them), and its operation **may not comply with upstream terms of service**. The author does not encourage or support commercial use or exposing it as a public service.

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

**Highlights**

- **All three protocols at once**: Chat Completions / Responses / Anthropic Messages, streaming and non-streaming
- **Reasoning**: upstream `delta.reasoning_content` → `reasoning_content` / `thinking` blocks / reasoning summary
- **Tool calls**: native `tool_calls` channel, with fragment reassembly
- **Credential reuse**: reads your desktop client's existing login — no re-authentication
- **Multi-account pool**: put several accounts in a pool and it routes across them by account health, swapping on rate limits
- **Content sanitization**: dodges upstream keyword review (required for Claude Code / Codex)
- **Built-in console**: compiled in via `go:embed`, no frontend build step, i18n + light/dark themes

---

## Quick Start

```bash
# Option 1: install directly (Go 1.23+)
go install github.com/576469377/Agent2API/cmd/agent2api@latest

# Option 2: build from source
git clone https://github.com/576469377/Agent2API && cd Agent2API
make build                     # produces bin/agent2api

agent2api                      # starts on 127.0.0.1:8787
agent2api models               # list available models
```

**No configuration required**: it auto-detects your desktop client's existing login. Without a desktop client, use `agent2api login` for device-code login.

The banner prints the console URL — open it in a browser to manage the gateway.

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

```bash
curl http://127.0.0.1:8787/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hello"}]}'

curl http://127.0.0.1:8787/v1/messages \
  -H "Content-Type: application/json" -H "anthropic-version: 2023-06-01" \
  -d '{"model":"deepseek-v4-flash","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}'

curl http://127.0.0.1:8787/v1/responses \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-v4-flash","input":"hello"}'
```

---

## Web Console

Compiled into the binary with `go:embed` — no frontend build, no CDN dependencies, native SVG charts. Visit `http://127.0.0.1:8787`:

| Page | What it shows |
|---|---|
| **Overview** | Status wall, request trend, token usage, per-protocol/model/**account** rankings; auto-refresh with countdown |
| **Accounts** | Pool management: scheduling state, login state and credential expiry, **per-account usage**; enable/disable, reset cooldown, re-login, add account |
| **Chat** | Talk to the gateway directly to verify protocols and models; tables & math rendering |
| **Platforms** | Upstream URL, account count, model count, health |
| **Models** | Model list with capability tags; switchable **account × model matrix** |
| **Request Log** | Last 200 requests (with the account actually used), failed rows highlighted |
| **Settings** | Sanitization toggle (hot reload), model refetch, access key, effective config |

![Console · Accounts](docs/images/console-accounts.png)

> Account names in the screenshot are demo data. Only **math rendering** lazily loads KaTeX on first use and degrades to plain text offline.

---

## Multi-Account Pool

One account not enough quota? Put several credentials in a pool directory — the gateway routes across them by account health and swaps accounts on rate limits.

```bash
agent2api login -out auths/account-a.json    # log in one by one
agent2api login -out auths/account-b.json
agent2api -accounts-dir auths                 # start (auto-enabled if ./auths exists)
```

You can also **add accounts** from the console's Accounts page (device-code login); credentials are written to disk and picked up by **hot reload** (directory scanned every 10s — no restart needed).

**Scheduling semantics**

| Situation | Behavior |
|---|---|
| Routing | Requests are picked by **weighted random** over account health (success-rate EWMA×0.6 + latency EWMA×0.4, consecutive failures penalized; scores shrink toward neutral on few samples) — healthier accounts are picked more but never hog traffic, and a fresh pool degrades to even rotation. Sessions also **stick to one account** (identified via `metadata.user_id` / `user`), so long contexts don't drift between accounts |
| Rate limited (429) | Cool down **that model on that account** and swap (other models on the same account keep working); the cooldown parses the upstream's exact reset time (e.g. "resets at 23:21:31") and thaws on schedule — exponential backoff otherwise (10s floor, 60s base, 24h cap) |
| Auth failed (401 after refresh) | 10-minute cooldown of the whole account |
| Request fault (context too long, …) | Fail fast — rotating accounts can't help |
| Transport error / 5xx | Swap but don't cool down (could be a global blip), with pool-level backoff and jitter |
| All cooling | Return 429 with the **earliest** thaw time; all auth-failed returns 401 (retrying can never succeed) |

**Account management in the console**: per-account scheduling state (▸ marks the next one used), login state and credential expiry, per-account usage (persisted across restarts); supports **disable/enable** (temporarily pull an account out of scheduling), **reset cooldown** (when upstream recovers early), and **re-login** (device-code login for a dropped account, writing back to the same file).

> Desktop-client credentials and pool files can coexist — each account card is labeled with its source. The desktop client going offline **does not affect** the gateway: credentials are read once at startup and refreshed by the gateway itself.

---

## Supported Endpoints

| Endpoint | Protocol | Streaming | Non-streaming |
|---|:-:|:-:|:-:|
| `POST /v1/chat/completions` | OpenAI Chat Completions | ✅ | ✅ |
| `POST /v1/responses` | OpenAI Responses | ✅ | ✅ |
| `POST /v1/messages` | Anthropic Messages | ✅ | ✅ |
| `GET /v1/models` | Model list | — | ✅ |
| `GET /health` | Health check | — | ✅ |
| `GET /` | Web console | — | ✅ |

Text, reasoning, tool calls, multi-turn, system prompts, sampling parameters, image input.

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
| `-metrics-file` | — | Metrics persistence path |
| `-no-persist` | — | Disable metrics persistence |
| `-no-sanitize` | — | Disable content sanitization |
| `-platform` | — | Force a specific upstream platform (only `workbuddy` today; auto-detect by default, for debugging) |

Subcommands: `agent2api` (serve), `agent2api login` (device-code login), `agent2api models` (list models), `agent2api dedupe` (remove duplicate/invalid credentials from a credential directory; previews by default, deletes with `-yes`, defaults to `~/.workbuddy`, override with `-dir`).

> [!NOTE]
> `server.write_timeout_sec` and `log.level` / `log.format` are **not read** at the moment (streaming responses deliberately have no write timeout; log level/format are not implemented yet) — kept for backward compatibility.

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

`Adapter` has just 3 methods (`Stream` / `ListModels` / `Name`) and `ResponseStream` has just 1 (`Recv`), so faking an adapter for tests costs almost nothing.

See [`docs/design/01-架构设计.md`](docs/design/01-架构设计.md) for the full design and [`docs/research/02-WorkBuddy上游协议逆向.md`](docs/research/02-WorkBuddy上游协议逆向.md) for the upstream protocol teardown.

---

## Testing

```bash
make check        # gofmt + go vet + go test
make cover        # coverage
go test ./... -race
```

Protocol translation uses **golden-frame replay**: sanitized real upstream samples live in [`fixtures/`](fixtures/) and are fed to the parser frame by frame, asserting the resulting **IR event sequence** rather than bytes — fully offline, never hitting upstream.

12 packages, 123 test cases, `-race` clean. Core package coverage: `common` 94%, `obs` 94%, `llm` 89%, `adapter` 65%, `config` 58%, `app` 38%, `workbuddy` 55%.

---

## Known Limitations

**By design**

- **Upstream does not support non-streaming requests**: non-streaming responses are aggregated proxy-side, so TTFB matches streaming
- **Single process, single instance**: cooldown state lives in memory; multiple instances cool down independently and will hammer upstream
- **Quota query endpoint not implemented**: that upstream route requires enterprise privileges (403)
- **DSML text-mode tool-call fallback not implemented**: upstream currently uses native `tool_calls`; it must be added if upstream ever regresses
- **Reasoning is one-way**: the upstream only emits `reasoning_content` on responses and without a signature — Anthropic `thinking` blocks always carry `"signature": ""`, and assistant thinking is never replayed upstream (each turn re-reasons from scratch); revisit if the upstream ever validates signatures

**Other behavior**

- **Metrics persistence is on by default**: without a config file it writes `agent2api-metrics.json` to the working directory (contains usage stats). It is in `.gitignore`, but **exclude it manually when packaging**
- **"Add account" in the console needs a pool directory**: without one, credentials go to `~/.workbuddy`; configure `-accounts-dir` (or create `auths/`) and they join the pool automatically

---

## Roadmap

- [ ] Pool circuit breaking and quota queries
- [ ] A second platform adapter
- [ ] `cmd/probe` protocol drift detection

---

## Contributing

Issues and PRs are welcome — please read [CONTRIBUTING.md](CONTRIBUTING.md) first.

> [!IMPORTANT]
> **Never paste real credentials, tokens, or account nicknames** in an issue.

---

## License

**TBD** — the repository ships no LICENSE file, so all rights are reserved by default. Do not use commercially or redistribute until a license is chosen.

This is a personal learning and research project, for use only with your own authorized accounts on your own machine or a private environment, at your own risk. Full terms in [DISCLAIMER.md](DISCLAIMER.md).
