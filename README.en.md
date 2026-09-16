# Agent2API

A reverse-proxy gateway that translates AI agent platforms' private protocols into standard LLM APIs.

Currently implements the **WorkBuddy / CodeBuddy** platform, exposing three standard protocols so that any OpenAI/Anthropic-compatible client (Claude Code, Codex CLI, various SDKs and frontends) can connect with zero changes.

[简体中文](README.md) · **English**

**Quick Start** · [Background](#background) · [Web Console](#web-console) · [Configuration](#configuration) · [Security](#security) · [Architecture](#architecture) · [Known Limitations](#known-limitations) · [Contributing](#contributing)

---

> [!WARNING]
> **Learning / research project, not production software.** For **accounts you are authorized to use**, on your own machine or a private deployment, at your own risk.
> It reads the **credential** your desktop client has already logged in with (credential = your account; never share it). Its operation **may conflict with the upstream Terms of Service**.
> The author does not encourage or support commercial use or offering it as a service. Full terms: **[DISCLAIMER.md](DISCLAIMER.md)**.

---

## Background

Clients like Claude Code and Codex CLI only speak the standard **OpenAI / Anthropic** protocols, while agent platforms' private upstreams expose no standard interface — so your platform credits can't be used from those clients.

This project puts a protocol translation layer on **your own machine**. It does not forge a protocol; it translates the three standard dialects into the upstream's private one.

```
Claude Code / Codex / any OpenAI client
        │  /v1/chat/completions · /v1/responses · /v1/messages
        ▼
   ┌──────────────────────────────────────┐
   │  Agent2API                           │
   │   downstream codecs ── IR ── adapter │
   └──────────────────────────────────────┘
        │  https://copilot.tencent.com/v2/chat/completions
        ▼
   WorkBuddy upstream
```

### Features

- **All three protocols** — OpenAI Chat Completions, OpenAI Responses, Anthropic Messages, each with streaming and non-streaming
- **Reasoning content** — maps the upstream's `delta.reasoning_content` to `reasoning_content` / `thinking` blocks / reasoning summaries
- **Tool calling** — native `tool_calls` channel with fragmented-argument reassembly
- **Credential reuse** — reads the desktop client's existing session; **no re-login**
- **Content sanitization** — prevents the client's fixed system template from being falsely flagged by upstream keyword moderation
- **Dynamic model list** — fetched from upstream at runtime, never hardcoded
- **Built-in console** — embedded via `go:embed`, no frontend build step, dark-mode aware

---

## Quick Start

```bash
# Option 1: install directly (requires Go 1.23+)
go install github.com/576469377/Agent2API/cmd/agent2api@latest

# Option 2: build from source
git clone https://github.com/576469377/Agent2API && cd Agent2API
make build                     # produces bin/agent2api

# Verify credentials (reuses the desktop client's session; no re-login)
agent2api models               # or ./bin/agent2api models

# Start (defaults to 127.0.0.1:8787)
agent2api                      # or ./bin/agent2api
```

No desktop client on this machine, or want a different account? Use `agent2api login` (device-code flow).

> [!WARNING]
> **Listening on `0.0.0.0` without `-api-key` lets anyone on your network spend your account's credits.** See [Security](#security).

### Calling the API

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

### Connecting Clients

**Claude Code:**

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8787
export ANTHROPIC_API_KEY=anything   # not verified when no gateway key is set
```

**Codex CLI:** point base_url / API key at `http://127.0.0.1:8787` and any value (uses `/v1/responses`).

**Any OpenAI SDK:** set base_url to `http://127.0.0.1:8787/v1`.

> [!TIP]
> Do **not** disable sanitization (`-no-sanitize`) for Claude Code / Codex: their fixed system templates contain many security-policy phrases that upstream keyword moderation falsely flags.

---

## Web Console

Embedded in the gateway — no separate deployment, no frontend build step (assets compiled in via `go:embed`; hand-drawn SVG charts). Visit `http://127.0.0.1:8787` after starting:

| Page | What it shows |
|---|---|
| **Overview** | Status wall, request trend, token bars, rankings, recent requests; auto-refresh |
| **Chat** | Streaming output, collapsible reasoning, tunable params, code copy; tables & math rendering |
| **Platforms** | Upstream URL, account, model count, health |
| **Models** | Model list with capability tags, filterable |
| **Request Log** | Last 200 requests, success/failure filter, failed rows highlighted |
| **Settings** | Sanitization toggle (hot reload), model-list refetch, effective config |

> [!NOTE]
> Zero CDN dependencies for core functionality. Only **math rendering** lazily loads KaTeX on first use and degrades to plain text offline. Screenshots are welcome — see [Contributing](#contributing).

---

## Supported Endpoints

| Endpoint | Protocol | Streaming | Non-streaming |
|---|:-:|:-:|:-:|
| `POST /v1/chat/completions` | OpenAI Chat Completions | ✅ | ✅ |
| `POST /v1/responses` | OpenAI Responses | ✅ | ✅ |
| `POST /v1/messages` | Anthropic Messages | ✅ | ✅ |
| `GET /v1/models` | OpenAI model list | — | ✅ |
| `GET /health` | Health check | — | ✅ |
| `GET /` | Web console | — | ✅ |

Supported: text, reasoning, tool calls, multi-turn, system prompts, sampling parameters, image input.

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
| `-base-url` | `AGENT2API_BASE_URL` | Upstream URL, default [copilot.tencent.com](https://copilot.tencent.com) |
| `-platform` | — | Upstream platform; only `workbuddy` for now |
| `-metrics-file` | — | Metrics persistence path |
| `-no-persist` | — | Disable metrics persistence (stats reset on restart) |
| `-no-sanitize` | — | Disable content sanitization |

Subcommands: `agent2api` (serve), `agent2api models` (list models), `agent2api login` (device-code login).

---

## Security

This project reads and writes **authentication credentials** on your machine:

1. **Credential source:** the gateway never asks for your username or password — it reads the credential file the local desktop client has **already logged in** with (`-credential` overrides the path). Equivalent to lending the desktop client's session to a local process.
2. **The credential file *is* your account.** Never copy it elsewhere; never commit it. `.gitignore` already excludes the relevant paths.
3. **Listens on `127.0.0.1` only by default.** Switching to `0.0.0.0` without `-api-key` lets anyone on your network spend your account's credits. Shared deployments must set `-api-key` and enforce auth at a reverse proxy.
4. **Logs and metrics** may record model names and token counts; `-no-persist` disables persistence.

Vulnerability reporting: [SECURITY.md](SECURITY.md).

---

## Architecture

```
internal/
├── llm/          IR layer (zero dependencies): request/response/error intermediate representation
├── adapter/      Upstream platform seam: the Adapter interface
│   └── workbuddy/  WorkBuddy implementation: auth / request / SSE / models / sanitization
├── api/          Downstream protocol codecs
│   ├── common/     SSE writing, error payloads (shared by all three)
│   ├── openai/chat/
│   ├── openai/responses/
│   └── anthropic/messages/
├── app/          HTTP routing and orchestration
├── obs/          Metrics collection and persistence
├── web/          Embedded console (go:embed)
└── config/       Configuration
```

**Key design point:** `api/*` and `adapter/*` never import each other — they communicate only through [`internal/llm`](internal/llm/). This reduces "N platforms × M protocols" from N×M to N+M: adding a second platform requires no changes to the three downstream codecs.

The `Adapter` interface has only 3 methods (`Stream` / `ListModels` / `Name`) and `ResponseStream` only 1 (`Recv`), making test fakes extremely cheap.

📖 Design notes are Chinese-only for now: [`docs/design/01-架构设计.md`](docs/design/01-架构设计.md) · [`docs/research/02-WorkBuddy上游协议逆向.md`](docs/research/02-WorkBuddy上游协议逆向.md).

---

## What This Project Teaches

In recommended reading order (all paths clickable):

| Topic | Code |
|---|---|
| **IR-layered architecture** — zero-dependency IR + tiny seam interfaces, N×M → N+M | [`internal/llm/`](internal/llm/) · [`adapter.go`](internal/adapter/adapter.go) |
| **Streaming protocol translation** — three SSE dialects ↔ private upstream; Anthropic block start/stop pairing is the classic trap | [`internal/api/`](internal/api/) · [`sse.go`](internal/adapter/workbuddy/sse.go) |
| **Golden-frame replay testing** — assert IR event sequences, not bytes; fully offline | [`fixtures/`](fixtures/) · [`sse_test.go`](internal/adapter/workbuddy/sse_test.go) |
| **Structured error classification** — classify once at the source, carry as a struct | [`failure.go`](internal/llm/failure.go) |
| **Multi-layer timeout control** — idle watchdog + total deadline + caller context; why `http.Client.Timeout` kills long streams | [`sse.go`](internal/adapter/workbuddy/sse.go) |
| **Batched SSE writes** — many events per frame, one Write + one Flush; post-commit error degradation | [`app.go`](internal/app/app.go) |
| **Reverse-engineering methodology** — evidence grading (🟢 verified / 🟡 inferred / ⚪ unconfirmed) | [`docs/research/`](docs/research/) |
| **Metrics & aggregation** — atomics + single lock; how to pick the TPS denominator | [`metrics.go`](internal/obs/metrics.go) |

The flip side matters too: [Known Limitations](#known-limitations) honestly records design trade-offs, with every defect fix (root cause + regression test) preserved in the CHANGELOG — the real gap between "runs" and "production-ready."

---

## Relation to Similar Projects

| Project | Language | What was borrowed |
|---|---|---|
| [hawklithm/workbuddy2api](https://github.com/hawklithm/workbuddy2api) | Python | Most complete protocol notes: DSML parsing, sanitization wordlist, header set |
| [Sliverkiss/workbuddy2api](https://github.com/Sliverkiss/workbuddy2api) | Go | Engineering structure: dual-realm, pool / scheduler / session layering |
| [WncFht/devin2api](https://github.com/WncFht/devin2api) | Go | **The model for multi-platform IR layering** (adopted directly) |

**How this project differs:** all three downstream protocols at once; `delta.reasoning_content` handled (commonly missed); model list fetched dynamically (bundled lists are always stale); offline golden-frame replay tests.

---

## Testing

```bash
make check        # go vet + go test
make cover        # coverage
go test ./... -race
```

Protocol conversion uses **golden-frame replay**: sanitized real upstream samples in [`fixtures/`](fixtures/) are fed frame-by-frame to the parser, asserting the **IR event sequence** rather than bytes — fully offline, never hitting upstream.

Current coverage (measured):

| Package | Coverage |
|---|---|
| [`internal/api/common`](internal/api/common/) | 94.3% |
| [`internal/obs`](internal/obs/) | 93.3% |
| [`internal/llm`](internal/llm/) | 88.6% |
| [`internal/config`](internal/config/) | 75.0% |
| [`internal/api/anthropic/messages`](internal/api/anthropic/messages/) | 56.4% |
| [`internal/api/openai/responses`](internal/api/openai/responses/) | 55.9% |
| [`internal/adapter/workbuddy`](internal/adapter/workbuddy/) | 55.2% |
| [`internal/api/openai/chat`](internal/api/openai/chat/) | 53.1% |
| [`internal/app`](internal/app/) | 50.1% |
| [`cmd/agent2api`](cmd/agent2api/) · [`internal/web`](internal/web/) | 0.0% (thin CLI shell / go:embed static assets) |

---

## Known Limitations

### By design

- **Upstream does not support non-streaming requests** — aggregated proxy-side, so TTFB matches streaming
- **Single account** — no account pool or circuit breaking
- **Quota query endpoint not implemented** — requires enterprise privileges upstream (403)
- **DSML text-mode tool-call fallback not implemented** — upstream currently uses native `tool_calls`

### Known defects (all 7 fixed on 2026-09-16)

These used to be open issues; each fix now ships with a regression test (see `CHANGELOG.md` and the test files):

1. ~~Chat Completions silently truncates on mid-stream failure~~ — `EventError` now emits an `{"error":{...}}` frame per the official SDK contract; no fake `finish_reason`/`[DONE]` afterwards.
2. ~~Anthropic streaming `input_tokens` is always 0~~ — `message_delta.usage` now carries the real value (official SDK treats it as a cumulative overwrite).
3. ~~Responses API `output` can drop items~~ — iterates by actual map keys; blocks started but never ended are synthesized as `incomplete` items.
4. ~~Tool descriptions are not sanitized~~ — `convertTools` now wires in `SanitizeToolDescription`.
5. ~~No retry or backoff anywhere~~ — up to 3 dial attempts with exponential backoff + Retry-After; at most one 401-refresh per attempt (loop guard); only transport errors / 5xx / 429 retry.
6. ~~The console cannot send an API key~~ — the frontend now attaches `X-Api-Key` everywhere, prompts on 401 and persists after verification; new unauthenticated `/api/auth-hint`; auth failures correctly return 401 (was 400).
7. ~~Test gaps~~ — `llm` 88.6%, `common` 94.3%, `config` 75%, `chat` 53.1%, `app` 50.1% (all were 0%).

### Other known behavior

- **Metrics persistence is on by default** — writes `agent2api-metrics.json` to the working directory (usage stats); in `.gitignore`, but **exclude it manually when packaging**.

---

## Roadmap

- [x] Fix the 7 known defects (2026-09-16, see above)
- [x] Wire up retry/backoff (dial backoff + Retry-After + 401-refresh guard)
- [ ] Second platform adapter (Devin / Cursor)
- [ ] `cmd/probe` protocol drift detection
- [ ] Multi-account pool + cooldown + circuit breaking

---

## Contributing

Issues and PRs are welcome — please read [CONTRIBUTING.md](CONTRIBUTING.md) first.

> [!IMPORTANT]
> **Never paste real credentials, tokens, or account names into an issue.**

---

## License

**TBD.** This repository ships **no LICENSE file**, so all rights are reserved by default — until a license is chosen, please do not use it commercially or redistribute it. Both this file and [README.md](README.md) will be updated once decided.

---

## Compliance

A **personal learning / research project**: authorized accounts only, local or private deployment, at your own risk. The author does not encourage or support commercial use or offering it as a service.

Full terms: **[DISCLAIMER.md](DISCLAIMER.md)** · Related: [SECURITY.md](SECURITY.md) · [CONTRIBUTING.md](CONTRIBUTING.md) · [CHANGELOG.md](CHANGELOG.md)
