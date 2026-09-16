# Agent2API

A reverse-proxy gateway that translates AI agent platforms' private protocols into standard LLM APIs.

Currently implements the **WorkBuddy / CodeBuddy** platform, exposing three standard protocols so that any OpenAI/Anthropic-compatible client (Claude Code, Codex CLI, various SDKs and frontends) can connect with zero changes.

[简体中文](README.md) · **English**

---

> ## ⚠️ Read This First
>
> ### This is a **learning / research project**
>
> It exists to study and practice: protocol reverse-engineering, IR-layered architecture, SSE streaming protocol translation, and Go concurrency and timeout control.
>
> **It is not designed for production, nor intended to be run as a service for others.** The code prioritizes *explaining the mechanism* over *surviving production traffic*.
>
> The author **does not encourage or support** commercial use or offering it as a service. Please read the code for **learning purposes**.
>
> ### Usage boundaries
>
> - For **accounts you are authorized to use**, on **your own machine or a private deployment**, entirely **at your own risk**
> - It reads the **credential** your local desktop client has already logged in with (credential = your account; never share it)
> - Its operation **may conflict with** the upstream platform's Terms of Service
> - The upstream protocol is private and **may change without notice**, breaking this tool
> - The known-issues list is **not exhaustive** — see [Known Limitations](#known-limitations)
>
> Full terms: **[DISCLAIMER.md](DISCLAIMER.md)**.

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
- **Content sanitization** — prevents the client's fixed system template from being falsely flagged by upstream keyword moderation (required for Claude Code / Codex)
- **Dynamic model list** — fetched from upstream at runtime, never hardcoded
- **Built-in console** — embedded via `go:embed`, no frontend build step

---

## Relation to Similar Projects

Protocol details and architecture were informed by these open-source implementations:

| Project | Language | What was borrowed |
|---|---|---|
| [hawklithm/workbuddy2api](https://github.com/hawklithm/workbuddy2api) | Python | Most complete protocol notes: DSML parsing, sanitization wordlist, header set |
| [Sliverkiss/workbuddy2api](https://github.com/Sliverkiss/workbuddy2api) | Go | Engineering structure: dual-realm, pool / scheduler / session layering |
| [WncFht/devin2api](https://github.com/WncFht/devin2api) | Go | **The model for multi-platform IR layering** (adopted directly) |

**How this project differs:** all three downstream protocols implemented at once; `delta.reasoning_content` is handled (commonly missed, silently discarding reasoning); the model list is fetched dynamically rather than bundled (bundled lists are always stale); and protocol conversion is covered by **offline golden-frame replay tests**.

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

On startup the console URL and all endpoints are printed:

```
──────────────────────────────────────────────────────────────
  Agent2API console    http://127.0.0.1:8787/

  workbuddy · account <your-account> · <N> models
──────────────────────────────────────────────────────────────
  Endpoints
    POST  http://127.0.0.1:8787/v1/chat/completions   OpenAI Chat Completions
    POST  http://127.0.0.1:8787/v1/responses          OpenAI Responses
    POST  http://127.0.0.1:8787/v1/messages           Anthropic Messages
    GET   http://127.0.0.1:8787/v1/models             Model list
    GET   http://127.0.0.1:8787/health                Health check
──────────────────────────────────────────────────────────────
  Note  No API key configured — reachable by anyone on this machine
        Ctrl+C to stop
```

> When listening on `0.0.0.0` the banner shows `localhost`, which is the address that actually works.
> **Listening on `0.0.0.0` without `-api-key` lets anyone on your network spend your account's credits** (see [Security](#security)).

> If the WorkBuddy desktop client isn't installed on this machine, or you want a different account:
> ```bash
> ./bin/agent2api login      # device-code login; authorize in the browser
> ```

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

> Do **not** disable sanitization (`-no-sanitize`) when using Claude Code / Codex — their fixed system templates contain many security-policy phrases that upstream keyword moderation flags.

---

## Web Console

Embedded in the gateway — no separate deployment, no frontend build step (assets are compiled in via `go:embed`; charts are hand-drawn SVG).

Visit `http://127.0.0.1:8787` after starting. Six pages: **Overview** (status wall, request trend, token bars, per-protocol and per-model rankings, recent requests; auto-refreshes every 5s), **Chat** (streaming output, collapsible reasoning, tunable sampling params, code-block copy, stop generation), **Platforms**, **Models**, **Request Log** (last 200 requests, filterable), and **Settings** (hot-reload sanitization toggle, model-list refetch, effective config, connection instructions).

> **CDN note:** core functionality has zero CDN dependencies. Only **math rendering** (`$...$`) lazily loads KaTeX 0.16.11 (CSS + JS) from jsDelivr on first use; if it fails to load or you're offline, it degrades to plain text with **no other functionality affected**.

> **Screenshots:** the repo currently has none. Contributions welcome (see [CONTRIBUTING.md](CONTRIBUTING.md)) — for now, just start it and look.

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

Supported: text, reasoning (`reasoning_content` / `thinking` / reasoning summary), tool calls, multi-turn, system prompts, sampling parameters, image input.

---

## Configuration

Precedence: CLI flags > environment variables > config file > built-in defaults.

See `config.example.json` for a config-file example (passed via `-config`).

| Flag | Environment variable | Description |
|---|---|---|
| `-config` | — | Config file path (JSON) |
| `-port` | `AGENT2API_PORT` | Listen port, default 8787 |
| `-host` | `AGENT2API_HOST` | Listen address, default 127.0.0.1 |
| `-api-key` | `AGENT2API_API_KEY` | Gateway access key; empty disables auth |
| `-credential` | `AGENT2API_CREDENTIAL_PATH` | Credential file path; auto-detected when empty |
| `-base-url` | `AGENT2API_BASE_URL` | Upstream URL, default `https://copilot.tencent.com` |
| `-platform` | — | Upstream platform; only `workbuddy` for now |
| `-metrics-file` | — | Metrics persistence path |
| `-no-persist` | — | Disable metrics persistence (stats reset on restart) |
| `-no-sanitize` | — | Disable content sanitization |

Subcommands: `agent2api` (serve), `agent2api models` (list models), `agent2api login` (device-code login).

> **About sanitization:** upstream applies keyword-level content moderation. Claude Code / Codex's fixed system templates contain many compliance phrases (DoS, exploit, credential testing, …) that get falsely flagged. Sanitization is on by default and **should not be disabled** for those clients.

---

## Security

This project reads and writes **authentication credentials** on your machine:

1. **Credential source:** the gateway does **not** ask for your username or password. It reads the credential file the local WorkBuddy desktop client has **already logged in** with (`-credential` overrides the path). This is equivalent to lending the desktop client's session to a local process.
2. **The credential file *is* your account.** Never copy it to another machine and never commit it. The repo's `.gitignore` already excludes the relevant paths (`config.json`, `*.session.json`, `auths/`, `*.info`).
3. **Listens on `127.0.0.1` only by default.** ⚠️ If you change this to `0.0.0.0` **without setting `-api-key`**, anyone on your network can spend your account's credits. Shared or public deployments **must** set `-api-key` and enforce auth at a reverse proxy in front.
4. **The console does not send an API key** (known defect). The web console does not attach `-api-key` to `/api/*` requests, so **setting `-api-key` makes its Overview/Platforms/Models/Request-Log pages return 401**. Gateway API authentication itself works correctly.
5. **Logs and metrics** may record model names and token counts; `-no-persist` disables metrics persistence.

See [SECURITY.md](SECURITY.md) for vulnerability reporting.

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

**Key design point:** `api/*` and `adapter/*` never import each other — they communicate only through `llm`. This reduces "N platforms × M protocols" from N×M to N+M: adding a second platform requires no changes to the three downstream codecs.

The `Adapter` interface has only 3 methods (`Stream` / `ListModels` / `Name`) and `ResponseStream` only 1 (`Recv`), making test fakes extremely cheap.

Design notes are Chinese-only for now: [`docs/design/01-架构设计.md`](docs/design/01-架构设计.md), [`docs/research/02-WorkBuddy上游协议逆向.md`](docs/research/02-WorkBuddy上游协议逆向.md).

---

## Testing

```bash
make check        # go vet + go test
make cover        # coverage
go test ./... -race
```

Protocol conversion uses **golden-frame replay** testing: sanitized real upstream samples live in [`fixtures/`](fixtures/), are fed frame-by-frame to the parser, and assert on the resulting **IR event sequence** rather than bytes — so conversion can be fully verified offline without hitting upstream.

**Current coverage** (measured via `go test ./... -cover`):

| Package | Coverage |
|---|---|
| `internal/obs` | 93.4% |
| `internal/api/anthropic/messages` | 56.0% |
| `internal/api/openai/responses` | 49.2% |
| `internal/adapter/workbuddy` | 27.1% |
| `cmd/agent2api`, `internal/api/common`, `internal/api/openai/chat`, `internal/app`, `internal/config`, `internal/llm`, `internal/web` | **0.0%** |

The orchestration core (`internal/app`) and the Chat codec have **no tests** at all. Contributions welcome.

---

## Known Limitations

### By design

- **Upstream does not support non-streaming requests** — non-streaming responses are aggregated proxy-side, so time-to-first-byte matches streaming.
- **Single account** — no account pool or circuit breaking.
- **Quota/credit query endpoint not implemented** — requires enterprise privileges upstream (403).
- **DSML text-mode tool-call fallback not implemented** — upstream currently uses the native `tool_calls` channel.

### Known defects (unfixed)

Confirmed by code review and testing, ordered by impact:

1. **Chat Completions silently truncates on mid-stream failure** (most severe)
   `internal/api/openai/chat/chat.go` encodes `EventError` to **zero frames**. When upstream fails mid-stream, the client has already received HTTP 200 and then the content simply stops — **no `finish_reason`, no `[DONE]`, no error frame**. The client cannot distinguish "finished normally" from "upstream died."
2. **Anthropic streaming `input_tokens` is always 0**
   `message_start` in `internal/api/anthropic/messages/messages.go` hardcodes `"input_tokens": 0`; input tokens are never reported while streaming. Clients that track token usage see 0.
3. **Responses API `output` can drop items**
   `internal/api/openai/responses/responses.go` iterates `for i := 0; i < len(e.itemID); i++` over a sparse map keyed by content index. If a block is started but never ended (e.g. the stream dies mid-tool-call), that item silently vanishes from `output[]`.
4. **Tool descriptions are not sanitized**
   `SanitizeToolDescription` in `internal/adapter/workbuddy/sanitize.go` is implemented but **never called** — only system prompts are sanitized. Tool descriptions containing flagged terms can still be rejected upstream.
5. **No retry or backoff anywhere**
   The only retry is a single credential-refresh-and-redial on 401. Upstream 5xx / 429 failures go straight back to the client. (`isRetryableTransportError`, `parseRetryAfter`, and `Failure.RetryAfterSeconds` are currently **dead code**.)
6. **The console cannot send an API key**
   The frontend sends no auth header, so with `-api-key` set every `/api/*` call returns 401. See Security item 4.
7. **Test gaps**
   No tests for the orchestration core, the Chat codec, or config; 7 of 12 packages are at 0.0%.

### Other known behavior

- **Metrics persistence is on by default** — with no config file it writes `agent2api-metrics.json` to the **current working directory** (containing your usage stats). This is in `.gitignore`, but you **must exclude it manually when packaging (zip/tar)**.

---

## Roadmap

- [ ] Add tests for `internal/app`, `internal/api/openai/chat`, `internal/config`
- [ ] Fix the 7 known defects above
- [ ] Wire up retry/backoff (the helpers already exist)
- [ ] Second platform adapter (Devin / Cursor)
- [ ] `cmd/probe` protocol drift detection
- [ ] Multi-account pool + cooldown + circuit breaking

---

## Contributing

Issues and PRs are welcome — please read [CONTRIBUTING.md](CONTRIBUTING.md) first.

> ⚠️ **Never paste real credentials, tokens, or account names into an issue.**

---

## License

**TBD.**

This repository ships **no LICENSE file**, so all rights are reserved by default — until a license is chosen, please do not use it commercially or redistribute it.

Both this file and [README.md](README.md) will be updated once decided.

---

## Compliance

For **authorized accounts only**, on a **local or private deployment**, **at your own risk**. Read **[DISCLAIMER.md](DISCLAIMER.md)** before use. (The disclaimer's governing text is Chinese.)

Related: [SECURITY.md](SECURITY.md) · [CONTRIBUTING.md](CONTRIBUTING.md) · [CHANGELOG.md](CHANGELOG.md)
