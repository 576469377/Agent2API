import {
  escapeHTML as e,
  number as n,
  compact,
  duration,
  countdown,
  timestamp,
  icon,
  responseError,
  readEvents,
  markdown,
} from "./core.js";
import { renderMath } from "./math.js";
import { requestKey, accountKey, filterRequests, summarizeRequests, selectTrend, sortUsage } from "./monitor.js";
import { mergePricingSnapshot, modelPricing, compareModelRates } from "./pricing.js";

const $ = (id) => document.getElementById(id);
const saved = (key, fallback = "") => {
  try {
    return localStorage.getItem(key) ?? fallback;
  } catch {
    return fallback;
  }
};
const save = (key, value) => {
  try {
    localStorage.setItem(key, value);
  } catch {}
};
const s = {
  lang: saved("agent2api_lang", "zh"),
  theme: saved("agent2api_theme", "auto"),
  key: saved("agent2api_key"),
  page: "overview",
  cache: {},
  errors: {},
  busy: new Set(),
  authRequired: false,
  trend: "requests",
  usage: "by_model",
  trendMinutes: 30,
  refreshEvery: [0, 10, 30, 60].includes(Number(saved("agent2api_refresh", "10"))) ? Number(saved("agent2api_refresh", "10")) : 10,
  lastPoll: Date.now(),
  analytics: { dimension: "by_model", sort: "total", query: "" },
  connectionModel: "",
  connectionProtocol: "openai-chat",
  models: {
    query: "",
    platform: "all",
    capability: "all",
    pricing: "all",
    view: "list",
    page: 1,
    sort: "name",
  },
  accounts: { query: "", platform: "all", status: "all", view: saved("agent2api_account_view", "table") },
  requests: {
    query: "",
    platform: "all",
    status: "all",
    protocol: "all",
    page: 1,
    range: "all", account: "all", model: "all", sort: "time", size: 20,
  },
  chat: {
    model: saved("agent2api_chat_model"),
    draft: "",
    messages: [],
    controller: null,
    params: {
      temperature: 0.7,
      max_tokens: 4096,
      reasoning_effort: "",
      system: "",
    },
  },
};
const L = (zh, en) => (s.lang === "en" ? en : zh);
const names = {
  overview: () => L("网关概览", "Gateway overview"),
  requests: () => L("调用日志", "Request logs"),
  analytics: () => L("用量分析", "Usage analytics"),
  accounts: () => L("账号管理", "Accounts"),
  platforms: () => L("平台连接", "Platforms"),
  models: () => L("模型目录", "Model catalog"),
  chat: () => L("模型测试", "Playground"),
  connection: () => L("客户端接入", "Client setup"),
  settings: () => L("网关设置", "Settings"),
};
const descriptions = {
  overview: () =>
    L(
      "运行状态、调用趋势与平台可用性，一眼掌握。",
      "Your gateway health, traffic and platform availability at a glance.",
    ),
  requests: () =>
    L(
      "查看实际路由、用量与错误，定位每一次调用。",
      "Inspect actual routes, usage and errors for each request.",
    ),
  analytics: () => L("按模型、协议和账号分析累计用量，定位失败与高消耗来源。", "Explore cumulative usage by model, protocol and account."),
  connection: () => L("选择模型与协议，复制实际网关地址和请求示例。", "Choose a model and protocol, then copy gateway URLs and request examples."),
  accounts: () =>
    L(
      "统一管理各平台账号，查看调度状态、并发和实际用量。",
      "Manage platform accounts, scheduling, concurrency and recorded usage.",
    ),
  platforms: () =>
    L(
      "一个网关连接多个平台，同名模型按路由规则分配。",
      "Multiple platforms, one gateway. Shared model names follow your routing policy.",
    ),
  models: () =>
    L(
      "按来源与能力寻找模型，复制完整 ID 即可指定平台。",
      "Find models by source and capability. Copy a full ID to pin its platform.",
    ),
  chat: () =>
    L(
      "直接验证模型回复，支持流式输出与思考内容。",
      "Try a model with streaming responses and reasoning.",
    ),
  settings: () =>
    L(
      "连接地址、运行参数与各平台的兼容设置。",
      "Connection details, runtime parameters and platform settings.",
    ),
};
const eyebrows = {
  overview: "WORKSPACE OVERVIEW",
  requests: "REQUEST ACTIVITY",
  analytics: "USAGE ANALYTICS",
  connection: "CLIENT CONNECTIONS",
  accounts: "ACCOUNT MANAGEMENT",
  platforms: "CONNECTED PLATFORMS",
  models: "MODEL CATALOG",
  chat: "MODEL PLAYGROUND",
  settings: "GATEWAY SETTINGS",
};
const endpoints = {
  status: "/api/status",
  metrics: "/api/metrics",
  accounts: "/api/accounts/manage",
  models: "/api/models",
  platforms: "/api/platforms",
  config: "/api/config",
  matrix: "/api/accounts/models",
};
const pageKeys = {
  overview: ["status", "metrics", "accounts", "models"],
  requests: ["metrics"],
  analytics: ["metrics"],
  connection: ["config", "models"],
  accounts: ["accounts", "metrics"],
  platforms: ["platforms", "accounts", "models"],
  models: ["models"],
  chat: ["models"],
  settings: ["config"],
};
const platforms = () =>
  s.cache.status?.platforms ||
  s.cache.platforms?.active ||
  s.cache.models?.platforms ||
  [];
const models = () => s.cache.models?.models || [];
const accounts = () => s.cache.accounts?.accounts || [];
const platformName = (id) =>
  id === "workbuddy"
    ? L("WorkBuddy 国内版", "WorkBuddy · China")
    : id === "workbuddy-global"
      ? L("WorkBuddy 国际版", "WorkBuddy · Global")
      : platforms().find((p) => p.id === id)?.name ||
        id ||
        L("未记录平台", "Unrecorded platform");
const regionName = (id) =>
  id === "workbuddy"
    ? L("国内版", "China")
    : id === "workbuddy-global"
      ? L("国际版", "Global")
      : id === "(别名)" ? L("别名", "Alias") : id || "—";
const platformMark = (id) => id === "workbuddy" ? "CN" : id === "workbuddy-global" ? "GL" : String(id || "AP").replace(/[^a-z0-9]/gi, "").slice(0, 2).toUpperCase();
const regionTag = (id) =>
  `<span class="tag ${id === "workbuddy-global" ? "green" : "blue"}">${e(regionName(id))}</span>`;
const routeId = (model) =>
  model.routing_id ||
  (platforms().some((p) => p.id === model.platform) ? `${model.platform}/${model.id}` : model.id);
const gatewayURL = () => {
  const listen = s.cache.status?.listen || s.cache.config?.listen;
  if (!listen) return "";
  return `http://${listen.replace(/^0\.0\.0\.0:/, "127.0.0.1:").replace(/^\[::\]:/, "127.0.0.1:")}`;
};
const tag = (label, color = "", dot = false) =>
  `<span class="tag ${color}">${dot ? '<span class="status-dot"></span>' : ""}${e(label)}</span>`;
const button = (label, action, extra = "", glyph = "") =>
  `<button class="button ${extra}" data-action="${action}">${glyph ? icon(glyph) : ""}${e(label)}</button>`;
const iconButton = (glyph, label, action, attrs = "") =>
  `<button class="icon-button" aria-label="${e(label)}" title="${e(label)}" data-action="${action}" ${attrs}>${icon(glyph)}</button>`;
const empty = (title, description = "", glyph = "search") =>
  `<div class="empty-state">${icon(glyph)}<h3>${e(title)}</h3><p>${e(description)}</p></div>`;
const cardHead = (title, subtitle = "", right = "") =>
  `<div class="card-head"><div><h2>${e(title)}</h2>${subtitle ? `<p>${e(subtitle)}</p>` : ""}</div>${right}</div>`;
const detailItem = (title, value, cls = "") =>
  `<div><dt>${e(title)}</dt><dd class="${cls}">${e(value ?? "—")}</dd></div>`;
const search = (id, value, placeholder) =>
  `<label class="search">${icon("search")}<input id="${id}" type="search" value="${e(value)}" placeholder="${e(placeholder)}" aria-label="${e(placeholder)}"></label>`;
const options = (items, value) =>
  items
    .map(
      ([id, label]) =>
        `<option value="${e(id)}" ${id === value ? "selected" : ""}>${e(label)}</option>`,
    )
    .join("");
const platformOptions = (value) =>
  options(
    [
      ["all", L("全部平台", "All platforms")],
      ...platforms().map((p) => [p.id, platformName(p.id)]),
    ],
    value,
  );

function toast(text, error = false) {
  const element = document.createElement("div");
  element.className = `toast ${error ? "error" : ""}`;
  element.innerHTML = icon(error ? "info" : "check");
  element.append(document.createTextNode(text));
  $("toasts").append(element);
  setTimeout(() => element.remove(), 5000);
}
async function copy(value) {
  if (!value)
    return toast(
      L("连接地址尚未加载", "Connection details are not loaded"),
      true,
    );
  try {
    await navigator.clipboard.writeText(value);
    toast(L("已复制", "Copied"));
  } catch {
    toast(
      L(
        "无法访问剪贴板，请从详情中手动复制",
        "Clipboard unavailable. Copy the value from details.",
      ),
      true,
    );
  }
}
function authHeaders() {
  return s.key ? { Authorization: `Bearer ${s.key}` } : {};
}
async function api(path, body) {
  const response = await fetch(path, {
    method: body === undefined ? "GET" : "POST",
    headers: {
      ...authHeaders(),
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(30000),
    cache: "no-store",
  });
  if (!response.ok) {
    if (response.status === 401) s.authRequired = true;
    const error = new Error(await responseError(response));
    error.status = response.status;
    throw error;
  }
  return response.json();
}
async function refresh(keys = pageKeys[s.page], manual = false) {
  const needed = [...new Set(["status", ...keys])].filter(
    (key) => !s.busy.has(key),
  );
  if (!needed.length) return;
  s.lastPoll = Date.now();
  if (manual) $("refreshButton").classList.add("refreshing");
  needed.forEach((key) => s.busy.add(key));
  await Promise.allSettled(
    needed.map(async (key) => {
      try {
        const data = await api(endpoints[key]);
        if (data.error)
          throw new Error(
            typeof data.error === "string"
              ? data.error
              : JSON.stringify(data.error),
          );
        if (key === "models" && Array.isArray(data.models) && data.models.some((m) => !Object.hasOwn(m, "pricing"))) {
          // Optional metadata for a separate console on an older running gateway.
          // A missing snapshot remains unknown; generation is never probed.
          try {
            const snapshot = await fetch("/static/_model-pricing.json", { cache: "no-store", signal: AbortSignal.timeout(3000) });
            if (snapshot.ok) data.models = mergePricingSnapshot(data.models, await snapshot.json());
          } catch {}
        }
        s.cache[key] = data;
        delete s.errors[key];
        if (key === "status") {
          s.authRequired = false;
          s.updated = new Date();
        }
      } catch (error) {
        s.errors[key] = error;
      } finally {
        s.busy.delete(key);
      }
    }),
  );
  $("refreshButton").classList.remove("refreshing");
  chrome();
  draw();
}
function chrome() {
  document.documentElement.lang = s.lang === "en" ? "en" : "zh-CN";
  $("brandSubtitle").textContent = L("统一模型网关", "UNIFIED MODEL GATEWAY");
  $("navCaption").textContent = L("工作空间", "WORKSPACE");
  const navSignature = `${s.page}|${s.lang}|${models().length}|${accounts().length}`;
  if (chrome.navSignature !== navSignature)
    $("nav").innerHTML = [
      [L("运行监控", "MONITORING"), ["overview", "requests", "analytics"]],
      [L("资源管理", "RESOURCES"), ["accounts", "platforms", "models"]],
      [L("工具与配置", "TOOLS & SETTINGS"), ["chat", "connection", "settings"]],
    ].map(([title, pages]) => `<div class="nav-group-label">${e(title)}</div>${pages.map((page) => `<a class="nav-item ${page === s.page ? "active" : ""}" href="#${page}" ${page === s.page ? 'aria-current="page"' : ""}>${icon(page === "analytics" ? "activity" : page === "connection" ? "external" : page)}<span>${e(page === "overview" ? L("概览", "Overview") : names[page]())}</span>${["models", "accounts"].includes(page) && s.cache[page] ? `<span class="nav-count">${page === "models" ? models().length : accounts().length}</span>` : ""}</a>`).join("")}`).join("");
  chrome.navSignature = navSignature;
  $("sidebarShortcut").innerHTML =
    icon("bolt") + e(L("快速测试模型", "Try a model")) + icon("arrow");
  $("menuButton").innerHTML = icon("menu");
  $("menuButton").setAttribute("aria-label", L("展开导航", "Open navigation"));
  $("copyEndpoint").innerHTML =
    icon("copy") + e(L("复制 API 地址", "Copy API URL"));
  $("copyEndpoint").disabled = !gatewayURL();
  $("breadcrumb").textContent = names[s.page]();
  $("pageTitle").textContent = names[s.page]();
  $("pageDescription").textContent = descriptions[s.page]();
  $("pageEyebrow").textContent = eyebrows[s.page];
  $("refreshButton").innerHTML = icon("refresh") + e(L("刷新", "Refresh"));
  $("quickSearch").innerHTML = icon("search") + `<span>${e(L("搜索 / 快捷跳转", "Search / jump to"))}</span><kbd>Ctrl K</kbd>`;
  $("quickSearch").setAttribute("aria-label", L("搜索与快捷跳转", "Search and navigation"));
  $("autoRefresh").innerHTML = options([["0", L("手动刷新", "Manual refresh")], ["10", L("每 10 秒刷新", "Every 10s")], ["30", L("每 30 秒刷新", "Every 30s")], ["60", L("每 60 秒刷新", "Every 60s")]], String(s.refreshEvery));
  $("autoRefresh").setAttribute("aria-label", L("自动刷新间隔", "Auto-refresh interval"));
  $("footerText").textContent = L(
    "一个入口，连接全部平台。",
    "One gateway. Every platform.",
  );
  $("footerEndpoint").textContent = s.cache.status?.listen || "";
  if (chrome.language !== s.lang)
    $("themeSel").innerHTML = options(
      [
        ["auto", L("跟随系统", "System")],
        ["light", L("浅色", "Light")],
        ["dark", L("深色", "Dark")],
      ],
      s.theme,
    );
  chrome.language = s.lang;
  $("themeSel").value = s.theme;
  $("langSel").value = s.lang;
  const connected = !!s.cache.status && !s.errors.status;
  $("healthDot").className = `status-dot ${connected ? "online" : "offline"}`;
  $("healthText").textContent = connected
    ? L("网关运行中", "Gateway online")
    : s.authRequired
      ? L("需要 API Key", "API key required")
      : L("连接不可用", "Disconnected");
  $("version").textContent = s.cache.status ? `v${s.cache.status.version}` : "";
  $("connectionBadge").className =
    `connection-badge ${connected ? "online" : "offline"}`;
  $("connectionBadge").textContent = connected
    ? L("反代服务运行中", "Gateway online")
    : L("连接需要检查", "Connection needs attention");
  $("updatedAt").textContent = s.updated
    ? `${L("更新于", "Updated")} ${timestamp(s.updated)}`
    : "";
  const pageError = [
    ...new Set([
      "status",
      ...pageKeys[s.page],
      ...(s.page === "models" && s.models.view === "matrix" ? ["matrix"] : []),
    ]),
  ].find((key) => s.errors[key]);
  $("errorBanner").hidden = !pageError;
  if (pageError)
    $("errorBanner").innerHTML =
      `<span>${e(s.authRequired ? L("网关需要有效的 API Key，请配置控制台连接。", "Enter a valid API key to connect to the gateway.") : s.errors[pageError].message)} ${s.cache[pageError] ? e(L("当前保留上次成功的数据。", "Showing the last successful snapshot.")) : ""}</span>${s.authRequired ? button(L("配置连接", "Configure connection"), "auth", "small") : button(L("重试", "Retry"), "retry", "small")}`;
}
function navigate() {
  const page = location.hash.slice(1);
  s.page = Object.hasOwn(names, page) ? page : "overview";
  $("sidebar").classList.remove("open");
  $("menuButton").setAttribute("aria-expanded", "false");
  chrome();
  draw(true);
  document.documentElement.scrollTop = 0;
  document.body.scrollTop = 0;
  if (s.page === "models" && s.models.view === "matrix")
    refresh([...pageKeys.models, "matrix"]);
  else refresh(pageKeys[s.page]);
}
function draw(force = false) {
  if (s.page === "chat" && !force && $("chatMessages")) {
    updateChatModels();
    return;
  }
  // Background snapshots preserve search focus, selection and horizontal table scroll.
  const active = document.activeElement,
    focus = active?.id;
  const selection =
    active instanceof HTMLInputElement
      ? [active.selectionStart, active.selectionEnd]
      : null;
  const scroll = $("pageContent").querySelector(".table-wrap")?.scrollLeft || 0;
  const primary = pageKeys[s.page][0];
  if (!s.cache[primary] && !s.errors[primary])
    $("pageContent").innerHTML =
      `<div class="loading-state"><span class="spinner"></span>${e(L("正在加载…", "Loading…"))}</div>`;
  else if (!s.cache[primary] && s.errors[primary])
    $("pageContent").innerHTML = empty(
      L("暂时无法加载", "Unable to load"),
      L(
        "检查连接后点击刷新，已有反代请求不受页面影响。",
        "Check the connection and refresh. Existing gateway requests are unaffected.",
      ),
      "info",
    );
  else $("pageContent").innerHTML = views[s.page]();
  if (focus && $(focus)) {
    $(focus).focus({ preventScroll: true });
    if (selection && selection[0] != null)
      $(focus).setSelectionRange(...selection);
  }
  if (s.page === "models") updateModelTable();
  if (s.page === "requests") updateRequestTable();
  if (s.page === "accounts") updateAccountCards();
  if (s.page === "analytics") updateAnalyticsTable();
  if (s.page === "chat") {
    updateChatModels();
    renderMessages();
  }
  const table = $("pageContent").querySelector(".table-wrap");
  if (table) table.scrollLeft = scroll;
}

function metricCard(label, glyph, value, note, unit = "") {
  return `<article class="card stat-card"><div class="stat-heading">${icon(glyph)}${e(label)}</div><div class="stat-value">${e(value)}${unit ? `<small>${e(unit)}</small>` : ""}</div><div class="stat-note">${note}</div></article>`;
}
function chart(series, kind = "requests") {
  const values = selectTrend(series, s.trendMinutes);
  if (!values.length)
    return empty(
      L("尚无趋势数据", "No trend data"),
      L(
        "调用产生后，这里显示最近 30 分钟的数据。",
        "Traffic from the last 30 minutes will appear here.",
      ),
      "activity",
    );
  const a = values.map((row) =>
    kind === "requests" ? row.ok : row.input_tokens,
  );
  const b = values.map((row) =>
    kind === "requests" ? row.failed : row.output_tokens,
  );
  const max = Math.max(1, ...a, ...b),
    scale = max <= 5 ? Math.ceil(max) : Math.ceil(max / 5) * 5;
  const x = (index) => 36 + (index * 552) / Math.max(1, values.length - 1),
    y = (value) => 161 - (value / scale) * 135;
  const line = (rows) =>
    rows
      .map(
        (value, index) =>
          `${index ? "L" : "M"}${x(index).toFixed(1)},${y(value).toFixed(1)}`,
      )
      .join(" ");
  const axis = [0, 0.5, 1]
    .map(
      (ratio) =>
        `<line class="chart-grid" x1="36" x2="588" y1="${y(scale * ratio)}" y2="${y(scale * ratio)}"/><text x="27" y="${y(scale * ratio) + 3}" text-anchor="end">${e(compact(scale * ratio))}</text>`,
    )
    .join("");
  const times = [0, Math.floor((values.length - 1) / 2), values.length - 1]
    .map(
      (index) =>
        `<text x="${x(index)}" y="188" text-anchor="${index === 0 ? "start" : index === values.length - 1 ? "end" : "middle"}">${e(new Date(values[index].minute * 60000).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hour12: false }))}</text>`,
    )
    .join("");
  const points = values
    .map(
      (row, index) =>
        `<circle class="chart-point" cx="${x(index)}" cy="${y(a[index])}" r="2" fill="var(--accent)"><title>${e(timestamp(row.minute * 60000))} · ${e(kind === "requests" ? L("成功 / 失败", "Success / Failed") : L("输入 / 输出 Token", "Input / output tokens"))}: ${a[index]} / ${b[index]}</title></circle>`,
    )
    .join("");
  const width = 552 / Math.max(1, values.length - 1);
  const hits = values.map((row, index) => `<rect class="chart-hit" tabindex="0" role="button" aria-label="${e(`${timestamp(row.minute * 60000)} · ${a[index]} / ${b[index]}`)}" data-chart-point="${index}" data-x="${x(index)}" data-time="${e(timestamp(row.minute * 60000))}" data-a="${a[index]}" data-b="${b[index]}" data-kind="${kind}" x="${Math.max(36, x(index) - width / 2)}" y="20" width="${Math.min(width, 588 - Math.max(36, x(index) - width / 2))}" height="148"/>`).join("");
  return `<div class="chart-shell"><svg class="chart" viewBox="0 0 610 200" aria-label="${e(L(`最近 ${s.trendMinutes} 分钟趋势`, `Last ${s.trendMinutes} minutes`))}">${axis}<path class="chart-area" d="${line(a)} L588,161 L36,161 Z"/><path class="chart-line" d="${line(a)}"/><path class="chart-line red" d="${line(b)}"/>${points}${times}<line class="chart-crosshair" x1="36" x2="36" y1="20" y2="161" hidden/>${hits}</svg><div class="chart-tooltip" hidden role="status"></div></div>`;
}
function platformSummary() {
  return (
    platforms()
      .map((p) => {
        const pool = accounts().filter((a) => a.platform === p.id),
          ready = pool.filter((a) => a.healthy).length;
        return `<a class="platform-summary" href="#platforms"><div class="platform-avatar ${p.id === "workbuddy-global" ? "global" : ""}">${e(platformMark(p.id))}</div><div class="platform-label"><strong>${e(platformName(p.id))}</strong><small>${n(models().filter((m) => m.platform === p.id).length || p.models || 0)} ${e(L("个模型", "models"))} · ${ready}/${pool.length} ${e(L("账号就绪", "accounts ready"))}</small></div>${tag(pool.length ? (ready ? L("可调度", "Ready") : L("需关注", "Attention")) : L("待加载", "Loading"), ready ? "green" : "amber", true)}</a>`;
      })
      .join("") || empty(L("暂无平台", "No platforms"))
  );
}
function ranking(rows) {
  if (!rows?.length)
    return `<p class="ranking-empty">${e(L("开始调用后显示模型用量。", "Model usage appears after the first request."))}</p>`;
  const top = [...rows].sort((a, b) => b.total - a.total).slice(0, 4),
    max = Math.max(...top.map((row) => row.total), 1);
  return `<div class="mini-ranking">${top.map((row) => `<div class="ranking-row"><span class="truncate mono" title="${e(row.key)}">${e(row.key)}</span><div class="load-track"><span style="width:${(row.total / max) * 100}%"></span></div><span class="numeric">${n(row.total)}</span></div>`).join("")}</div>`;
}
function overview() {
  const m = s.cache.metrics || {},
    status = s.cache.status || {},
    ready = accounts().filter((a) => a.healthy).length;
  const currentRPM = m.series?.at(-1)?.total ?? m.last_minute_rpm;
  const recent = [...(m.recent || [])]
    .sort((a, b) => new Date(b.time) - new Date(a.time))
    .slice(0, 4);
  const sample = summarizeRequests(m.recent || []);
  const attention = accounts().filter((a) => !a.healthy && a.state !== "disabled");
  const failures = recentRequests().filter((row) => !row.ok).slice(0, 3);
  const enabled = accounts().filter((a) => a.state !== "disabled");
  const capacity = !enabled.length ? "—" : enabled.some((a) => !a.max_concurrency) ? L("不限", "Unlimited") : enabled.reduce((sum, a) => sum + a.max_concurrency, 0);
  return `<div class="runtime-strip"><span class="runtime-live"><span class="status-dot ${s.errors.status ? "offline" : "online"}"></span>${e(s.errors.status ? L("连接异常", "Connection issue") : L("网关在线", "Gateway online"))}</span><span>${e(L("实时并发", "In flight"))}<strong>${n(m.in_flight)}</strong></span><span>${e(L("本分钟调用", "Current minute"))}<strong>${n(currentRPM)}</strong></span><span>${e(L("账号就绪", "Ready accounts"))}<strong>${ready}/${accounts().length}</strong></span><span>${e(L("运行时间", "Uptime"))}<strong>${e(countdown(status.uptime_sec || 0))}</strong></span><a href="#connection">${icon("external")}${e(L("接入客户端", "Connect a client"))}</a></div>
  <div class="section-heading"><h2>${e(L("累计用量", "Cumulative usage"))}</h2><span>${e(L("累计指标可跨网关重启保留，趋势按最近分钟展示", "Persisted totals · recent minute-based trends"))}</span></div>
  <div class="stats-grid operations-stats">
    ${metricCard(L("累计调用", "Total requests"), "activity", n(m.total), `${e(L("当前分钟已完成", "Completed this minute"))} <strong>${n(currentRPM)}</strong>`)}
    ${metricCard(L("调用成功率", "Success rate"), "check", m.total ? `${(m.success_rate * 100).toFixed(1)}%` : "—", `<span class="positive">${n(m.ok)} ${e(L("成功", "successful"))}</span><span>·</span><span>${n(m.failed)} ${e(L("失败", "failed"))}</span>`)}
    ${metricCard(L("平均响应耗时", "Average duration"), "clock", m.total ? (m.avg_latency_ms / 1000).toFixed(2) : "—", e(L("包含连接与完整生成时间", "Includes connection and full generation")), m.total ? "s" : "")}
    ${metricCard(L("输入 Token", "Input tokens"), "tokens", compact(m.input_tokens), `<span title="${n(m.input_tokens)}">${n(m.input_tokens)} tokens</span>`)}
    ${metricCard(L("输出 Token", "Output tokens"), "tokens", compact(m.output_tokens), `${e(L("思考 Token", "Reasoning tokens"))} ${n(m.reasoning_tokens)}`)}
    ${metricCard(L("平均生成速度", "Generation speed"), "bolt", m.tps_samples ? Number(m.avg_tps).toFixed(1) : "—", `${n(m.tps_samples)} ${e(L("个有效测速样本", "measured samples"))}`, m.tps_samples ? "tok/s" : "")}
  </div>
  <div class="overview-grid"><section class="card">${cardHead(
    L("流量趋势", "Traffic activity"),
    L(`最近 ${s.trendMinutes} 分钟 · 每分钟聚合`, `Last ${s.trendMinutes} minutes · per-minute buckets`),
    `<div class="chart-caption"><span><i class="legend-dot"></i>${e(s.trend === "requests" ? L("成功", "Success") : L("输入", "Input"))}</span><span><i class="legend-dot red"></i>${e(s.trend === "requests" ? L("失败", "Failed") : L("输出", "Output"))}</span><select id="trendSelect" class="select" aria-label="${e(L("趋势类型", "Trend type"))}">${options(
      [
        ["requests", L("调用", "Requests")],
        ["tokens", "Token"],
      ],
      s.trend,
    )}</select></div>`,
  )}<div class="chart-controls"><div class="segmented">${[15, 30].map((minutes) => `<button data-action="trend-range" data-value="${minutes}" aria-pressed="${s.trendMinutes === minutes}" class="${s.trendMinutes === minutes ? "active" : ""}">${minutes} ${e(L("分钟", "min"))}</button>`).join("")}</div><span>${e(L("悬停或聚焦查看每分钟读数", "Hover or focus for minute details"))}</span></div><div class="card-body">${chart(m.series, s.trend)}</div><div class="summary-metrics"><div><small>${e(L("记录样本", "Sample size"))}</small><strong>${n(sample.total)}</strong><small>${e(L("最近最多 200 条", "Latest 200 at most"))}</small></div><div><small>${e(L("完整耗时 P50", "Duration P50"))}</small><strong>${e(duration(sample.p50))}</strong></div><div><small>${e(L("完整耗时 P95", "Duration P95"))}</small><strong>${e(duration(sample.p95))}</strong></div></div></section>
  <section class="card">${cardHead(L("平台与账号", "Platforms & accounts"), L("统一入口，按平台独立调度", "One endpoint, independent platform pools"), `<a class="text-link" href="#platforms">${e(L("管理", "Manage"))} →</a>`)}<div class="card-body">${platformSummary()}<div class="subtle-strip"><span>${e(L("平台", "Platforms"))} <strong>${platforms().length}</strong></span><span>${e(L("模型条目", "Model entries"))} <strong>${models().length}</strong></span><span>${e(L("账号", "Accounts"))} <strong>${accounts().length}</strong></span></div></div>${cardHead(
    L("调用分布", "Usage breakdown"),
    L("累计调用次数", "Cumulative requests"),
    `<select id="usageSelect" class="select" aria-label="${e(L("统计维度", "Usage dimension"))}">${options(
      [
        ["by_model", L("模型", "Model")],
        ["by_protocol", L("协议", "Protocol")],
        ["by_account", L("账号", "Account")],
      ],
      s.usage,
    )}</select>`,
  )}<div class="card-body">${ranking(s.usage === "by_protocol" ? (m.by_protocol || []).map((row) => ({ ...row, key: protocolName(row.key) })) : m[s.usage])}</div></section></div>
  <section class="card operations-attention">${cardHead(L("运行诊断", "Operational checks"), L("账号调度与最近记录中的失败", "Account scheduling and failures in the recent sample"), `<button class="button small" data-action="inspect-failures">${e(L("查看失败调用", "Inspect failures"))}${icon("arrow")}</button>`)}<div class="diagnostic-grid"><div class="diagnostic-summary"><span class="diagnostic-icon ${attention.length ? "negative" : "positive"}">${icon(attention.length ? "info" : "check")}</span><div><strong>${e(attention.length ? L(`${attention.length} 个账号需要关注`, `${attention.length} accounts need attention`) : enabled.length ? L("全部启用账号可调度", "Enabled accounts are ready") : L("没有已启用账号", "No enabled accounts"))}</strong><p>${e(L(`账号槽位 ${capacity} · ${failures.length ? "最近记录存在失败调用" : "最近记录未发现失败"}`, `Account slots ${capacity} · ${failures.length ? "Recent failures found" : "No failures in recent records"}`))}</p></div></div><div class="diagnostic-items">${attention.slice(0, 2).map((a) => `<button data-action="account-detail" data-index="${accounts().indexOf(a)}">${regionTag(a.platform)}<span>${e(a.identity?.nickname || a.label)}</span>${tag(...accountStatus(a))}${icon("chevron")}</button>`).join("")}${failures.slice(0, 2).map((row) => `<button data-action="request-detail" data-key="${e(requestKey(row))}">${tag(String(row.status), "red")}<span class="truncate">${e(row.model)} · ${e(row.error || L("调用失败", "Request failed"))}</span>${icon("chevron")}</button>`).join("")}${!attention.length && !failures.length ? `<a href="#models">${icon("models")}${e(L("查看模型与账号可用性", "Review model availability"))}${icon("arrow")}</a>` : ""}</div></div></section>
  <section class="card">${cardHead(L("最近调用", "Recent requests"), L("实际路由、账号与完整耗时", "Actual route, account and full duration"), `<a class="text-link" href="#requests">${e(L("查看全部", "View all"))} →</a>`)}${requestTable(recent, false)}</section>`;
}

function platformTabs(kind, selected) {
  const all = kind === "models" ? models() : accounts();
  return `<div class="segmented" aria-label="${e(L("平台筛选", "Platform filter"))}">${[["all", L("全部平台", "All platforms")], ...platforms().map((p) => [p.id, regionName(p.id)])].map(([id, label]) => `<button data-action="${kind}-platform" data-value="${e(id)}" class="${selected === id ? "active" : ""}" aria-pressed="${selected === id}">${e(label)}<span class="count">${id === "all" ? all.length : all.filter((item) => item.platform === id).length}</span></button>`).join("")}</div>`;
}
function analyticsPage() {
  const m = s.cache.metrics || {};
  return `<div class="stats-grid">${metricCard(L("累计调用", "Requests"), "activity", n(m.total), e(L("含已持久化记录", "Includes persisted counts")))}${metricCard(L("累计 Token", "Total tokens"), "tokens", compact((m.input_tokens || 0) + (m.output_tokens || 0)), `${n(m.input_tokens)} ${e(L("输入", "input"))} / ${n(m.output_tokens)} ${e(L("输出", "output"))}`)}${metricCard(L("累计失败", "Failed requests"), "info", n(m.failed), m.total ? `${((m.failed / m.total) * 100).toFixed(2)}% ${e(L("失败率", "failure rate"))}` : "—")}${metricCard(L("平均完整耗时", "Average duration"), "clock", e(duration(m.total ? m.avg_latency_ms : null)), e(L("包含连接与完整生成", "Connection and generation included")))}</div>
  <section class="card analytics-card">${cardHead(L("用量分布", "Usage distribution"), L("累计统计，不受最近 200 条记录的限制", "Cumulative aggregates, beyond the 200-record sample"), `<div class="segmented">${[["by_model", L("模型", "Model")], ["by_protocol", L("协议", "Protocol")], ["by_account", L("账号", "Account")]].map(([key, name]) => `<button data-action="analytics-dimension" data-value="${key}" class="${s.analytics.dimension === key ? "active" : ""}" aria-pressed="${s.analytics.dimension === key}">${e(name)}</button>`).join("")}</div>`)}<div id="analyticsDistribution"></div></section>
  <div class="toolbar"><div class="toolbar-group">${search("analyticsSearch", s.analytics.query, L("搜索统计项…", "Search usage groups…"))}<select class="select" id="analyticsSort" aria-label="${e(L("用量排序", "Usage sort"))}">${options([["total", L("调用量优先", "Most requests")], ["tokens", L("Token 用量优先", "Most tokens")], ["failed", L("失败量优先", "Most failures")], ["avg_latency_ms", L("耗时优先", "Longest duration")]], s.analytics.sort)}</select></div>${button(L("导出统计", "Export usage"), "export-usage", "", "requests")}</div><section class="card" id="analyticsResults"></section><p class="scope-note">${e(L("统计按网关原始模型名与账号标识聚合；同名跨平台项可能合并。点击查看记录可进一步按平台筛选，记录页只保留最近 200 条。", "Aggregates use original model names and account labels; identical names across platforms can be combined. Request drill-down retains the latest 200 records."))}</p>`;
}
function usageRows() {
  const q = s.analytics.query.trim().toLowerCase();
  return sortUsage(s.cache.metrics?.[s.analytics.dimension] || [], s.analytics.sort).filter((row) => `${row.key} ${s.analytics.dimension === "by_protocol" ? protocolName(row.key) : ""}`.toLowerCase().includes(q));
}
function updateAnalyticsTable() {
  if (!$("analyticsResults")) return;
  const all = s.cache.metrics?.[s.analytics.dimension] || [], rows = usageRows();
  const total = all.reduce((sum, row) => sum + row.total, 0);
  const colors = ["#6384f4", "#37b7a5", "#a087ee", "#e5ae56", "#dc798e", "#7387a3"];
  const top = sortUsage(all).slice(0, 5), other = total - top.reduce((sum, row) => sum + row.total, 0);
  const parts = [...top.map((row) => row.total), ...(other > 0 ? [other] : [])];
  let offset = 0;
  const gradient = parts.map((count, index) => { const start = offset; offset += count / total * 100; return `${colors[index]} ${start}% ${offset}%`; }).join(",");
  const name = (row) => s.analytics.dimension === "by_protocol" ? protocolName(row.key) : row.key || L("未记录", "Unrecorded");
  $("analyticsDistribution").innerHTML = total ? `<div class="distribution-layout"><div class="usage-donut" style="--distribution:conic-gradient(${gradient})" role="img" aria-label="${e(L("累计调用占比分布", "Cumulative request distribution"))}"><div><span>${e(L("累计调用", "Requests"))}</span><strong>${compact(total)}</strong></div></div><div class="distribution-legend">${top.map((row, i) => `<button data-action="usage-requests" data-value="${e(row.key)}" data-kind="${s.analytics.dimension}"><i style="background:${colors[i]}"></i><span class="truncate mono" title="${e(name(row))}">${e(name(row))}</span><strong>${n(row.total)}</strong><span>${((row.total / total) * 100).toFixed(1)}%</span>${icon("chevron")}</button>`).join("")}${other ? `<div class="legend-other"><i style="background:${colors[5]}"></i><span>${e(L("其他", "Other"))}</span><strong>${n(other)}</strong><span>${((other / total) * 100).toFixed(1)}%</span></div>` : ""}</div></div>` : empty(L("尚无统计数据", "No usage data"), L("请求完成后显示真实用量。", "Completed requests populate usage."), "activity");
  $("analyticsResults").innerHTML = rows.length ? `<div class="table-wrap"><table><thead><tr><th>${e(L("统计项", "Group"))}</th><th class="num">${e(L("调用次数", "Requests"))}</th><th class="num">${e(L("成功率", "Success rate"))}</th><th class="num">${e(L("平均耗时", "Avg duration"))}</th><th class="num">${e(L("输入 Token", "Input tokens"))}</th><th class="num">${e(L("输出 Token", "Output tokens"))}</th><th class="num">${e(L("失败", "Failed"))}</th><th></th></tr></thead><tbody>${rows.map((row) => `<tr><td class="usage-label mono" title="${e(name(row))}">${e(name(row))}<div class="usage-bar"><i style="width:${total ? row.total / total * 100 : 0}%"></i></div></td><td class="num numeric">${n(row.total)}</td><td class="num"><span class="${row.failed ? "negative" : "positive"}">${row.total ? (row.success_rate * 100).toFixed(1) + "%" : "—"}</span></td><td class="num mono">${e(duration(row.total ? row.avg_latency_ms : null))}</td><td class="num numeric">${n(row.input_tokens)}</td><td class="num numeric">${n(row.output_tokens)}</td><td class="num numeric ${row.failed ? "negative" : "muted"}">${n(row.failed)}</td><td><button class="button small" data-action="usage-requests" data-kind="${s.analytics.dimension}" data-value="${e(row.key)}">${e(L("记录", "Logs"))}${icon("arrow")}</button></td></tr>`).join("")}</tbody></table></div><div class="table-footer">${rows.length} ${e(L("个统计项", "groups"))} · ${e(L("累计统计", "Cumulative aggregates"))}</div>` : empty(L("没有匹配的统计项", "No matching groups"));
}
function connectionPage() {
  const selected = models().find((m) => routeId(m) === s.connectionModel) || models().find((m) => m.routing_id) || models()[0];
  s.connectionModel = selected ? routeId(selected) : "";
  const origin = gatewayURL(), protocol = s.connectionProtocol;
  const url = protocol === "anthropic-messages" ? origin : `${origin}/v1`;
  return `<div class="connection-layout"><section class="card">${cardHead(L("连接配置", "Connection configuration"), L("使用网关地址，支持同一入口接入多个平台", "One gateway endpoint for all connected platforms"))}<div class="card-body"><div class="protocol-choices">${[["openai-chat", "Chat Completions", L("OpenAI 兼容客户端", "OpenAI-compatible clients")], ["openai-responses", "Responses", L("Responses 兼容客户端", "Responses-compatible clients")], ["anthropic-messages", "Anthropic Messages", "Claude Code / ZCode"]].map(([id, name, note]) => `<button data-action="connection-protocol" data-value="${id}" class="${protocol === id ? "active" : ""}" aria-pressed="${protocol === id}"><span>${e(name)}</span><small>${e(note)}</small>${protocol === id ? icon("check") : ""}</button>`).join("")}</div><label class="field"><span>${e(L("使用模型", "Model"))}</span><select class="select" id="connectionModel">${models().length ? options(models().map((m) => [routeId(m), routeId(m)]), s.connectionModel) : '<option>—</option>'}</select></label><div class="field"><span>Base URL</span><div class="endpoint-box"><code>${e(origin ? url : "—")}</code><button class="icon-button" data-action="copy-connection-url" aria-label="${e(L("复制接入地址", "Copy connection URL"))}">${icon("copy")}</button></div></div>${protocol === "anthropic-messages" ? `<p class="scope-note">${e(L("Claude Code 使用上方根地址；ZCode 的 Anthropic 供应商 Base URL 填写根地址 + /v1。", "Claude Code uses the origin above. ZCode's Anthropic provider uses the origin plus /v1."))}</p>` : ""}<dl class="details-list">${detailItem(L("模型路由", "Model route"), s.connectionModel || "—", "mono")}${detailItem(L("鉴权", "Authentication"), s.cache.config?.auth_enabled ? L("填写你配置的网关 API Key", "Use your configured gateway API key") : L("未启用校验；客户端必填时使用 agent2api-local", "Not enabled; use agent2api-local if required"))}</dl><div class="detail-actions"><button class="button primary" data-action="try-model" data-model="${e(s.connectionModel)}" ${!selected ? "disabled" : ""}>${icon("chat")}${e(L("测试所选模型", "Try selected model"))}</button>${button(L("查看路由策略", "Routing policy"), "open-platforms", "")}</div></div></section><section class="card connection-example">${cardHead(L("请求示例", "Request example"), L("适用于 macOS / Linux shell", "For macOS / Linux shells"), button(L("复制示例", "Copy example"), "copy-connection-example", "small", "copy"))}<div class="card-body"><pre><code>${e(connectionExample())}</code></pre><p class="scope-note">${e(L("启用鉴权时，将 YOUR_GATEWAY_KEY 换成你配置的网关密钥。示例不读取或展示浏览器保存的密钥。", "When authentication is enabled, replace YOUR_GATEWAY_KEY with your configured gateway key. Saved browser keys are never included in examples."))}</p><div class="connection-checklist"><strong>${e(L("接入检查", "Connection checklist"))}</strong><p>${icon("check")}${e(L("客户端指向网关 8787，而非管理控制台 8788", "Use gateway port 8787, not console port 8788"))}</p><p>${icon("check")}${e(L("使用平台前缀明确模型来源", "Use a platform prefix for an explicit route"))}</p><p>${icon("check")}${e(L("先在模型测试中确认真实回复", "Confirm a reply in the playground first"))}</p></div></div></section></div>`;
}
function connectionExample() {
  const protocol = s.connectionProtocol, model = s.connectionModel || "YOUR_ROUTING_ID";
  const path = protocol === "openai-responses" ? "responses" : protocol === "anthropic-messages" ? "messages" : "chat/completions";
  const payload = protocol === "openai-responses" ? { model, input: "Hello", stream: true } : { model, ...(protocol === "anthropic-messages" ? { max_tokens: 1024 } : {}), messages: [{ role: "user", content: "Hello" }], stream: true };
  const key = s.cache.config?.auth_enabled ? "YOUR_GATEWAY_KEY" : "agent2api-local";
  const header = protocol === "anthropic-messages" ? `x-api-key: ${key}` : `Authorization: Bearer ${key}`;
  const quote = (value) => "'" + value.replaceAll("'", "'\\''") + "'";
  return `curl ${quote(`${gatewayURL() || "http://127.0.0.1:8787"}/v1/${path}`)} \\\n  -H 'Content-Type: application/json' \\\n  -H ${quote(header)} \\\n${protocol === "anthropic-messages" ? "  -H 'anthropic-version: 2023-06-01' \\\n" : ""}  -d ${quote(JSON.stringify(payload, null, 2))}`;
}
function modelPage() {
  return `<div class="notice info">${icon("info")}<div><strong>${e(L("同名模型，明确选择来源", "Same model name, explicit source"))}</strong><p>${e(L("带前缀的 ID 固定平台；不带前缀的 ID 按平台顺序调度，可按配置回退到另一平台。", "A prefixed ID pins its platform. Bare IDs follow platform priority and configured fallback."))} <a href="#platforms">${e(L("查看路由规则", "View routing policy"))} →</a></p></div></div>
  <div class="toolbar"><div class="toolbar-group">${platformTabs("models", s.models.platform)}</div><div class="toolbar-group">${button(L("更新目录", "Refresh catalog"), "reload-models", "", "refresh")}</div></div>
  <div class="toolbar"><div class="toolbar-group">${search("modelSearch", s.models.query, L("搜索模型名称或 ID…", "Search model name or ID…"))}<select id="capabilityFilter" class="select" aria-label="${e(L("能力筛选", "Capability filter"))}">${options(
    [
      ["all", L("全部能力", "All capabilities")],
      ["supports_images", L("视觉", "Vision")],
      ["supports_tools", L("工具调用", "Tools")],
      ["supports_thinking", L("思考", "Reasoning")],
    ],
    s.models.capability,
  )}</select><select id="pricingFilter" class="select" aria-label="${e(L("费率筛选", "Rate filter"))}" ${s.models.view === "matrix" ? "disabled" : ""}>${options([["all", L("全部费率", "All rates")], ["free", L("零积分", "Zero credits")], ["credits", L("积分计费", "Uses credits")], ["dynamic", L("浮动倍率", "Variable rate")], ["unknown", L("费率未知", "Unknown rate")]], s.models.pricing)}</select><select id="modelSort" class="select" aria-label="${e(L("模型排序", "Model sort"))}" ${s.models.view === "matrix" ? "disabled" : ""}>${options([["name", L("名称排序", "By name")], ["rate", L("积分倍率从低到高", "Lowest credit rate")], ["context", L("上下文优先", "Largest context")], ["output", L("输出上限优先", "Largest output")]], s.models.sort)}</select></div><div class="segmented"><button data-action="model-view" data-value="list" class="${s.models.view === "list" ? "active" : ""}">${e(L("模型目录", "Catalog"))}</button><button data-action="model-view" data-value="matrix" class="${s.models.view === "matrix" ? "active" : ""}">${e(L("账号可用性", "Availability"))}</button></div></div>
  <section class="card" id="modelResults"></section><p class="scope-note">${e(L("费率直接读取上游模型配置，不发送生成请求。× 值为积分消耗倍率，不是金额单价；零积分仍受账号权限与用量限制。详情中可查看原始标注和读取时间，超过 24 小时未更新的费率归为未知。", "Rates come from upstream configuration without generation requests. Multipliers are credit usage rates, not currency prices. Zero-credit models still have account and usage limits. Details show original labels and observation times; rates older than 24 hours are treated as unknown."))}</p>`;
}
function pricingName(status) {
  return ({ free: L("零积分", "Zero credits"), credits: L("积分计费", "Uses credits"), dynamic: L("浮动倍率", "Variable rate"), unknown: L("费率未知", "Unknown rate") })[status] || L("费率未知", "Unknown rate");
}
function pricingCell(m) {
  const p = modelPricing(m), rate = p.multiplier;
  const value = p.status === "dynamic" ? L("随所选模型变化", "Depends on selected model") : rate !== undefined ? `${p.stale ? L("上次 ", "Previously ") : ""}×${String(rate)}` : "—";
  return `${tag(pricingName(p.status), p.status === "free" ? "green" : p.status === "credits" ? "blue" : "outline")}<div class="cell-sub mono">${e(value)}</div><div class="cell-sub">${p.observedAt ? e((p.stale ? L("待更新 · ", "Stale · ") : "") + timestamp(p.observedAt)) : e(L("上游未提供", "Not reported"))}</div>`;
}
function updateModelTable() {
  if (!$("modelResults")) return;
  if (s.models.view === "matrix") return renderMatrix();
  const q = s.models.query.trim().toLowerCase();
  const filtered = models().filter(
    (m) =>
      (s.models.platform === "all" || m.platform === s.models.platform) &&
      (s.models.capability === "all" || m[s.models.capability]) &&
      (s.models.pricing === "all" || modelPricing(m).status === s.models.pricing) &&
      `${m.id} ${m.display_name} ${routeId(m)}`.toLowerCase().includes(q),
  );
  filtered.sort((a, b) => (s.models.sort === "rate" ? compareModelRates(a, b) : s.models.sort === "context" ? (b.context_tokens || 0) - (a.context_tokens || 0) : s.models.sort === "output" ? (b.max_output_tokens || 0) - (a.max_output_tokens || 0) : 0) || routeId(a).localeCompare(routeId(b)));
  const { rows, footer } = paginate(filtered, "models", 20);
  const body = rows
    .map(
      (m) =>
        `<tr><td class="cell-model"><div class="cell-main mono" title="${e(routeId(m))}">${e(m.id)}</div><div class="cell-sub mono truncate" title="${e(routeId(m))}">${e(routeId(m))}</div></td><td>${regionTag(m.platform)}<div class="cell-sub mono">${e(m.platform || "—")}</div></td><td class="cell-pricing">${pricingCell(m)}</td><td class="num mono">${m.context_tokens ? e(compact(m.context_tokens)) : "—"}</td><td class="num mono">${m.max_output_tokens ? e(compact(m.max_output_tokens)) : "—"}</td><td><div class="tags">${m.supports_images ? tag(L("视觉", "Vision"), "blue") : ""}${m.supports_tools ? tag(L("工具", "Tools"), "outline") : ""}${m.supports_thinking ? tag(L("思考", "Reasoning"), "outline") : ""}${!m.supports_images && !m.supports_tools && !m.supports_thinking ? '<span class="muted">—</span>' : ""}</div></td><td><div class="row-actions">${iconButton("copy", L("复制完整模型 ID", "Copy full model ID"), "copy-model", `data-model="${e(routeId(m))}"`)}${iconButton("chat", L("测试此模型", "Try this model"), "try-model", `data-model="${e(routeId(m))}"`)}${iconButton("more", L("模型详情", "Model details"), "model-detail", `data-model="${e(routeId(m))}"`)}</div></td></tr>`,
    )
    .join("");
  $("modelResults").innerHTML = rows.length
    ? `<div class="table-wrap"><table class="model-catalog-table"><thead><tr><th>${e(L("模型", "Model"))}</th><th>${e(L("来源平台", "Source"))}</th><th>${e(L("积分倍率", "Credit rate"))}</th><th class="num">${e(L("上下文", "Context"))}</th><th class="num">${e(L("最大输出", "Max output"))}</th><th>${e(L("能力", "Capabilities"))}</th><th class="num">${e(L("操作", "Actions"))}</th></tr></thead><tbody>${body}</tbody></table></div>${footer}`
    : empty(
        L("没有匹配的模型", "No matching models"),
        L(
          "尝试更换平台、费率、能力或搜索关键词。",
          "Try another platform, rate, capability or search term.",
        ),
      );
}
function paginate(items, kind, size) {
  const pages = Math.max(1, Math.ceil(items.length / size));
  s[kind].page = Math.min(s[kind].page, pages);
  const current = s[kind].page,
    start = (current - 1) * size;
  return {
    rows: items.slice(start, start + size),
    footer: `<div class="table-footer"><span>${items.length ? `${start + 1}–${Math.min(start + size, items.length)} / ${n(items.length)}` : "0"} ${e(L("条记录", "entries"))}</span><div class="pagination"><button class="icon-button" data-action="paginate" data-kind="${kind}" data-value="-1" aria-label="${e(L("上一页", "Previous page"))}" ${current <= 1 ? "disabled" : ""}>${icon("left")}</button><span>${current} / ${pages}</span><button class="icon-button" data-action="paginate" data-kind="${kind}" data-value="1" aria-label="${e(L("下一页", "Next page"))}" ${current >= pages ? "disabled" : ""}>${icon("chevron")}</button></div></div>`,
  };
}
function matrixOwners() {
  const owners = new Map();
  // Mirrors the backend's first-owner/bare-label collision convention.
  for (const a of accounts()) {
    const key = owners.has(a.label) ? `${a.platform}/${a.label}` : a.label;
    owners.set(key, a);
  }
  return owners;
}
function renderMatrix() {
  const m = s.cache.matrix;
  if (!m) {
    $("modelResults").innerHTML = s.errors.matrix
      ? empty(
          L("可用性加载失败", "Availability unavailable"),
          s.errors.matrix.message,
          "info",
        )
      : `<div class="loading-state"><span class="spinner"></span>${e(L("加载账号目录…", "Loading account catalogs…"))}</div>`;
    return;
  }
  const owners = matrixOwners(),
    columns = (m.accounts || []).filter(
      (key) =>
        s.models.platform === "all" ||
        owners.get(key)?.platform === s.models.platform,
    );
  const allowed = new Set(
    models()
      .filter(
        (model) =>
          (s.models.platform === "all" ||
            model.platform === s.models.platform) &&
          (s.models.capability === "all" || model[s.models.capability]),
      )
      .map((model) => model.id),
  );
  const rows = (m.models || []).filter(
    (id) =>
      id.toLowerCase().includes(s.models.query.toLowerCase()) &&
      allowed.has(id),
  );
  if (!columns.length || !rows.length) {
    $("modelResults").innerHTML = empty(
      L("没有匹配的账号与模型", "No matching accounts and models"),
    );
    return;
  }
  const header = columns
    .map((key) => {
      const a = owners.get(key);
      return `<th><span class="matrix-head-name">${e(a?.identity?.nickname || a?.label || key)}</span><span class="matrix-head-note">${e(a ? regionName(a.platform) : L("未记录来源", "Source unavailable"))} · ${n(m.in_flight?.[key])} / ${a?.max_concurrency || m.max_concurrency || "∞"}</span>${m.account_cooldowns?.[key] > 0 ? tag(countdown(m.account_cooldowns[key]), "amber") : a?.state === "disabled" ? tag(L("已停用", "Disabled")) : ""}</th>`;
    })
    .join("");
  const body = rows
    .map(
      (id) =>
        `<tr><td class="mono">${e(id)}</td>${columns
          .map((key) => {
            const cool = m.cooldowns?.[key]?.[id] || 0,
              supported = (m.matrix?.[key] || []).includes(id);
            return `<td>${cool > 0 ? `<span class="cooldown" data-until="${Date.now() + cool * 1000}">${e(countdown(cool))}</span>` : supported ? `<span class="supported" aria-label="${e(L("支持", "Supported"))}">✓</span>` : `<span class="unsupported" aria-label="${e(L("未开放", "Unavailable"))}">—</span>`}</td>`;
          })
          .join("")}</tr>`,
    )
    .join("");
  $("modelResults").innerHTML =
    `${cardHead(L("模型 × 账号", "Models × accounts"), L("✓ 表示目录支持；整号冷却显示在列头，单模型冷却显示在对应格。", "✓ means catalog support. Account cooldowns appear in headers; model cooldowns in cells."))}<div class="table-wrap"><table class="matrix"><thead><tr><th>${e(L("模型", "Model"))}</th>${header}</tr></thead><tbody>${body}</tbody></table></div><div class="table-footer">${e(L("目录支持不代表剩余额度，实际调用结果以日志为准。", "Catalog support does not guarantee quota. Check request logs for actual results."))}</div>`;
}

function accountStatus(a) {
  if (a.state === "disabled") return [L("已停用", "Disabled"), ""];
  if (a.healthy) return [L("就绪", "Ready"), "green"];
  if (a.reason === "quota_exhausted" || a.reason === "quota")
    return [L("额度耗尽", "Quota exhausted"), "amber"];
  if (a.reason === "unauthorized" || a.reason === "auth")
    return [L("需要登录", "Login required"), "red"];
  if (a.cooldown_secs > 0) return [L("冷却中", "Cooling down"), "amber"];
  return [L("暂不可用", "Unavailable"), "red"];
}
function credentialStatus(a) {
  const identity = a.identity;
  if (!identity) return L("未提供信息", "No information");
  if (identity.refresh_expired) return L("需要重新登录", "Sign in again");
  if (identity.needs_refresh) return L("等待自动刷新", "Awaiting refresh");
  return L("已登录", "Signed in");
}
function accountPage() {
  const all = accounts(),
    ready = all.filter((a) => a.healthy).length,
    cooling = all.filter((a) => !a.healthy && a.state !== "disabled").length,
    disabled = all.filter((a) => a.state === "disabled").length;
  return `<div class="health-summary"><span class="item"><span class="status-dot online"></span><strong>${ready}</strong>${e(L("就绪", "Ready"))}</span><span class="item"><span class="status-dot" style="background:var(--amber)"></span><strong>${cooling}</strong>${e(L("需关注", "Need attention"))}</span><span class="item"><span class="status-dot"></span><strong>${disabled}</strong>${e(L("停用", "Disabled"))}</span><span class="item"><strong>${n(all.reduce((sum, a) => sum + (a.in_flight || 0), 0))}</strong>${e(L("处理中的请求", "Active requests"))}</span></div>
  <div class="toolbar"><div class="toolbar-group">${platformTabs("accounts", s.accounts.platform)}</div>${button(L("添加账号", "Add account"), "add-account", "primary", "plus")}</div>
  <div class="toolbar"><div class="toolbar-group">${search("accountSearch", s.accounts.query, L("搜索昵称、账号或 UID…", "Search name, account or UID…"))}<select id="accountStatusFilter" class="select" aria-label="${e(L("账号状态", "Account status"))}">${options(
    [
      ["all", L("全部状态", "All states")],
      ["ready", L("就绪", "Ready")],
      ["attention", L("需关注", "Attention")],
      ["disabled", L("已停用", "Disabled")],
    ],
    s.accounts.status,
  )}</select></div><div class="toolbar-group"><span class="result-count" id="accountCount"></span><div class="segmented">${[["table", L("表格", "Table")], ["cards", L("卡片", "Cards")]].map(([id, title]) => `<button data-action="account-view" data-value="${id}" class="${s.accounts.view === id ? "active" : ""}" aria-pressed="${s.accounts.view === id}">${e(title)}</button>`).join("")}</div></div></div><div class="${s.accounts.view === "table" ? "card" : "account-grid"}" id="accountCards"></div>`;
}
function updateAccountCards() {
  if (!$("accountCards")) return;
  const q = s.accounts.query.trim().toLowerCase();
  const rows = accounts().filter(
    (a) =>
      (s.accounts.platform === "all" || a.platform === s.accounts.platform) &&
      `${a.label} ${a.identity?.nickname || ""} ${a.identity?.uid || ""}`
        .toLowerCase()
        .includes(q) &&
      (s.accounts.status === "all" ||
        (s.accounts.status === "ready" && a.healthy) ||
        (s.accounts.status === "disabled" && a.state === "disabled") ||
        (s.accounts.status === "attention" &&
          !a.healthy &&
          a.state !== "disabled")),
  );
  $("accountCount").textContent = L(
    `${rows.length} 个账号`,
    `${rows.length} accounts`,
  );
  if (s.accounts.view === "table") {
    $("accountCards").innerHTML = rows.length ? `<div class="table-wrap"><table class="account-table"><thead><tr><th>${e(L("账号", "Account"))}</th><th>${e(L("平台", "Platform"))}</th><th>${e(L("调度状态", "Scheduling"))}</th><th>${e(L("登录状态", "Credentials"))}</th><th>${e(L("并发", "Concurrency"))}</th><th class="num">${e(L("累计调用", "Requests"))}</th><th class="num">${e(L("操作", "Actions"))}</th></tr></thead><tbody>${rows.map((a) => {
      const index = accounts().indexOf(a), name = a.identity?.nickname || a.label;
      const ambiguous = accounts().filter((other) => other.label === a.label).length > 1;
      const usage = s.cache.metrics?.by_account?.find((row) => row.key === a.label);
      const max = a.max_concurrency || 0, current = a.in_flight || 0;
      return `<tr><td><div class="account-table-name"><span class="account-avatar">${e(name.slice(0, 1))}</span><div><strong>${e(name)}</strong><div class="cell-sub mono">${e(a.label)}</div></div></div><div class="cell-sub">${e(a.identity?.source === "desktop" ? L("桌面应用", "Desktop app") : L("凭证文件", "Credential file"))}</div></td><td>${regionTag(a.platform)}</td><td>${tag(...accountStatus(a))}${Object.keys(a.model_cooldowns || {}).length ? `<div class="cell-sub">${Object.keys(a.model_cooldowns).length} ${e(L("个模型冷却", "models cooling"))}</div>` : ""}${a.cooldown_secs > 0 ? `<div class="cell-sub" data-until="${Date.now() + a.cooldown_secs * 1000}">${e(countdown(a.cooldown_secs))}</div>` : ""}</td><td><span class="${a.identity?.refresh_expired ? "negative" : ""}">${e(credentialStatus(a))}</span><div class="cell-sub">${a.identity?.expires_at ? e(timestamp(a.identity.expires_at, true)) : "—"}</div></td><td class="account-concurrency"><span class="numeric">${current} / ${max || "∞"}</span><div class="load-track"><span style="width:${max ? Math.min(100, current / max * 100) : 0}%"></span></div></td><td class="num numeric">${ambiguous ? `<span class="muted" title="${e(L("同标识跨平台聚合，无法单独归属", "This label is aggregated across platforms"))}">—</span>` : n(usage?.total)}${!ambiguous && usage?.failed ? `<div class="cell-sub negative">${n(usage.failed)} ${e(L("失败", "failed"))}</div>` : ""}</td><td><div class="row-actions"><button class="button small" data-action="account-requests" data-platform="${e(a.platform)}" data-label="${e(a.label)}">${e(L("记录", "Logs"))}</button>${iconButton("more", L("账号详情与操作", "Account details and actions"), "account-detail", `data-index="${index}"`)}</div></td></tr>`;
    }).join("")}</tbody></table></div>` : empty(L("没有匹配的账号", "No matching accounts"), L("更换筛选条件，或添加一个账号。", "Change filters or add an account."), "accounts");
    return;
  }
  $("accountCards").innerHTML =
    rows
      .map((a) => {
        const index = accounts().indexOf(a),
          name = a.identity?.nickname || a.label,
          [state, color] = accountStatus(a),
          max = a.max_concurrency || 0;
        const load = max
          ? `${a.in_flight || 0} / ${max}`
          : `${a.in_flight || 0}`;
        return `<article class="card account-card"><div class="account-card-top"><div class="account-avatar">${e(name.slice(0, 1).toUpperCase())}</div><div class="account-name"><strong title="${e(name)}">${e(name)}</strong><small>${e(platformName(a.platform))}</small></div>${iconButton("more", L("账号详情与操作", "Account details and actions"), "account-detail", `data-index="${index}"`)}</div><div class="account-state">${tag(state, color, true)}<div class="account-load">${e(L("并发", "Active"))} ${e(load)}<div class="load-track"><span style="width:${max ? Math.min(100, ((a.in_flight || 0) / max) * 100) : 0}%"></span></div></div></div>${a.cooldown_secs > 0 ? `<div class="account-note">${e(L("整号冷却剩余", "Account cooldown"))} <span data-until="${Date.now() + a.cooldown_secs * 1000}">${e(countdown(a.cooldown_secs))}</span></div>` : ""}<div class="account-kv"><span>${e(L("登录状态", "Credentials"))}</span><span>${e(credentialStatus(a))}</span></div><div class="account-kv"><span>${e(L("来源", "Source"))}</span><span>${e(a.identity?.source === "desktop" ? L("桌面应用", "Desktop app") : a.identity?.source === "file" ? L("凭证文件", "Credential file") : a.identity?.source || "—")}</span></div><div class="account-kv"><span>UID</span><span title="${e(a.identity?.uid || "")}">${e(a.identity?.uid || "—")}</span></div><button class="button small" data-action="account-detail" data-index="${index}">${e(L("查看详情与管理", "Details & management"))}${icon("arrow")}</button></article>`;
      })
      .join("") ||
    `<div class="card" style="grid-column:1/-1">${empty(L("没有匹配的账号", "No matching accounts"), L("更换筛选条件，或添加一个账号。", "Change filters or add an account."), "accounts")}</div>`;
}
function routePolicy() {
  const routing = s.cache.models?.routing,
    order = (s.cache.models?.platforms || platforms()).map((p) => p.id);
  const shared = models().find((m) =>
    models().some(
      (other) => other.id === m.id && other.platform !== m.platform,
    ),
  )?.id;
  const example = shared || models()[0]?.id || "model-id";
  return `<div class="route-policy"><section class="card">${cardHead(L("自动路由", "Automatic routing"), L("使用不带平台前缀的模型名", "Use a model name without a prefix"))}<div class="card-body"><div class="route-path"><code>${e(example)}</code>${icon("arrow")}${order.map((id, index) => `${index ? icon("arrow") : ""}${regionTag(id)}`).join("")}</div><p>${e(L("按配置中的平台顺序，选择支持该模型的平台。", "Selects a platform supporting the model, in configured order."))}</p><div class="tags" style="margin-top:14px">${tag(routing ? (routing.fallback_on_unavailable ? L("不可用时同名模型回退已启用", "Same-model fallback enabled") : L("自动回退未启用", "Fallback disabled")) : L("路由信息加载中", "Loading routing policy"), routing?.fallback_on_unavailable ? "green" : "")}</div></div></section><section class="card">${cardHead(L("固定平台", "Pinned platform"), L("使用完整的平台 / 模型 ID", "Use the full platform / model ID"))}<div class="card-body"><div class="route-path"><code>${e(order.find((id) => id === "workbuddy-global") || order[0] || "platform")}/${e(example)}</code>${icon("arrow")}${regionTag(order.find((id) => id === "workbuddy-global") || order[0])}</div><p>${e(L("只在指定平台的账号池内调度。显式前缀与模型别名不进行跨平台回退。", "Uses only the selected platform pool. Prefixed IDs and aliases never fall back across platforms."))}</p><div class="tags" style="margin-top:14px">${tag(L("适合 ZCode 的平台前缀配置", "Ideal for prefixed ZCode models"), "blue")}</div></div></section></div>`;
}
function platformPage() {
  const active = s.cache.platforms?.active || platforms(),
    planned = s.cache.platforms?.planned || [];
  return `<div class="platform-grid">${active
    .map((p) => {
      const pool = accounts().filter((a) => a.platform === p.id),
        ready = pool.filter((a) => a.healthy).length,
        count =
          models().filter((m) => m.platform === p.id).length || p.models || 0;
      return `<article class="card platform-card"><div class="platform-card-top"><div class="platform-avatar ${p.id === "workbuddy-global" ? "global" : ""}">${e(platformMark(p.id))}</div><div><h2>${e(platformName(p.id))}</h2><p class="mono">${e(p.id)}</p></div>${tag(p.status === "active" ? L("已连接", "Connected") : L("需关注", "Attention"), p.status === "active" ? "green" : "amber", true)}</div><div class="platform-card-stats"><div><strong>${count}</strong><small>${e(L("模型条目", "Model entries"))}</small></div><div><strong>${pool.length}</strong><small>${e(L("账号总数", "Accounts"))}</small></div><div><strong>${ready}</strong><small>${e(L("账号就绪", "Ready accounts"))}</small></div></div><div class="platform-url mono">${e(p.base_url || "—")}</div><div class="platform-card-footer"><span>${e(L("模型前缀", "Model prefix"))} <code>${e(p.id)}/</code></span><button class="button small" data-action="platform-models" data-value="${e(p.id)}">${e(L("查看模型", "View models"))}${icon("arrow")}</button></div></article>`;
    })
    .join(
      "",
    )}</div><div class="section-label">${e(L("同名模型路由规则", "SHARED MODEL ROUTING"))}</div>${routePolicy()}${planned.length ? `<details class="card planned-platforms"><summary>${e(L("尚未接入的平台", "Platforms not yet integrated"))} · ${planned.map((p) => e(p.name)).join(" / ")}</summary>${planned.map((p) => `<p><strong>${e(p.name)}</strong> · ${e(p.note || p.id)}</p>`).join("")}</details>` : ""}`;
}

function protocolName(protocol) {
  return (
    {
      "openai-chat": "Chat Completions",
      "openai-responses": "Responses",
      "anthropic-messages": "Anthropic Messages",
    }[protocol] ||
    protocol ||
    "—"
  );
}
function recentRequests() {
  return [...(s.cache.metrics?.recent || [])].sort(
    (a, b) => new Date(b.time) - new Date(a.time),
  );
}
function requestPage() {
  const sample = recentRequests();
  const accountOptions = [...new Map(sample.map((row) => [accountKey(row), `${regionName(row.platform)} / ${row.account || L("未记录账号", "Unrecorded")}`])).entries()];
  if (s.requests.account.startsWith("label:")) accountOptions.unshift([s.requests.account, `${L("标识", "Label")}: ${s.requests.account.slice(6)}`]);
  else if (s.requests.account !== "all" && !accountOptions.some(([key]) => key === s.requests.account)) {
    const [platform, label] = JSON.parse(s.requests.account);
    accountOptions.unshift([s.requests.account, `${regionName(platform)} / ${label || L("未记录账号", "Unrecorded")}`]);
  }
  const modelOptions = [...new Set(sample.map((row) => row.model))];
  if (s.requests.model !== "all" && !modelOptions.includes(s.requests.model)) modelOptions.push(s.requests.model);
  return `<div class="card request-filters"><div class="toolbar"><div class="toolbar-group">${search("requestSearch", s.requests.query, L("搜索模型、账号、状态码或错误…", "Search model, account, status or error…"))}<select id="requestPlatformFilter" class="select" aria-label="${e(L("平台筛选", "Platform filter"))}">${platformOptions(s.requests.platform)}</select><select id="requestStatusFilter" class="select" aria-label="${e(L("调用状态", "Request status"))}">${options(
    [
      ["all", L("全部状态", "All states")],
      ["ok", L("成功", "Success")],
      ["failed", L("失败", "Failed")],
    ],
    s.requests.status,
  )}</select><select id="requestProtocolFilter" class="select" aria-label="${e(L("协议筛选", "Protocol filter"))}">${options(
    [
      ["all", L("全部协议", "All protocols")],
      ["openai-chat", "Chat Completions"],
      ["openai-responses", "Responses"],
      ["anthropic-messages", "Anthropic"],
    ],
    s.requests.protocol,
  )}</select></div></div><div class="toolbar filter-secondary"><div class="toolbar-group"><select id="requestModelFilter" class="select" aria-label="${e(L("模型筛选", "Model filter"))}">${options([["all", L("全部模型", "All models")], ...modelOptions.sort().map((id) => [id, id])], s.requests.model)}</select><select id="requestAccountFilter" class="select" aria-label="${e(L("账号筛选", "Account filter"))}">${options([["all", L("全部账号", "All accounts")], ...accountOptions], s.requests.account)}</select><select id="requestRangeFilter" class="select" aria-label="${e(L("记录时间范围", "Request time range"))}">${options([["all", L("全部缓存记录", "All buffered records")], ["15", L("最近 15 分钟", "Last 15 minutes")], ["30", L("最近 30 分钟", "Last 30 minutes")], ["60", L("最近 1 小时", "Last hour")]], s.requests.range)}</select></div>${button(L("清空筛选", "Reset filters"), "reset-request-filters", "ghost", "close")}</div></div><div class="sample-summary" id="requestSampleStats"></div><section class="card"><div class="card-head request-table-head"><div><h2>${e(L("调用记录", "Request records"))}</h2><p>${e(L("网关仅保留最近 200 条；筛选与导出作用于这些记录", "The gateway retains the latest 200 records; filters and exports apply to this sample"))}</p></div><div class="toolbar-group"><select id="requestSort" class="select" aria-label="${e(L("记录排序", "Request sort"))}">${options([["time", L("最新优先", "Newest first")], ["latency", L("耗时优先", "Longest duration")], ["tokens", L("Token 优先", "Most tokens")]], s.requests.sort)}</select><select id="requestPageSize" class="select" aria-label="${e(L("每页记录数", "Records per page"))}">${options([["20", "20 / " + L("页", "page")], ["50", "50 / " + L("页", "page")], ["100", "100 / " + L("页", "page")]], String(s.requests.size))}</select>${button(L("导出", "Export"), "export-requests", "small", "requests")}</div></div><div id="requestResults"></div></section>`;
}
function filteredRequests() {
  return filterRequests(recentRequests(), s.requests);
}
function requestTable(rows, details = true) {
  if (!rows.length)
    return empty(
      L("暂无调用记录", "No requests found"),
      L(
        "开始调用模型，或调整当前筛选条件。",
        "Make a model request or adjust your filters.",
      ),
      "requests",
    );
  return `<div class="table-wrap"><table><thead><tr><th>${e(L("时间", "Time"))}</th><th>${e(L("模型与平台", "Model & platform"))}</th><th>${e(L("协议", "Protocol"))}</th><th>${e(L("状态", "Status"))}</th><th class="num">${e(L("耗时", "Duration"))}</th><th class="num">${e(L("输入 / 输出", "Input / output"))}</th><th></th></tr></thead><tbody>${rows
    .map((row) => {
      // Use a stable key instead of object identity: overview and logs have separate array copies.
      const key = requestKey(row);
      return `<tr class="request-click" data-action="request-detail" data-key="${e(key)}" tabindex="0" aria-label="${e(`${row.model} · ${timestamp(row.time)} · ${L("调用详情", "Request details")}`)}"><td class="mono">${e(timestamp(row.time))}<div class="cell-sub">${e(new Date(row.time).toLocaleDateString(undefined, { month: "2-digit", day: "2-digit" }))}</div></td><td class="cell-model"><div class="cell-main mono" title="${e(row.model)}">${e(row.model)}</div><div class="cell-sub">${row.platform ? regionTag(row.platform) : e(L("来源未记录", "Source unrecorded"))} ${e(row.account || "")}</div>${row.model_requested ? `<div class="cell-sub requested-model" title="${e(row.model_requested)}">${e(L("请求", "Requested"))}: ${e(row.model_requested)}</div>` : ""}</td><td><span style="font-size:11px">${e(protocolName(row.protocol))}</span><div class="cell-sub">${e(row.stream ? L("流式", "Streaming") : L("非流式", "Non-streaming"))}</div></td><td>${tag(`${row.status} · ${row.ok ? L("成功", "OK") : L("失败", "Failed")}`, row.ok ? "green" : "red")} ${!row.ok && row.error ? `<span class="table-short-error" title="${e(row.error)}">${e(row.error.slice(0, 120))}</span>` : ""}</td><td class="num mono">${e(duration(row.duration_ms))}${row.gen_ms > 0 && row.output_tokens > 0 ? `<div class="cell-sub">${(row.output_tokens / (row.gen_ms / 1000)).toFixed(1)} tok/s</div>` : ""}</td><td class="num mono">${n(row.input_tokens)} <span class="muted">/</span> ${n(row.output_tokens)}</td><td>${iconButton("chevron", L("调用详情", "Request details"), "request-detail", `data-key="${e(key)}"`)}</td></tr>`;
    })
    .join("")}</tbody></table></div>`;
}
function updateRequestTable() {
  if (!$("requestResults")) return;
  const filtered = filteredRequests(),
    { rows, footer } = paginate(filtered, "requests", s.requests.size);
  $("requestResults").innerHTML = requestTable(rows) + footer;
  const stats = summarizeRequests(filtered);
  $("requestSampleStats").innerHTML = `<span>${e(L("匹配记录", "Matched"))}<strong>${n(stats.total)}</strong><small>/ ${recentRequests().length}</small></span><span>${e(L("成功率", "Success rate"))}<strong>${stats.total ? (stats.success_rate * 100).toFixed(1) + "%" : "—"}</strong></span><span>${e(L("失败", "Failed"))}<strong class="${stats.failed ? "negative" : ""}">${n(stats.failed)}</strong></span><span>P95<strong>${e(duration(stats.p95))}</strong></span><span>${e(L("输出 Token", "Output tokens"))}<strong>${compact(stats.output_tokens)}</strong></span>`;
}

function settingsPage() {
  const config = s.cache.config || {},
    upstream = config.upstream || {};
  return `<div class="settings-grid"><div class="settings-stack"><section class="card">${cardHead(L("客户端连接", "Client connection"), L("ZCode 与其他客户端使用同一个网关入口", "ZCode and other clients share this gateway"))}<div class="card-body"><div class="field"><span>OpenAI Base URL</span><div class="endpoint-box"><code>${e(gatewayURL() ? `${gatewayURL()}/v1` : "—")}</code>${iconButton("copy", L("复制地址", "Copy URL"), "copy-openai")}</div></div><div class="field"><span>Anthropic Base URL</span><div class="endpoint-box"><code>${e(gatewayURL() || "—")}</code>${iconButton("copy", L("复制地址", "Copy URL"), "copy-base")}</div></div><div class="setting-row"><div>${e(L("网关鉴权", "Gateway authentication"))}<p>${e(config.auth_enabled ? L("客户端需要配置有效的 API Key。", "Clients need a valid API key.") : L("当前本地网关未启用 API Key 校验。", "API key validation is disabled on this local gateway."))}</p></div>${tag(config.auth_enabled ? L("已启用", "Enabled") : L("未启用", "Disabled"), config.auth_enabled ? "green" : "")}</div><div class="setting-row"><div>${e(L("控制台连接凭据", "Console credentials"))}<p>${e(L("只保存到当前浏览器，不更改后端鉴权。", "Saved in this browser only; does not change gateway authentication."))}</p></div>${button(L("配置", "Configure"), "auth", "small")}</div></div></section><section class="card">${cardHead(L("平台兼容设置", "Platform compatibility"), L("支持即时修改，无需重启网关", "Changes apply immediately without restarting"))}<div class="card-body">${(config.platforms || []).map((p) => `<div class="setting-row"><div>${e(platformName(p.id))}<p>${e(L("请求内容脱敏", "Request content sanitization"))}</p></div><input type="checkbox" class="switch" data-sanitize="${e(p.id)}" ${p.sanitize ? "checked" : ""} aria-label="${e(platformName(p.id) + " " + L("内容脱敏", "sanitization"))}"></div>`).join("")}</div></section></div><div class="settings-stack"><section class="card">${cardHead(L("运行参数", "Runtime parameters"), L("连接参数由启动配置管理", "Connection parameters are managed by startup configuration"))}<div class="card-body"><dl class="details-list">${detailItem(L("监听地址", "Listen address"), config.listen, "mono")}${detailItem(L("普通请求超时", "Request timeout"), upstream.request_timeout)}${detailItem(L("流式空闲超时", "Stream idle timeout"), upstream.stream_idle_timeout)}${detailItem(L("流式总超时", "Stream total timeout"), upstream.stream_total_timeout)}${detailItem(L("模型目录缓存", "Model catalog cache"), upstream.model_cache_ttl)}</dl></div></section><section class="card">${cardHead(L("模型目录", "Model catalog"), L("从已连接的平台更新可用模型", "Reload available models from connected platforms"))}<div class="card-body"><p class="muted" style="font-size:12px;margin-bottom:15px">${e(L("保留平台前缀与同名模型路由，更新后可回到模型目录查看。", "Keeps platform prefixes and shared-name routing. Review changes in the model catalog."))}</p>${button(L("更新模型目录", "Refresh model catalog"), "reload-models", "", "refresh")}</div></section></div></div>`;
}

function showDrawer(title, content) {
  $("detailContent").innerHTML =
    `<div class="dialog-header"><h2>${e(title)}</h2>${iconButton("close", L("关闭", "Close"), "close-detail")}</div>${content}`;
  if (!$("detailDialog").open) $("detailDialog").showModal();
}
function showModelDetail(id) {
  const m = models().find((model) => routeId(model) === id);
  if (!m) return;
  showDrawer(
    L("模型详情", "Model details"),
    `<div class="detail-hero"><div class="platform-avatar ${m.platform === "workbuddy-global" ? "global" : ""}">${icon("models")}</div><div><h2>${e(m.display_name || m.id)}</h2><p>${e(platformName(m.platform))}</p></div></div><dl class="details-list">${detailItem(L("完整模型 ID", "Full model ID"), routeId(m), "mono")}${detailItem(L("原始模型名", "Original model name"), m.id, "mono")}${detailItem(L("费率类型", "Rate type"), pricingName(modelPricing(m).status))}${detailItem(L("积分倍率", "Credit multiplier"), modelPricing(m).multiplier !== undefined ? "×" + modelPricing(m).multiplier : modelPricing(m).status === "dynamic" ? L("随所选模型变化", "Depends on selected model") : L("未知", "Unknown"))}${detailItem(L("上游费率标注", "Original rate label"), modelPricing(m).raw || L("未提供", "Not reported"))}${detailItem(L("费率来源与时间", "Rate source and time"), modelPricing(m).observedAt ? L("上游模型配置 · ", "Upstream configuration · ") + timestamp(modelPricing(m).observedAt, true) + (modelPricing(m).stale ? L(" · 已超过 24 小时，需更新", " · Older than 24 hours, refresh needed") : "") : L("无可核实信息", "No verified metadata"))}${detailItem(L("上下文窗口", "Context window"), m.context_tokens ? n(m.context_tokens) + " tokens" : L("未提供", "Unreported"))}${detailItem(L("最大输出", "Maximum output"), m.max_output_tokens ? n(m.max_output_tokens) + " tokens" : L("未提供", "Unreported"))}${detailItem(L("模型能力", "Capabilities"), [m.supports_images ? L("视觉", "Vision") : "", m.supports_tools ? L("工具调用", "Tools") : "", m.supports_thinking ? L("思考", "Reasoning") : ""].filter(Boolean).join(" / ") || L("未提供", "Unreported"))}${detailItem(L("裸模型名默认路由", "Default route for bare ID"), m.default_route ? L("默认优先使用此平台", "This platform is preferred") : L("完整 ID 可固定到此平台", "Use the full ID to select this platform"))}${m.description ? detailItem(L("说明", "Description"), m.description) : ""}</dl><div class="detail-actions"><button class="button" data-action="copy-model" data-model="${e(id)}">${icon("copy")}${e(L("复制完整 ID", "Copy full ID"))}</button><button class="button primary" data-action="try-model" data-model="${e(id)}">${icon("chat")}${e(L("测试模型", "Try model"))}</button></div>`,
  );
}
function showAccountDetail(index) {
  const a = accounts()[index];
  if (!a) return;
  const [status, color] = accountStatus(a),
    identity = a.identity || {};
  showDrawer(
    L("账号详情", "Account details"),
    `<div class="detail-hero"><div class="account-avatar">${e((identity.nickname || a.label).slice(0, 1))}</div><div><h2>${e(identity.nickname || a.label)}</h2><p>${e(platformName(a.platform))}</p></div></div><div class="tags" style="margin-top:17px">${tag(status, color, true)}${tag(credentialStatus(a), "outline")}</div><dl class="details-list">${detailItem(L("账号标识", "Account label"), a.label, "mono")}${detailItem("UID", identity.uid)}${accountUsageDetails(a)}${detailItem(L("当前并发", "Active requests"), `${a.in_flight || 0} / ${a.max_concurrency || L("不限", "Unlimited")}`)}${detailItem(L("状态原因", "State reason"), a.reason || "—")}${a.cooldown_secs ? detailItem(L("整号冷却", "Account cooldown"), countdown(a.cooldown_secs)) : ""}${detailItem(L("登录令牌到期", "Access token expiry"), identity.expires_at ? timestamp(identity.expires_at, true) : "—")}${detailItem(L("凭证位置", "Credential path"), identity.credential_path || "—", "mono")}${a.last_error ? detailItem(L("最近错误", "Last error"), a.last_error, "error-text") : ""}${
      Object.keys(a.model_cooldowns || {}).length
        ? detailItem(
            L("模型冷却", "Model cooldowns"),
            Object.entries(a.model_cooldowns)
              .map(([id, seconds]) => `${id}: ${countdown(seconds)}`)
              .join("\n"),
            "mono",
          )
        : ""
    }</dl><div class="detail-actions">${button(a.state === "disabled" ? L("启用账号", "Enable account") : L("停用账号", "Disable account"), "account-control", "", a.state === "disabled" ? "check" : "stop").replace('data-action="account-control"', `data-action="account-control" data-value="${a.state === "disabled" ? "enable" : "disable"}" data-platform="${e(a.platform)}" data-label="${e(a.label)}"`)}<button class="button" data-action="account-control" data-value="reset" data-platform="${e(a.platform)}" data-label="${e(a.label)}">${icon("refresh")}${e(L("重置冷却", "Reset cooldown"))}</button>${identity.source === "desktop" ? `<p class="muted" style="font-size:11px;margin-top:10px">${e(L("此账号来自桌面应用，请在对应应用中重新登录；网关会自动读取更新后的凭证。", "This account comes from a desktop app. Sign in there; the gateway will read the refreshed credentials."))}</p>` : `<button class="button" data-action="relogin" data-platform="${e(a.platform)}" data-label="${e(identity.credential_path ? identity.credential_path.split(/[\\/]/).pop() : a.label)}">${e(L("重新登录", "Sign in again"))}</button>`}</div>`,
  );
}
function accountUsageDetails(account) {
  const metrics = s.cache.metrics;
  if (!metrics) return "";
  if (accounts().filter((a) => a.label === account.label).length > 1) {
    return detailItem(
      L("账号用量", "Account usage"),
      L(
        "此标识在多个平台重复，统计按标识合并；可在调用日志按平台查看。",
        "This label exists on multiple platforms. Aggregates combine labels; filter request logs by platform.",
      ),
    );
  }
  const usage = metrics.by_account?.find((row) => row.key === account.label);
  return (
    detailItem(
      L("累计调用", "Recorded requests"),
      `${n(usage?.total)} · ${n(usage?.ok)} ${L("成功", "successful")}`,
    ) +
    detailItem(
      L("累计输入 / 输出 Token", "Recorded input / output tokens"),
      `${n(usage?.input_tokens)} / ${n(usage?.output_tokens)}`,
    )
  );
}
function showRequestDetail(key) {
  const row = recentRequests().find(
    (r) => requestKey(r) === key,
  );
  if (!row) return;
  showDrawer(
    L("调用详情", "Request details"),
    `<div class="detail-hero"><div class="platform-avatar">${icon("requests")}</div><div><h2>${e(row.model)}</h2><p>${e(timestamp(row.time, true))}</p></div></div><div class="tags" style="margin-top:17px">${tag(`${row.status} · ${row.ok ? L("成功", "Success") : L("失败", "Failed")}`, row.ok ? "green" : "red")}${regionTag(row.platform)}</div><dl class="details-list">${detailItem(L("客户端请求模型", "Requested model"), row.model_requested || row.model, "mono")}${detailItem(L("实际模型", "Actual model"), row.model, "mono")}${detailItem(L("实际平台", "Actual platform"), row.platform || L("未记录", "Unrecorded"), "mono")}${detailItem(L("账号", "Account"), row.account || "—")}${detailItem(L("协议", "Protocol"), protocolName(row.protocol))}${detailItem(L("输出方式", "Response mode"), row.stream ? L("流式", "Streaming") : L("非流式", "Non-streaming"))}${detailItem(L("完整耗时", "Total duration"), duration(row.duration_ms))}${detailItem(L("输入 / 输出 Token", "Input / output tokens"), `${n(row.input_tokens)} / ${n(row.output_tokens)}`)}${detailItem(L("思考 Token", "Reasoning tokens"), n(row.reasoning_tokens))}${detailItem(L("生成速度", "Generation speed"), row.gen_ms > 0 && row.output_tokens ? `${(row.output_tokens / (row.gen_ms / 1000)).toFixed(1)} tok/s` : L("未测量", "Not measured"))}${row.error ? detailItem(L("错误详情", "Error details"), row.error, "error-text") : ""}</dl>`,
  );
}

function modal(title, content) {
  $("modalContent").innerHTML =
    `<div class="dialog-header"><h2>${e(title)}</h2>${iconButton("close", L("关闭", "Close"), "close-modal")}</div>${content}`;
  if (!$("modal").open) $("modal").showModal();
}
function openRequestFilters(filters) {
  s.requests = { ...s.requests, query: "", platform: "all", status: "all", protocol: "all", account: "all", model: "all", range: "all", sort: "time", page: 1, ...filters };
  if (s.page === "requests") draw();
  else location.hash = "requests";
}
function commandSearch() {
  if ($("modal").open) return;
  modal(L("搜索与快捷跳转", "Search and navigation"), `<div class="command-search-field">${search("commandSearch", "", L("搜索页面、模型或账号…", "Search pages, models or accounts…"))}<kbd>Esc</kbd></div><div id="commandResults" class="command-results"></div><p class="scope-note">${e(L("Enter 打开第一项 · Esc 关闭", "Enter opens the first result · Esc closes"))}</p>`);
  updateCommandResults("");
  $("commandSearch").focus();
}
function updateCommandResults(query) {
  const q = query.trim().toLowerCase();
  const pages = Object.keys(names).filter((page) => `${page} ${names[page]()}`.toLowerCase().includes(q));
  const foundModels = q ? models().filter((m) => `${routeId(m)} ${m.display_name}`.toLowerCase().includes(q)).slice(0, 6) : [];
  const foundAccounts = q ? accounts().filter((a) => `${a.label} ${a.identity?.nickname || ""} ${a.platform}`.toLowerCase().includes(q)).slice(0, 4) : [];
  $("commandResults").innerHTML = `${pages.length ? `<div class="command-section-label">${e(L("页面", "Pages"))}</div>${pages.map((page) => `<button data-action="jump-page" data-value="${page}">${icon(page === "analytics" ? "activity" : page === "connection" ? "external" : page)}<span>${e(names[page]())}</span>${icon("arrow")}</button>`).join("")}` : ""}${foundModels.length ? `<div class="command-section-label">${e(L("测试模型", "Try a model"))}</div>${foundModels.map((m) => `<button data-action="try-model" data-model="${e(routeId(m))}">${icon("models")}<span class="mono truncate">${e(routeId(m))}</span>${icon("arrow")}</button>`).join("")}` : ""}${foundAccounts.length ? `<div class="command-section-label">${e(L("账号", "Accounts"))}</div>${foundAccounts.map((a) => `<button data-action="account-detail" data-index="${accounts().indexOf(a)}">${icon("accounts")}<span>${e(a.identity?.nickname || a.label)}</span>${regionTag(a.platform)}</button>`).join("")}` : ""}` || empty(L("没有匹配结果", "No matches"), L("尝试模型名、平台或页面名称。", "Try a model, platform or page name."));
}
function chartReadout(target) {
  const shell = target.closest(".chart-shell"), tooltip = shell?.querySelector(".chart-tooltip");
  if (!tooltip) return;
  const { time, a, b, kind, x } = target.dataset;
  tooltip.hidden = false;
  tooltip.innerHTML = `<strong>${e(time)}</strong><div><i class="legend-dot"></i>${e(kind === "requests" ? L("成功", "Success") : L("输入 Token", "Input tokens"))}<b>${n(a)}</b></div><div><i class="legend-dot red"></i>${e(kind === "requests" ? L("失败", "Failed") : L("输出 Token", "Output tokens"))}<b>${n(b)}</b></div>`;
  tooltip.style.left = `${Math.min(Math.max(10, Number(x) / 610 * shell.clientWidth - 80), Math.max(10, shell.clientWidth - 175))}px`;
  const crosshair = shell.querySelector(".chart-crosshair");
  crosshair.removeAttribute("hidden");
  crosshair.setAttribute("x1", x);
  crosshair.setAttribute("x2", x);
}
document.addEventListener("pointerover", (event) => {
  const point = event.target.closest?.("[data-chart-point]");
  if (point) chartReadout(point);
});
document.addEventListener("focusin", (event) => {
  if (event.target.matches("[data-chart-point]")) chartReadout(event.target);
});
document.addEventListener("pointerout", (event) => {
  const shell = event.target.closest?.(".chart-shell");
  if (shell && !shell.contains(event.relatedTarget) && !shell.contains(document.activeElement)) {
    shell.querySelector(".chart-tooltip").hidden = true;
    shell.querySelector(".chart-crosshair").setAttribute("hidden", "");
  }
});
document.addEventListener("focusout", (event) => {
  const shell = event.target.closest?.(".chart-shell");
  if (shell && !shell.contains(event.relatedTarget)) {
    shell.querySelector(".chart-tooltip").hidden = true;
    shell.querySelector(".chart-crosshair").setAttribute("hidden", "");
  }
});
function authDialog() {
  modal(
    L("配置控制台连接", "Console connection"),
    `<p>${e(L("填写网关的 API Key。网关未启用鉴权时可以留空；此项不修改后端配置。", "Enter the gateway API key. Leave it empty when gateway authentication is disabled. This does not change the backend configuration."))}</p><form id="authForm"><label class="field"><span>API Key</span><input id="authKey" type="password" autocomplete="off" value="${e(s.key)}" placeholder="${e(L("未启用鉴权时留空", "Leave empty if authentication is disabled"))}"></label><div id="authMessage" class="login-status" role="status"></div><div class="modal-actions">${button(L("取消", "Cancel"), "close-modal")}<button class="button primary" type="submit" id="authSave">${e(L("验证并保存", "Verify & save"))}</button></div></form>`,
  );
}
async function saveAuth(event) {
  event.preventDefault();
  const old = s.key,
    newKey = $("authKey").value.trim();
  s.key = newKey;
  $("authSave").disabled = true;
  try {
    s.cache.status = await api("/api/status");
    s.authRequired = false;
    save("agent2api_key", newKey);
    $("modal").close();
    s.errors = {};
    toast(L("连接已验证", "Connection verified"));
    await refresh([
      ...new Set([...pageKeys[s.page], "models", "accounts", "config"]),
    ]);
  } catch (error) {
    s.key = old;
    if ($("authMessage")) {
      $("authMessage").textContent = error.message;
      $("authMessage").className = "login-status negative";
    }
    if ($("authSave")) $("authSave").disabled = false;
  }
}
function addAccountDialog() {
  modal(
    L("添加账号", "Add account"),
    `<p>${e(L("选择账号所属的平台，然后在浏览器中完成授权。新账号会自动加入对应账号池。", "Choose a platform and authorize in your browser. The new account will join its pool automatically."))}</p><form id="addAccountForm"><label class="field"><span>${e(L("来源平台", "Platform"))}</span><select id="loginPlatform">${options(
      platforms().map((p) => [p.id, platformName(p.id)]),
      s.accounts.platform === "all" ? platforms()[0]?.id : s.accounts.platform,
    )}</select></label><div class="modal-actions">${button(L("取消", "Cancel"), "close-modal")}<button class="button primary" type="submit">${icon("external")}${e(L("开始登录", "Start login"))}</button></div></form>`,
  );
}
let loginGeneration = 0;
async function startLogin(platform, name = "") {
  $("detailDialog").close();
  const generation = ++loginGeneration;
  modal(
    L("账号授权", "Account authorization"),
    `<p>${e(platformName(platform))} · ${e(L("正在获取授权地址…", "Getting an authorization URL…"))}</p><div id="loginBody" class="loading-state" style="min-height:100px"><span class="spinner"></span></div>`,
  );
  const alive = () => generation === loginGeneration && $("modal").open;
  let session;
  try {
    session = await api(
      `/api/accounts/manage?platform=${encodeURIComponent(platform)}${name ? `&name=${encodeURIComponent(name)}` : ""}`,
      {},
    );
  } catch (error) {
    if (alive())
      $("loginBody").innerHTML =
        `<div class="message-error">${e(error.message)}</div>`;
    return;
  }
  if (!alive()) return;
  $("loginBody").className = "";
  $("loginBody").innerHTML =
    `<label class="field"><span>${e(L("授权地址", "Authorization URL"))}</span><input id="loginURL" readonly value="${e(session.auth_url || "")}"></label><div class="modal-actions">${button(L("复制链接", "Copy link"), "copy-login", "", "copy")}<button class="button primary" data-action="open-login" id="loginOpen" ${session.auth_url ? "" : "disabled"}>${icon("external")}${e(L("打开授权页面", "Open authorization"))}</button></div><div id="loginStatus" class="login-status" role="status">${e(L("等待授权，最长 5 分钟。", "Waiting for authorization, up to 5 minutes."))}</div>`;
  for (let i = 0; i < 150 && alive(); i++) {
    if (session.status === "success" || session.status === "failed") break;
    await new Promise((resolve) => setTimeout(resolve, 2000));
    if (!alive()) return;
    try {
      session = await api(
        `/api/accounts/login-status?id=${encodeURIComponent(session.id)}`,
      );
    } catch {
      continue;
    }
    if (!alive()) return;
    if (session.auth_url) {
      $("loginURL").value = session.auth_url;
      $("loginOpen").disabled = false;
    }
  }
  if (!alive()) return;
  $("loginStatus").textContent =
    session.status === "success"
      ? L(
          "登录成功，新账号会自动加入账号池。",
          "Signed in. Your account will join the pool automatically.",
        )
      : session.status === "failed"
        ? `${L("登录失败", "Login failed")}: ${session.error || ""}`
        : L(
            "等待已结束。可以关闭窗口后重新发起登录。",
            "The wait has ended. Close this window to start again.",
          );
  if (session.status === "success") {
    toast(L("账号登录成功", "Account signed in"));
    await refresh(["accounts"]);
    setTimeout(() => {
      if (alive()) refresh(["accounts"]);
    }, 11000);
  }
}

function chatPage() {
  return `<section class="card chat-card"><div class="chat-toolbar"><div class="toolbar-group">${icon("models")}<select id="chatModel" class="select" aria-label="${e(L("选择模型", "Choose a model"))}"></select><span class="chat-region" id="chatRegion"></span></div><div class="toolbar-group">${iconButton("settings", L("生成参数", "Generation settings"), "chat-settings")}${iconButton("plus", L("新对话", "New conversation"), "clear-chat")}</div></div><div class="chat-messages" id="chatMessages" aria-label="${e(L("对话内容", "Conversation"))}"></div><form class="chat-compose" id="chatForm"><textarea id="chatInput" placeholder="${e(L("输入消息，测试模型…", "Write a message to try this model…"))}" aria-label="${e(L("消息", "Message"))}">${e(s.chat.draft)}</textarea><div class="compose-actions"><span class="compose-hint">${e(L("Enter 发送 · Shift + Enter 换行", "Enter to send · Shift + Enter for a new line"))}</span><div>${button(L("停止", "Stop"), "stop-chat", "danger", "stop").replace("<button ", '<button id="chatStop" ' + (s.chat.controller ? "" : "hidden "))}<button class="button primary" id="chatSend" type="submit" ${s.chat.controller ? "hidden" : ""} ${!s.chat.draft.trim() ? "disabled" : ""}>${icon("send")}${e(L("发送", "Send"))}</button></div></div></form><div class="chat-footer-note">${e(L("此处的测试使用真实账号额度，调用结果会记录到日志。", "Tests use account quota. Results are recorded in request logs."))}</div></section>`;
}
function updateChatModels() {
  if (!$("chatModel")) return;
  const all = models(),
    ids = all.map(routeId),
    bareIds = [...new Set(all.map((m) => m.id))];
  if (
    !s.chat.model ||
    (!ids.includes(s.chat.model) && !bareIds.includes(s.chat.model))
  )
    s.chat.model = routeId(
      all.find(
        (m) => m.platform === "workbuddy-global" && m.id === "gpt-6.1-sol",
      ) ||
        all.find((m) => m.platform === "workbuddy-global") ||
        all[0] || { id: "" },
    );
  const html =
    (s.cache.models?.platforms || platforms())
      .map(
        (p) =>
          `<optgroup label="${e(platformName(p.id))}">${all
            .filter((m) => m.platform === p.id)
            .map(
              (m) =>
                `<option value="${e(routeId(m))}">${e(routeId(m))}</option>`,
            )
            .join("")}</optgroup>`,
      )
      .join("") +
    `<optgroup label="${e(L("自动路由 · 按平台优先级", "Automatic · platform priority"))}">${bareIds.map((id) => `<option value="${e(id)}">${e(id)}</option>`).join("")}</optgroup>`;
  if ($("chatModel").innerHTML !== html) $("chatModel").innerHTML = html;
  $("chatModel").value = s.chat.model;
  $("chatModel").disabled = !!s.chat.controller;
  $("chatRegion").textContent = s.chat.model.includes("/")
    ? regionName(s.chat.model.split("/")[0])
    : L("自动路由", "Auto route");
}
function welcome() {
  return `<div class="chat-welcome"><div class="welcome-mark">${icon("bolt")}</div><h2>${e(L("从一次对话开始", "Start with a conversation"))}</h2><p>${e(L("选择国内版或国际版模型，直接验证网关的回复与流式输出。", "Select a regional model to test gateway responses and streaming."))}</p><div class="chat-prompts">${[L("用一句话介绍你自己", "Introduce yourself in one sentence"), L("写一个 Python 快速排序", "Write a Python quicksort")].map((text) => `<button class="button" data-action="chat-prompt" data-value="${e(text)}">${e(text)}</button>`).join("")}</div></div>`;
}
function messageHTML(message, index) {
  const assistant = message.role === "assistant";
  return `<article class="message ${assistant ? "assistant" : "user"}" id="message-${index}"><div class="message-meta"><span class="mini-avatar">${assistant ? "A2" : "U"}</span><span>${e(assistant ? "Agent2API" : L("你", "You"))}</span>${assistant ? `<span class="muted mono" style="font-size:10px;font-weight:400">${e(message.model || "")}</span>` : ""}</div><details class="message-reasoning" ${message.reasoning ? "" : "hidden"}><summary>${e(L("思考过程", "Reasoning"))}</summary><div></div></details><div class="message-body"></div><div class="message-tools" hidden></div><div class="message-error" hidden></div><div class="message-footer"></div></article>`;
}
function renderMessages() {
  const host = $("chatMessages");
  if (!host) return;
  if (!s.chat.messages.length) {
    host.innerHTML = welcome();
    return;
  }
  host.innerHTML = s.chat.messages.map(messageHTML).join("");
  s.chat.messages.forEach((message, index) => updateMessage(index, false));
  host.scrollTop = host.scrollHeight;
}
function updateMessage(index, scroll = true) {
  const host = $("chatMessages"),
    message = s.chat.messages[index];
  if (!host || !message) return;
  const nearBottom =
    host.scrollHeight - host.scrollTop - host.clientHeight < 100;
  let element = $(`message-${index}`);
  if (!element) {
    host.querySelector(".chat-welcome")?.remove();
    host.insertAdjacentHTML("beforeend", messageHTML(message, index));
    element = $(`message-${index}`);
  }
  const body = element.querySelector(".message-body");
  if (message.role === "user") body.textContent = message.content;
  else
    body.innerHTML = message.content
      ? markdown(message.content)
      : message.pending
        ? `<span class="spinner"></span> <span class="muted" style="font-size:12px">${e(L("等待回复…", "Waiting for response…"))}</span>`
        : "";
  if (message.role === "assistant" && message.content) renderMath(body);
  const reasoning = element.querySelector(".message-reasoning");
  reasoning.hidden = !message.reasoning;
  reasoning.querySelector("div").textContent = message.reasoning || "";
  const error = element.querySelector(".message-error");
  error.hidden = !message.error;
  error.textContent = message.error || "";
  const tools = element.querySelector(".message-tools");
  tools.hidden = !message.tools?.length;
  tools.textContent = message.tools?.length
    ? L("模型请求了工具调用：", "Model requested tool calls: ") +
      message.tools
        .map((tool) => tool.function?.name || tool.id || "tool")
        .join(", ")
    : "";
  const footer = element.querySelector(".message-footer");
  footer.innerHTML =
    message.role === "assistant" && !message.pending
      ? `${iconButton("copy", L("复制回复", "Copy response"), "copy-message", `data-index="${index}"`)}<span>${e(message.stopped ? L("已停止", "Stopped") : message.error ? L("未完成", "Incomplete") : L("已完成", "Completed"))}</span>${message.duration ? `<span>${e(duration(message.duration))}</span>` : ""}${message.usage ? `<span>${n(message.usage.prompt_tokens)} / ${n(message.usage.completion_tokens)} tokens</span>` : ""}`
      : "";
  if (scroll && nearBottom) host.scrollTop = host.scrollHeight;
}
function syncChatControls() {
  if (!$("chatSend")) return;
  $("chatSend").hidden = !!s.chat.controller;
  $("chatStop").hidden = !s.chat.controller;
  $("chatSend").disabled = !s.chat.draft.trim() || !s.chat.model;
  $("chatInput").disabled = !!s.chat.controller;
  $("chatModel").disabled = !!s.chat.controller;
}
async function sendChat(event) {
  event?.preventDefault();
  if (s.chat.controller || !s.chat.draft.trim() || !s.chat.model) return;
  const start = performance.now(),
    model = s.chat.model,
    params = { ...s.chat.params };
  s.chat.messages.push({ role: "user", content: s.chat.draft.trim() });
  s.chat.draft = "";
  const history = s.chat.messages
    .filter(
      (message) =>
        message.role === "user" ||
        (message.role === "assistant" && message.content && !message.error),
    )
    .map(({ role, content }) => ({ role, content }));
  const index = s.chat.messages.length;
  s.chat.messages.push({
    role: "assistant",
    content: "",
    reasoning: "",
    model,
    pending: true,
    tools: [],
  });
  const controller = new AbortController();
  s.chat.controller = controller;
  if ($("chatInput")) $("chatInput").value = "";
  renderMessages();
  syncChatControls();
  const result = s.chat.messages[index];
  try {
    const request = {
      model,
      messages: params.system
        ? [{ role: "system", content: params.system }, ...history]
        : history,
      stream: true,
      stream_options: { include_usage: true },
      temperature: params.temperature,
      max_tokens: params.max_tokens,
      ...(params.reasoning_effort
        ? { reasoning_effort: params.reasoning_effort }
        : {}),
    };
    const response = await fetch("/v1/chat/completions", {
      method: "POST",
      headers: { ...authHeaders(), "Content-Type": "application/json" },
      body: JSON.stringify(request),
      signal: controller.signal,
    });
    if (!response.ok) {
      if (response.status === 401) s.authRequired = true;
      throw new Error(await responseError(response));
    }
    let completed = false;
    for await (const event of readEvents(response.body)) {
      if (event.done) {
        completed = true;
        break;
      }
      if (event.error)
        throw new Error(event.error.message || String(event.error));
      if (event.usage) result.usage = event.usage;
      for (const choice of event.choices || []) {
        if (choice.index && choice.index !== 0) continue;
        const delta = choice.delta || {};
        if (typeof delta.content === "string") result.content += delta.content;
        if (typeof delta.reasoning_content === "string")
          result.reasoning += delta.reasoning_content;
        for (const call of delta.tool_calls || []) {
          const i = call.index ?? 0;
          result.tools[i] ??= { id: "", function: { name: "", arguments: "" } };
          if (call.id) result.tools[i].id = call.id;
          if (call.function?.name)
            result.tools[i].function.name += call.function.name;
          if (call.function?.arguments)
            result.tools[i].function.arguments += call.function.arguments;
        }
        if (choice.finish_reason) completed = true;
      }
      updateMessage(index);
    }
    if (!completed)
      throw new Error(
        L(
          "连接提前结束，回复可能不完整。",
          "The connection ended before completion. The response may be incomplete.",
        ),
      );
  } catch (error) {
    if (controller.signal.aborted) result.stopped = true;
    else {
      result.error = error.message;
      chrome();
    }
  } finally {
    result.pending = false;
    result.duration = performance.now() - start;
    s.chat.controller = null;
    updateMessage(index);
    syncChatControls();
    if ($("chatInput")) $("chatInput").focus();
  }
}
function chatSettings() {
  const params = s.chat.params;
  modal(
    L("生成参数", "Generation settings"),
    `<form id="chatSettingsForm"><div class="fields-grid"><label class="field"><span>Temperature</span><input id="chatTemperature" type="number" min="0" max="2" step="0.1" required value="${params.temperature}"></label><label class="field"><span>Max tokens</span><input id="chatMaxTokens" type="number" min="1" max="1000000" required value="${params.max_tokens}"></label></div><label class="field"><span>${e(L("思考强度", "Reasoning effort"))}</span><select id="chatEffort">${options(
      [
        ["", L("模型默认", "Model default")],
        ["low", L("低", "Low")],
        ["medium", L("中", "Medium")],
        ["high", L("高", "High")],
      ],
      params.reasoning_effort,
    )}</select></label><label class="field"><span>System prompt</span><textarea id="chatSystem">${e(params.system)}</textarea></label><div class="modal-actions">${button(L("取消", "Cancel"), "close-modal")}<button class="button primary" type="submit">${e(L("保存", "Save"))}</button></div></form>`,
  );
}
function exportRequests() {
  exportCSV(filteredRequests(), [
      "time",
      "platform",
      "model_requested",
      "model",
      "account",
      "protocol",
      "status",
      "duration_ms",
      "input_tokens",
      "output_tokens",
      "error",
    ], "requests");
}
function exportCSV(rows, columns, name) {
  // Protect spreadsheet consumers from formula execution in upstream text.
  const csvCell = (value) => {
    let text = String(value ?? "");
    if (/^[=+@\-\t\r]/.test(text)) text = "'" + text;
    return '"' + text.replace(/"/g, '""') + '"';
  };
  const csv =
    "\uFEFF" +
    [
      columns.join(","),
      ...rows.map((row) => columns.map((key) => csvCell(row[key])).join(",")),
    ].join("\r\n");
  const url = URL.createObjectURL(
    new Blob([csv], { type: "text/csv;charset=utf-8" }),
  );
  const link = document.createElement("a");
  link.href = url;
  link.download = `agent2api-${name}-${new Date().toISOString().slice(0, 10)}.csv`;
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

const views = {
  overview,
  analytics: analyticsPage,
  connection: connectionPage,
  models: modelPage,
  accounts: accountPage,
  platforms: platformPage,
  requests: requestPage,
  settings: settingsPage,
  chat: chatPage,
};
document.addEventListener("click", async (event) => {
  const target = event.target.closest("[data-action]");
  if (!target || target.disabled) return;
  const { action, value, model, index, platform, label, key, kind } =
    target.dataset;
  try {
    switch (action) {
      case "retry":
        return refresh(pageKeys[s.page], true);
      case "command-search":
        return commandSearch();
      case "jump-page":
        $("modal").close();
        location.hash = value;
        return;
      case "trend-range":
        s.trendMinutes = Number(value);
        return draw();
      case "analytics-dimension":
        s.analytics.dimension = value;
        return draw();
      case "export-usage":
        return exportCSV(usageRows(), ["key", "total", "ok", "failed", "input_tokens", "output_tokens", "avg_latency_ms", "success_rate"], s.analytics.dimension);
      case "usage-requests":
        return openRequestFilters({ ...(kind === "by_model" ? { model: value } : kind === "by_protocol" ? { protocol: value } : { account: "label:" + value }) });
      case "account-requests":
        return openRequestFilters({ platform, account: accountKey({ platform, account: label }) });
      case "inspect-failures":
        return openRequestFilters({ status: "failed" });
      case "reset-request-filters":
        return openRequestFilters({});
      case "account-view":
        s.accounts.view = value;
        save("agent2api_account_view", value);
        return draw();
      case "connection-protocol":
        s.connectionProtocol = value;
        return draw();
      case "copy-connection-url":
        return copy(s.connectionProtocol === "anthropic-messages" ? gatewayURL() : `${gatewayURL()}/v1`);
      case "copy-connection-example":
        return copy(connectionExample());
      case "open-platforms":
        location.hash = "platforms";
        return;
      case "auth":
        return authDialog();
      case "close-detail":
        return $("detailDialog").close();
      case "close-modal":
        return $("modal").close();
      case "copy-openai":
        return copy(`${gatewayURL()}/v1`);
      case "copy-base":
        return copy(gatewayURL());
      case "copy-model":
        return copy(model);
      case "model-detail":
        return showModelDetail(model);
      case "try-model":
        if (s.chat.controller)
          return toast(
            L(
              "请先停止当前生成，再切换模型。",
              "Stop the current response before switching models.",
            ),
            true,
          );
        s.chat.model = model;
        $("modal").close();
        save("agent2api_chat_model", model);
        $("detailDialog").close();
        location.hash = "chat";
        if (s.page === "chat") draw(true);
        return;
      case "models-platform":
        s.models.platform = value;
        s.models.page = 1;
        return draw();
      case "accounts-platform":
        s.accounts.platform = value;
        return draw();
      case "platform-models":
        s.models.platform = value;
        s.models.page = 1;
        location.hash = "models";
        return;
      case "model-view":
        s.models.view = value;
        draw();
        if (value === "matrix") return refresh(["matrix", "accounts"]);
        return;
      case "paginate":
        s[kind].page += Number(value);
        if (kind === "models") updateModelTable();
        else updateRequestTable();
        return;
      case "reload-models":
        target.disabled = true;
        await api("/api/config", { refresh_models: true });
        await refresh(["models"]);
        toast(L("模型目录已更新", "Model catalog updated"));
        return;
      case "account-detail":
        $("modal").close();
        return showAccountDetail(Number(index));
      case "request-detail":
        return showRequestDetail(key);
      case "add-account":
        return addAccountDialog();
      case "relogin":
        return startLogin(platform, label);
      case "account-control":
        target.disabled = true;
        await api(
          `/api/accounts/manage?action=${encodeURIComponent(value)}&platform=${encodeURIComponent(platform)}`,
          { label },
        );
        $("detailDialog").close();
        await refresh(["accounts"]);
        toast(L("账号状态已更新", "Account state updated"));
        return;
      case "copy-login":
        return copy($("loginURL")?.value);
      case "open-login": {
        const url = new URL($("loginURL").value);
        if (!["http:", "https:"].includes(url.protocol))
          throw new Error(L("授权链接无效", "Invalid authorization URL"));
        window.open(url.href, "_blank", "noopener,noreferrer");
        return;
      }
      case "chat-settings":
        return chatSettings();
      case "chat-prompt":
        s.chat.draft = value;
        $("chatInput").value = value;
        syncChatControls();
        $("chatInput").focus();
        return;
      case "stop-chat":
        s.chat.controller?.abort();
        return;
      case "clear-chat":
        if (s.chat.controller)
          return toast(L("请先停止当前生成", "Stop the response first"), true);
        s.chat.messages = [];
        renderMessages();
        return;
      case "copy-message":
        return copy(s.chat.messages[Number(index)]?.content);
      case "copy-code":
        return copy(target.dataset.code);
      case "export-requests":
        return exportRequests();
    }
  } catch (error) {
    toast(error.message, true);
    chrome();
  } finally {
    if (target.isConnected) target.disabled = false;
  }
});
document.addEventListener("input", (event) => {
  const { id, value } = event.target;
  if (id === "commandSearch") updateCommandResults(value);
  if (id === "analyticsSearch") {
    s.analytics.query = value;
    updateAnalyticsTable();
  }
  if (id === "modelSearch") {
    s.models.query = value;
    s.models.page = 1;
    updateModelTable();
  }
  if (id === "accountSearch") {
    s.accounts.query = value;
    updateAccountCards();
  }
  if (id === "requestSearch") {
    s.requests.query = value;
    s.requests.page = 1;
    updateRequestTable();
  }
  if (id === "chatInput") {
    s.chat.draft = value;
    syncChatControls();
  }
});
document.addEventListener("change", async (event) => {
  const target = event.target,
    { id, value } = target;
  if (id === "autoRefresh") {
    s.refreshEvery = Number(value);
    save("agent2api_refresh", value);
    s.lastPoll = Date.now();
    return;
  }
  if (id === "analyticsSort") { s.analytics.sort = value; updateAnalyticsTable(); }
  if (id === "modelSort") { s.models.sort = value; s.models.page = 1; updateModelTable(); }
  if (id === "pricingFilter") { s.models.pricing = value; s.models.page = 1; updateModelTable(); }
  if (id === "connectionModel") { s.connectionModel = value; draw(); }
  const requestFilters = { requestModelFilter: "model", requestAccountFilter: "account", requestRangeFilter: "range", requestSort: "sort", requestPageSize: "size" };
  if (requestFilters[id]) {
    s.requests[requestFilters[id]] = id === "requestPageSize" ? Number(value) : value;
    s.requests.page = 1;
    updateRequestTable();
  }
  if (id === "langSel") {
    s.lang = value;
    save("agent2api_lang", value);
    chrome();
    draw(true);
  }
  if (id === "themeSel") {
    s.theme = value;
    save("agent2api_theme", value);
    document.documentElement.dataset.theme = value;
  }
  if (id === "capabilityFilter") {
    s.models.capability = value;
    s.models.page = 1;
    updateModelTable();
  }
  if (id === "accountStatusFilter") {
    s.accounts.status = value;
    updateAccountCards();
  }
  if (id === "requestPlatformFilter") {
    s.requests.platform = value;
    s.requests.page = 1;
    updateRequestTable();
  }
  if (id === "requestStatusFilter") {
    s.requests.status = value;
    s.requests.page = 1;
    updateRequestTable();
  }
  if (id === "requestProtocolFilter") {
    s.requests.protocol = value;
    s.requests.page = 1;
    updateRequestTable();
  }
  if (id === "chatModel") {
    s.chat.model = value;
    save("agent2api_chat_model", value);
    updateChatModels();
  }
  if (id === "trendSelect") {
    s.trend = value;
    draw();
  }
  if (id === "usageSelect") {
    s.usage = value;
    draw();
  }
  if (target.dataset.sanitize) {
    target.disabled = true;
    try {
      await api(
        `/api/config?platform=${encodeURIComponent(target.dataset.sanitize)}`,
        { sanitize: target.checked },
      );
      await refresh(["config"]);
      toast(L("兼容设置已更新", "Setting updated"));
    } catch (error) {
      target.checked = !target.checked;
      toast(error.message, true);
    } finally {
      target.disabled = false;
    }
  }
});
document.addEventListener("submit", (event) => {
  if (event.target.id === "chatForm") sendChat(event);
  if (event.target.id === "authForm") saveAuth(event);
  if (event.target.id === "addAccountForm") {
    event.preventDefault();
    startLogin($("loginPlatform").value);
  }
  if (event.target.id === "chatSettingsForm") {
    event.preventDefault();
    s.chat.params = {
      temperature: Number($("chatTemperature").value),
      max_tokens: Number($("chatMaxTokens").value),
      reasoning_effort: $("chatEffort").value,
      system: $("chatSystem").value,
    };
    $("modal").close();
    toast(L("生成参数已保存", "Generation settings saved"));
  }
});
document.addEventListener("keydown", (event) => {
  if (event.target.matches("[data-chart-point]") && ["ArrowLeft", "ArrowRight"].includes(event.key)) {
    event.preventDefault();
    const hits = [...event.target.closest("svg").querySelectorAll("[data-chart-point]")];
    const index = hits.indexOf(event.target) + (event.key === "ArrowRight" ? 1 : -1);
    hits[Math.max(0, Math.min(hits.length - 1, index))].focus();
    return;
  }
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") {
    event.preventDefault();
    return commandSearch();
  }
  if (event.target.id === "commandSearch" && event.key === "Enter") {
    event.preventDefault();
    $("commandResults")?.querySelector("button")?.click();
  }
  if (event.target.matches("tr.request-click") && ["Enter", " "].includes(event.key)) {
    event.preventDefault();
    return showRequestDetail(event.target.dataset.key);
  }
  if (
    event.target.id === "chatInput" &&
    event.key === "Enter" &&
    !event.shiftKey &&
    !event.isComposing
  ) {
    event.preventDefault();
    sendChat();
  }
  if (event.key === "Escape") {
    $("sidebar").classList.remove("open");
    $("menuButton").setAttribute("aria-expanded", "false");
  }
});
$("modal").addEventListener("close", () => loginGeneration++);
for (const id of ["detailDialog", "modal"])
  $(id).addEventListener("click", (event) => {
    const bounds = $(id).getBoundingClientRect();
    if (
      event.target === $(id) &&
      (event.clientX < bounds.left ||
        event.clientX > bounds.right ||
        event.clientY < bounds.top ||
        event.clientY > bounds.bottom)
    )
      $(id).close();
  });
$("menuButton").addEventListener("click", () => {
  $("sidebar").classList.toggle("open");
  $("menuButton").setAttribute(
    "aria-expanded",
    String($("sidebar").classList.contains("open")),
  );
});
document.addEventListener("click", (event) => {
  if (!event.target.closest("#sidebar, #menuButton")) {
    $("sidebar").classList.remove("open");
    $("menuButton").setAttribute("aria-expanded", "false");
  }
});
$("refreshButton").addEventListener("click", () =>
  refresh(
    s.page === "models" && s.models.view === "matrix"
      ? ["models", "matrix", "accounts"]
      : pageKeys[s.page],
    true,
  ),
);
$("copyEndpoint").addEventListener("click", () => copy(`${gatewayURL()}/v1`));
window.addEventListener("hashchange", navigate);
document.addEventListener("visibilitychange", () => {
  if (!document.hidden)
    refresh(s.page === "chat" ? ["status"] : pageKeys[s.page]);
});
setInterval(() => {
  if (document.hidden || $("modal").open || !s.refreshEvery || Date.now() - s.lastPoll < s.refreshEvery * 1000) return;
  const keys =
    s.page === "overview"
      ? ["status", "metrics", "accounts"]
      : ["requests", "analytics"].includes(s.page)
        ? ["metrics"]
        : s.page === "accounts"
          ? ["accounts", "metrics"]
          : ["status"];
  refresh(keys);
}, 1000);
setInterval(() => {
  document.querySelectorAll("[data-until]").forEach((element) => {
    const remaining = Math.max(
      0,
      (Number(element.dataset.until) - Date.now()) / 1000,
    );
    element.textContent =
      remaining > 0
        ? countdown(remaining)
        : L("等待状态更新", "Awaiting update");
  });
}, 1000);
navigate();
// Catalog and account data also populate the global navigation and chat selector.
refresh(["models", "accounts", "config", "platforms", "metrics"]);
