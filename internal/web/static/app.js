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
import { pageResources, refreshResources, filterModelCatalog, compareModelIdentity, remainingCooldown, availabilityState, pageSnapshotTime } from "./resources.js";
import { dashboardMetrics, accountReadiness, usageShares } from "./dashboard.js";
import { requestIssue, imageRecordSummary, activeFilterKeys, credentialState } from "./operations.js";

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
  sidebarCollapsed: saved("agent2api_sidebar_collapsed") === "true",
  density: saved("agent2api_density", "comfortable") === "compact" ? "compact" : "comfortable",
  key: saved("agent2api_key"),
  page: "overview",
  cache: {},
  receivedAt: {},
  errors: {},
  busy: new Set(),
  authRequired: false,
  trend: "requests",
  usage: "by_model",
  trendMinutes: 30,
  dashboardScope: "all",
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
      "掌握调用规模、流量变化与账号调度状态。",
      "Monitor request volume, traffic patterns and account readiness.",
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
      "按平台、能力与费率寻找模型，查看目录支持与账号状态。",
      "Find models by platform, capability and credit rate. Inspect catalogue support and account status.",
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
const pageKeys = pageResources;
const currentResources = (background = false) => refreshResources(s.page, s.models.view === "matrix", background);
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
const imageOverflowName = (policy) => policy === "trim_oldest" ? L("清理最旧图片，保留最近图片", "Omit oldest images, keep newest") : L("拒绝超限请求", "Reject overflow requests");
const regionName = (id) =>
  id === "workbuddy"
    ? L("国内版", "China")
    : id === "workbuddy-global"
      ? L("国际版", "Global")
      : id === "(别名)" ? L("别名", "Alias") : id || "—";
const platformMark = (id) => id === "workbuddy" ? "CN" : id === "workbuddy-global" ? "GL" : String(id || "AP").replace(/[^a-z0-9]/gi, "").slice(0, 2).toUpperCase();
const regionTag = (id) =>
  `<span class="tag region-tag">${e(regionName(id))}</span>`;
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
    const error = new Error(await responseError(response, s.lang));
    error.status = response.status;
    throw error;
  }
  return response.json();
}
async function refresh(keys = currentResources(), manual = false) {
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
            if (snapshot.ok) { data.models = mergePricingSnapshot(data.models, await snapshot.json()); data.pricing_snapshot = true; }
          } catch {}
        }
        s.cache[key] = data;
        s.receivedAt[key] = Date.now();
        delete s.errors[key];
        if (key === "status") {
          s.updated = new Date();
        }
      } catch (error) {
        s.errors[key] = error;
      } finally {
        s.busy.delete(key);
      }
    }),
  );
  s.authRequired = Object.values(s.errors).some((error) => error?.status === 401);
  $("refreshButton").classList.toggle("refreshing", s.busy.size > 0);
  chrome();
  draw();
}
function chrome() {
  document.documentElement.lang = s.lang === "en" ? "en" : "zh-CN";
  document.body.dataset.page = s.page;
  document.body.dataset.nav = s.sidebarCollapsed ? "compact" : "expanded";
  document.body.dataset.density = s.density;
  $("sidebarToggle").innerHTML = icon("panel");
  $("sidebarToggle").setAttribute("aria-label", s.sidebarCollapsed ? L("展开侧栏", "Expand sidebar") : L("收起侧栏", "Collapse sidebar"));
  $("sidebarToggle").setAttribute("aria-expanded", String(!s.sidebarCollapsed));
  $("brandSubtitle").textContent = L("统一模型网关", "UNIFIED MODEL GATEWAY");
  $("navCaption").textContent = L("工作空间", "WORKSPACE");
  const navSignature = `${s.page}|${s.lang}|${models().length}|${accounts().length}`;
  if (chrome.navSignature !== navSignature)
    $("nav").innerHTML = [
      [L("运行监控", "MONITORING"), ["overview", "requests", "analytics"]],
      [L("资源管理", "RESOURCES"), ["accounts", "platforms", "models"]],
      [L("工具与配置", "TOOLS & SETTINGS"), ["chat", "connection", "settings"]],
    ].map(([title, pages]) => `<div class="nav-group-label">${e(title)}</div>${pages.map((page) => `<a class="nav-item ${page === s.page ? "active" : ""}" href="#${page}" aria-label="${e(page === "overview" ? L("概览", "Overview") : names[page]())}" title="${e(page === "overview" ? L("概览", "Overview") : names[page]())}" ${page === s.page ? 'aria-current="page"' : ""}>${icon(page === "analytics" ? "activity" : page === "connection" ? "external" : page)}<span>${e(page === "overview" ? L("概览", "Overview") : names[page]())}</span>${["models", "accounts"].includes(page) && s.cache[page] ? `<span class="nav-count">${page === "models" ? models().length : accounts().length}</span>` : ""}</a>`).join("")}`).join("");
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
  const primarySignature = `${s.page}|${s.lang}`;
  if (chrome.primarySignature !== primarySignature) {
    $("pagePrimary").innerHTML = ["accounts", "platforms"].includes(s.page) ? button(L("添加账号", "Add account"), "add-account", "primary", "plus") : s.page === "requests" ? button(L("查看失败调用", "Inspect failures"), "inspect-failures", "", "info") : ["overview", "models"].includes(s.page) ? `<a class="button primary" href="#connection">${icon("external")}${e(L("接入客户端", "Connect a client"))}</a>` : "";
    chrome.primarySignature = primarySignature;
  }
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
  $("healthText").parentElement.title = $("healthText").textContent;
  $("connectionBadge").className =
    `connection-badge ${connected ? "online" : "offline"}`;
  $("connectionBadge").textContent = connected
    ? L("反代服务运行中", "Gateway online")
    : L("连接需要检查", "Connection needs attention");
  const pageTime = pageSnapshotTime(s.page, s.receivedAt, s.models.view === "matrix");
  $("updatedAt").textContent = pageTime ? `${L("数据更新", "Data updated")} ${timestamp(pageTime)}` : L("数据待加载", "Loading data");
  $("updatedAt").title = pageTime ? timestamp(pageTime, true) : "";
  const pageError = [
    ...new Set([
      "status",
      ...currentResources(),
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
  refresh(currentResources());
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
  const samePage = $("pageContent").dataset.page === s.page,
    scrollContainer = samePage ? $("pageContent").querySelector(".table-wrap") : null,
    scroll = { left: scrollContainer?.scrollLeft || 0, top: scrollContainer?.scrollTop || 0 },
    scrollFocused = active === scrollContainer,
    advancedOpen = samePage ? $("requestAdvanced")?.open : undefined;
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
  if (table) {
    table.scrollLeft = scroll.left;
    table.scrollTop = scroll.top;
    if (scrollFocused) table.focus({ preventScroll: true });
  }
  if ($("requestAdvanced") && advancedOpen != null) $("requestAdvanced").open = advancedOpen;
  $("pageContent").dataset.page = s.page;
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
  const axis = (kind === "requests" ? scale === 1 ? [0, 1] : [0, Math.ceil(scale / 2) / scale, 1] : [0, 0.5, 1])
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
  const points = a.some((value) => value > 0) ? values
    .map(
      (row, index) =>
        `<circle class="chart-point" cx="${x(index)}" cy="${y(a[index])}" r="2" fill="var(--accent)"><title>${e(timestamp(row.minute * 60000))} · ${e(kind === "requests" ? L("成功 / 失败", "Success / Failed") : L("输入 / 输出 Token", "Input / output tokens"))}: ${a[index]} / ${b[index]}</title></circle>`,
    )
    .join("") : "";
  const width = 552 / Math.max(1, values.length - 1);
  const hits = values.map((row, index) => `<rect class="chart-hit" tabindex="0" role="button" aria-label="${e(`${timestamp(row.minute * 60000)} · ${a[index]} / ${b[index]}`)}" data-chart-point="${index}" data-x="${x(index)}" data-time="${e(timestamp(row.minute * 60000))}" data-a="${a[index]}" data-b="${b[index]}" data-kind="${kind}" x="${Math.max(36, x(index) - width / 2)}" y="20" width="${Math.min(width, 588 - Math.max(36, x(index) - width / 2))}" height="148"/>`).join("");
  return `<div class="chart-shell"><svg class="chart" viewBox="0 0 610 200" aria-label="${e(L(`最近 ${s.trendMinutes} 分钟趋势`, `Last ${s.trendMinutes} minutes`))}">${axis}<path class="chart-area" d="${line(a)} L588,161 L36,161 Z"/><path class="chart-line" d="${line(a)}"/>${b.some((value) => value > 0) ? `<path class="chart-line red" d="${line(b)}"/>` : ""}${points}${times}<line class="chart-crosshair" x1="36" x2="36" y1="20" y2="161" hidden/>${hits}</svg><div class="chart-tooltip" hidden role="status"></div></div>`;
}
function platformSummary() {
  return (
    platforms()
      .map((p) => {
        const pool = accounts().filter((a) => a.platform === p.id),
          health = accountReadiness(pool);
        const label = !s.cache.accounts ? L("待加载", "Loading") : !pool.length ? L("未配置", "No accounts") : !health.enabled ? L("未启用", "Inactive") : !health.ready ? L("需关注", "Attention") : health.attention ? L("部分可用", "Partial") : L("可调度", "Ready");
        return `<a class="platform-summary" href="#platforms"><div class="platform-avatar ${p.id === "workbuddy-global" ? "global" : ""}">${e(platformMark(p.id))}</div><div class="platform-label"><strong>${e(platformName(p.id))}</strong><small>${n(models().filter((m) => m.platform === p.id).length || p.models || 0)} ${e(L("个模型", "models"))} · ${s.cache.accounts ? `${health.ready}/${health.enabled}` : "—"} ${e(L("就绪 / 启用", "ready / enabled"))}</small></div>${tag(label, health.ready && !health.attention ? "green" : "amber", true)}</a>`;
      })
      .join("") || empty(L("暂无平台", "No platforms"))
  );
}

function ranking(rows) {
  const top = usageShares(rows);
  if (!top.length) return empty(L("暂无调用分布", "No usage breakdown"), L("有调用记录后显示累计分布。", "Cumulative breakdown appears after traffic is recorded."), "activity");
  return `<div class="usage-ranking">${top.map((row, index) => `<button class="usage-ranking-row" data-action="usage-requests" data-kind="${e(s.usage)}" data-value="${e(row.key)}" title="${e(row.key)}"><span class="ranking-index">${index + 1}</span><div class="ranking-detail"><div><span class="truncate">${e(s.usage === "by_protocol" ? protocolName(row.key) : row.key)}</span><strong>${n(row.total)}<small>${e(L("次", "calls"))}</small></strong></div><div class="ranking-track"><i style="width:${row.share * 100}%"></i></div></div><span class="ranking-share">${(row.share * 100).toFixed(1)}%</span>${icon("chevron")}</button>`).join("")}</div>`;
}
function dashboardCard(label, glyph, value, note, tone = "blue", unit = "") {
  return `<article class="card dashboard-kpi ${tone}"><div class="dashboard-kpi-heading"><span>${e(label)}</span>${icon(glyph)}</div><div class="dashboard-kpi-value">${e(value)}${unit ? `<small>${e(unit)}</small>` : ""}</div><div class="dashboard-kpi-note">${note}</div></article>`;
}
function operationalNotices(pool, metrics) {
  const readiness = accountReadiness(pool);
  const failures = (metrics?.recent || []).filter(row => !row.ok);
  const attention = readiness?.attention || 0;
  return `<section class="operations-notices" aria-label="${e(L("运行提醒", "Operational notices"))}"><div class="operations-notices-heading">${icon(attention || failures.length || !readiness?.enabled ? "info" : "check")}<div><strong>${e(L("运行提醒", "Operational notices"))}</strong><small>${e(L("当前账号状态 · 最近调用样本", "Current accounts · recent request sample"))}</small></div></div><button data-action="attention-accounts" ${!readiness ? "disabled" : ""}><span class="notice-value ${attention ? "negative" : "positive"}">${readiness ? n(attention) : "—"}</span><span><strong>${e(L("需关注账号", "Accounts needing attention"))}</strong><small>${e(readiness ? attention ? L("检查登录与调度状态", "Review sign-in and scheduling") : readiness.enabled ? L("启用账号当前均就绪", "Enabled accounts are ready") : L("没有已启用账号", "No enabled accounts") : L("等待账号数据", "Waiting for accounts"))}</small></span>${icon("arrow")}</button><button data-action="inspect-failures" ${!metrics ? "disabled" : ""}><span class="notice-value ${failures.length ? "negative" : ""}">${metrics ? n(failures.length) : "—"}</span><span><strong>${e(L("最近样本失败", "Recent sample failures"))}</strong><small>${e(metrics ? L(`共 ${metrics.recent?.length || 0} 条记录 · 查看原因`, `${metrics.recent?.length || 0} records · inspect causes`) : L("等待调用数据", "Waiting for requests"))}</small></span>${icon("arrow")}</button></section>`;
}

function overview() {
  const m = s.cache.metrics, status = s.cache.status;
  const pool = s.cache.accounts ? accounts() : null;
  const readiness = accountReadiness(pool);
  const metrics = dashboardMetrics(m, s.dashboardScope, s.trendMinutes);
  const window = dashboardMetrics(m, "window", s.trendMinutes);
  const currentMinute = m?.series?.at(-1)?.total;
  const recent = [...(m?.recent || [])].sort((a, b) => new Date(b.time) - new Date(a.time)).slice(0, 5);
  const sample = summarizeRequests(m?.recent || []);
  const scope = s.dashboardScope === "all" ? L("累计统计", "Cumulative totals") : L(`最近 ${s.trendMinutes} 分钟`, `Last ${s.trendMinutes} minutes`);
  const totalTokens = metrics ? metrics.input_tokens + metrics.output_tokens : null;
  const value = (v, formatter = n) => v == null ? "—" : formatter(v);
  const connected = status && !s.errors.status;
  const trendControl = `<div class="chart-caption"><span><i class="legend-dot"></i>${e(s.trend === "requests" ? L("成功", "Success") : L("输入", "Input"))}</span><span><i class="legend-dot red"></i>${e(s.trend === "requests" ? L("失败", "Failed") : L("输出", "Output"))}</span><select id="trendSelect" class="select" aria-label="${e(L("趋势类型", "Trend type"))}">${options([["requests", L("调用", "Requests")], ["tokens", "Token"]], s.trend)}</select></div>`;
  return `<div class="dashboard">
  <div class="gateway-pulse"><span class="pulse-state ${connected ? "positive" : "negative"}"><span class="status-dot ${connected ? "online" : "offline"}"></span>${e(connected ? L("网关在线", "Gateway online") : status ? L("连接异常", "Connection issue") : L("连接中", "Connecting"))}</span><span>${e(L("运行时间", "Uptime"))}<strong>${status ? e(countdown(status.uptime_sec || 0)) : "—"}</strong></span><span>${e(L("实时并发", "In flight"))}<strong>${value(m?.in_flight)}</strong></span><span>${e(L("本分钟已完成", "Completed this minute"))}<strong>${value(currentMinute)}</strong></span></div>
  ${operationalNotices(pool, m)}
  <div class="dashboard-section-head"><div><h2>${e(L("核心指标", "Key metrics"))}<span class="scope-pill">${e(scope)}</span></div><div class="segmented dashboard-scope" aria-label="${e(L("指标统计范围", "Metric scope"))}">${[["all", L("累计", "All time")], ["15", L("15 分钟", "15 min")], ["30", L("30 分钟", "30 min")]].map(([id, label]) => `<button data-action="dashboard-scope" data-value="${id}" aria-pressed="${id === "all" ? s.dashboardScope === "all" : s.dashboardScope === "window" && s.trendMinutes === Number(id)}" class="${id === "all" ? s.dashboardScope === "all" ? "active" : "" : s.dashboardScope === "window" && s.trendMinutes === Number(id) ? "active" : ""}">${e(label)}</button>`).join("")}</div></div>
  <div class="dashboard-kpis">
    ${dashboardCard(L("完成调用", "Completed requests"), "activity", value(metrics?.total), metrics ? `${e(L("成功", "Success"))} <strong>${n(metrics.ok)}</strong><span class="kpi-separator">/</span>${e(L("失败", "Failed"))} <strong class="${metrics.failed ? "negative" : ""}">${n(metrics.failed)}</strong>` : e(L("等待用量数据", "Waiting for metrics")))}
    ${dashboardCard(L("调用成功率", "Success rate"), "check", value(metrics?.success_rate, (v) => `${(v * 100).toFixed(1)}%`), metrics?.success_rate != null ? `<span class="kpi-meter" aria-hidden="true"><i style="width:${Math.max(0, Math.min(100, metrics.success_rate * 100))}%"></i></span>${e(L("按完成调用计算", "Of completed requests"))}` : e(L("当前范围尚无完成调用", "No completed requests in this scope")), "green")}
    ${dashboardCard(L("Token 用量", "Token usage"), "tokens", value(totalTokens, compact), metrics ? `${e(L("输入", "In"))} <strong title="${n(metrics.input_tokens)}">${compact(metrics.input_tokens)}</strong><span class="kpi-separator">/</span>${e(L("输出", "Out"))} <strong title="${n(metrics.output_tokens)}">${compact(metrics.output_tokens)}</strong>` : e(L("等待用量数据", "Waiting for metrics")), "violet")}
    ${dashboardCard(L("平均生成速度", "Generation speed"), "bolt", value(metrics?.avg_tps, (v) => v.toFixed(1)), metrics?.avg_tps != null ? e(s.dashboardScope === "all" ? L(`${n(metrics.tps_samples)} 个有效测速样本`, `${n(metrics.tps_samples)} measured requests`) : L("按有效生成时长加权", "Weighted by measured generation time")) : e(L("当前范围尚无测速样本", "No measured generation in this scope")), "amber", metrics?.avg_tps != null ? "tok/s" : "")}
  </div><p class="dashboard-scope-note">${e(s.dashboardScope === "all" ? L("累计数据可跨重启保留；下方流量趋势单独使用分钟窗口。", "Totals can persist across restarts. The traffic chart uses a separate minute window.") : L("核心指标与下方趋势使用同一组分钟桶，当前分钟仍在累计。", "Key metrics and traffic use the same minute buckets; the current minute is still accumulating."))}</p>
  <div class="dashboard-main-grid">
    <section class="card dashboard-traffic" data-trend="${e(s.trend)}">${cardHead(L("流量趋势", "Traffic activity"), L(`最近 ${s.trendMinutes} 分钟 · 每分钟聚合`, `Last ${s.trendMinutes} minutes · minute buckets`), trendControl)}
      <div class="traffic-totals"><div><span>${e(L("窗口完成调用", "Window requests"))}</span><strong>${value(window?.total)}</strong></div><div><span>${e(L("平均每分钟", "Average per minute"))}</span><strong>${value(window?.average_rpm, (v) => v.toFixed(1))}<small>RPM</small></strong></div><div><span>${e(L("窗口 Token", "Window tokens"))}</span><strong>${window ? compact(window.input_tokens + window.output_tokens) : "—"}</strong></div><div class="segmented traffic-range">${[15, 30].map((minutes) => `<button data-action="trend-range" data-value="${minutes}" aria-pressed="${s.trendMinutes === minutes}" class="${s.trendMinutes === minutes ? "active" : ""}">${minutes} ${e(L("分钟", "min"))}</button>`).join("")}</div></div>
      <div class="dashboard-chart">${chart(m?.series, s.trend)}${window?.series.length && !window.total ? `<div class="chart-idle"><span>${icon("activity")}</span><strong>${e(L("这个时段还没有完成调用", "No completed requests in this window"))}</strong><small>${e(L("新调用完成后，趋势会自动更新", "Traffic updates as new requests complete"))}</small></div>` : ""}</div>
      <div class="chart-footnote"><span>${icon("clock")}${e(L("当前分钟未结束", "Current minute is incomplete"))}</span><span>${e(L("悬停或聚焦查看分钟读数", "Hover or focus for minute details"))}</span></div>
    </section>
    <section class="card dashboard-platforms">${cardHead(L("平台状态", "Platform health"), L("账号池独立调度", "Independent account pools"), `<a class="text-link" href="#platforms">${e(L("管理", "Manage"))}${icon("arrow")}</a>`)}
      <div class="pool-readiness"><div><span>${e(L("就绪账号", "Ready accounts"))}</span><strong>${readiness ? `${readiness.ready}<small>/ ${readiness.enabled}</small>` : "—"}</strong></div>${tag(!readiness ? L("待加载", "Loading") : readiness.attention ? L("需关注", "Attention") : readiness.enabled ? L("可调度", "Ready") : L("未启用", "Inactive"), readiness?.enabled && !readiness.attention ? "green" : "amber", true)}<div class="pool-segments" aria-label="${e(L("账号状态分布", "Account state distribution"))}">${(pool || []).map((a) => `<i class="${a.state === "disabled" ? "disabled" : accountReadiness([a]).ready ? "ready" : "attention"}" title="${e(`${a.label} · ${accountStatus(a)[0]}`)}"></i>`).join("")}</div><small>${e(readiness ? L(`${readiness.attention} 需关注 · ${readiness.disabled} 已停用`, `${readiness.attention} need attention · ${readiness.disabled} disabled`) : L("等待账号数据", "Waiting for account data"))}</small></div>
      <div class="dashboard-platform-list">${platformSummary()}</div><div class="platform-inventory"><span>${e(L("已接入平台", "Platforms"))}<strong>${status ? platforms().length : "—"}</strong></span><span>${e(L("模型条目", "Model entries"))}<strong>${s.cache.models ? models().length : "—"}</strong></span></div>
    </section>
  </div>
  <div class="dashboard-detail-grid">
    <section class="card dashboard-performance">${cardHead(L("响应表现", "Response performance"), L(`最近记录样本 · ${sample.total} / 200 条`, `Recent sample · ${sample.total} / 200 records`), `<a class="text-link" href="#requests">${e(L("分析调用", "Inspect requests"))}${icon("arrow")}</a>`)}<div class="performance-values"><div><span>${e(L("典型完整耗时", "Typical full duration"))}<b>P50</b></span><strong>${e(duration(sample.p50))}</strong></div><div><span>${e(L("长尾完整耗时", "Tail full duration"))}<b>P95</b></span><strong>${e(duration(sample.p95))}</strong></div></div><div class="performance-baseline"><span>${e(L("累计平均完整耗时", "Cumulative mean duration"))}</span><strong>${e(duration(m?.total ? m.avg_latency_ms : null))}</strong></div><div class="sample-observed">${icon("clock")}${e(recent.length ? L(`样本最新记录：${timestamp(recent[0].time, true)}`, `Latest record: ${timestamp(recent[0].time, true)}`) : L("尚无调用记录", "No request records"))}</div><p class="dashboard-panel-note">${e(L("完整耗时包含连接与回复生成；P50 / P95 仅来自最近记录样本，不随上方统计范围变化。", "Full duration includes connection and generation. P50 / P95 use only recent request records and do not follow the metric scope."))}</p></section>
    <section class="card dashboard-distribution">${cardHead(L("调用分布", "Request distribution"), L("累计调用占比 · 前四项", "Cumulative request share · top four"), `<select id="usageSelect" class="select" aria-label="${e(L("统计维度", "Usage dimension"))}">${options([["by_model", L("模型", "Model")], ["by_protocol", L("协议", "Protocol")], ["by_account", L("账号", "Account")]], s.usage)}</select>`)}${ranking(m?.[s.usage])}<p class="dashboard-panel-note">${e(s.usage === "by_protocol" ? L("点击条目查看对应的最近调用记录。", "Select a row to inspect its recent requests.") : L("同名模型或账号标签可能跨平台合并；点击查看对应记录。", "Identical model or account labels may combine platforms. Select a row to inspect records."))}</p></section>
  </div>

  <section class="card dashboard-recent">${cardHead(L("最近调用", "Recent requests"), L("实际路由、账号与完整耗时", "Actual route, account and full duration"), `<a class="text-link" href="#requests">${e(L("查看记录", "View records"))}${icon("arrow")}</a>`)}${requestTable(recent, false)}</section>
  </div>`;
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
function densityControl() {
  return `<button class="button small density-control" data-action="toggle-density" aria-pressed="${s.density === "compact"}" title="${e(L("切换列表行间距", "Toggle table row spacing"))}">${icon("rows")}${e(s.density === "compact" ? L("紧凑", "Compact") : L("舒适", "Comfortable"))}</button>`;
}
function modelPage() {
  return `<section class="card resource-workspace model-workspace"><div class="workspace-controls">
  <div class="toolbar catalog-platform-toolbar"><div class="toolbar-group">${platformTabs("models", s.models.platform)}</div><div class="toolbar-group">${button(L("更新模型清单", "Refresh catalogue"), "reload-models", "", "refresh")}</div></div>
  <div class="toolbar catalog-filter-toolbar"><div class="toolbar-group">${search("modelSearch", s.models.query, L("搜索模型名称或 ID…", "Search model name or ID…"))}<select id="capabilityFilter" class="select" aria-label="${e(L("能力筛选", "Capability filter"))}">${options(
    [
      ["all", L("全部能力", "All capabilities")],
      ["supports_images", L("视觉", "Vision")],
      ["supports_tools", L("工具调用", "Tools")],
      ["supports_thinking", L("思考", "Reasoning")],
    ],
    s.models.capability,
  )}</select><select id="pricingFilter" class="select" aria-label="${e(L("费率筛选", "Rate filter"))}" ${s.models.view === "matrix" ? "disabled" : ""}>${options([["all", L("全部费率", "All rates")], ["free", L("零积分", "Zero credits")], ["credits", L("积分计费", "Uses credits")], ["dynamic", L("浮动倍率", "Variable rate")], ["unknown", L("费率未知", "Unknown rate")]], s.models.pricing)}</select><select id="modelSort" class="select" aria-label="${e(L("模型排序", "Model sort"))}" ${s.models.view === "matrix" ? "disabled" : ""}>${options([["name", L("名称排序", "By name")], ["rate", L("积分倍率从低到高", "Lowest credit rate")], ["context", L("上下文优先", "Largest context")], ["output", L("输出上限优先", "Largest output")]], s.models.sort)}</select></div><div class="segmented"><button data-action="model-view" data-value="list" class="${s.models.view === "list" ? "active" : ""}">${e(L("模型目录", "Catalog"))}</button><button data-action="model-view" data-value="matrix" class="${s.models.view === "matrix" ? "active" : ""}">${e(L("账号可用性", "Availability"))}</button></div></div>
  </div><div id="modelInsights"></div><div class="resource-table" id="modelResults"></div></section><div class="catalog-route-hint">${icon("models")}<span>${e(L("平台前缀固定来源，无前缀按优先级路由。", "A platform prefix pins the source; bare IDs use priority routing."))}</span><a class="text-link" href="#platforms">${e(L("路由规则", "Routing policy"))}${icon("arrow")}</a></div><p class="scope-note">${e(L("费率直接读取上游模型配置，不发送生成请求。× 值为积分消耗倍率，不是金额单价；零积分仍受账号权限与用量限制。详情中可查看原始标注和读取时间，超过 24 小时未更新的费率归为未知。", "Rates come from upstream configuration without generation requests. Multipliers are credit usage rates, not currency prices. Zero-credit models still have account and usage limits. Details show original labels and observation times; rates older than 24 hours are treated as unknown."))}</p>`;
}
function pricingName(status) {
  return ({ free: L("零积分", "Zero credits"), credits: L("积分计费", "Uses credits"), dynamic: L("浮动倍率", "Variable rate"), unknown: L("费率未知", "Unknown rate") })[status] || L("费率未知", "Unknown rate");
}
function updateModelInsights() {
  if (!$("modelInsights")) return;
  const matching = filterModelCatalog(models(), s.models), matrix = s.models.view === "matrix";
  const counts = { free: 0, credits: 0, dynamic: 0, unknown: 0 };
  for (const model of matching) counts[modelPricing(model).status]++;
  const times = matching.map((m) => Date.parse(modelPricing(m).observedAt || "")).filter(Number.isFinite);
  const filters = [s.models.query.trim(), s.models.platform !== "all", s.models.capability !== "all", !matrix && s.models.pricing !== "all"].filter(Boolean).length;
  const displayed = !matrix && s.models.pricing !== "all" ? counts[s.models.pricing] || 0 : matching.length;
  $("modelInsights").innerHTML = `<div class="catalog-summary">${Object.entries(counts).map(([key,count]) => `<button class="card catalog-summary-item ${!matrix && s.models.pricing === key ? "selected" : ""}" data-action="model-summary-filter" data-value="${key}" aria-pressed="${!matrix && s.models.pricing === key}" ${matrix ? "disabled" : ""}><span>${e(pricingName(key))}</span><strong>${n(count)}</strong><small>${e(key === "free" ? L("倍率为零，仍有用量限制", "Zero rate, usage limits apply") : key === "credits" ? L("显示上游积分倍率", "Upstream credit multipliers") : key === "dynamic" ? L("随自动选择的模型变化", "Depends on automatic selection") : L("缺失或已过期的信息", "Missing or stale metadata"))}</small></button>`).join("")}</div>
  <div class="catalog-context"><span><strong>${e(matrix ? L("账号可用性", "Account availability") : L("模型列表", "Model list"))}</strong><span class="result-count">${n(displayed)} / ${n(models().length)}${!matrix && s.models.pricing !== "all" ? ` · ${e(pricingName(s.models.pricing))}` : ""}</span></span><div class="toolbar-group">${filters ? `<button class="button ghost small" data-action="reset-model-filters">${icon("close")}${e(L("清除筛选", "Reset filters"))} (${filters})</button>` : ""}${densityControl()}</div></div>
  <div class="catalog-source">${icon("clock")}<span>${times.length ? e(L("最早费率记录：", "Oldest rate observation: ") + timestamp(Math.min(...times), true)) : e(L("没有可核实的费率记录", "No verified rate metadata"))}${s.cache.models?.pricing_snapshot ? ` · ${e(L("费率为读取快照，更新模型清单不会更新费率", "Rates use an observed snapshot; catalogue refresh does not refresh rates"))}` : ""}</span></div>`;
}
function pricingCell(m) {
  const p = modelPricing(m), rate = p.multiplier;
  const value = p.status === "dynamic" ? L("随所选模型变化", "Depends on selected model") : rate !== undefined ? `${p.stale ? L("上次 ", "Previously ") : ""}×${String(rate)}` : "—";
  return `<div title="${e(p.observedAt ? L("费率读取：", "Rate observed: ") + timestamp(p.observedAt, true) : L("无已核实费率", "No verified rate"))}">${tag(pricingName(p.status), p.status === "free" ? "green" : p.status === "credits" ? "blue" : "outline")}<div class="cell-sub mono">${e(value)}</div><div class="cell-sub">${p.stale ? e(L("需更新费率", "Rate needs refresh")) : p.status === "unknown" ? e(L("无固定倍率", "No fixed rate")) : ""}</div></div>`;
}
function updateModelTable() {
  if (!$("modelResults")) return;
  updateModelInsights();
  if (s.models.view === "matrix") return renderMatrix();
  const filtered = filterModelCatalog(models(), s.models).filter((m) => s.models.pricing === "all" || modelPricing(m).status === s.models.pricing);
  const platformOrder = platforms().map((p) => p.id);
  filtered.sort((a, b) => (s.models.sort === "rate" ? compareModelRates(a, b) : s.models.sort === "context" ? (b.context_tokens || 0) - (a.context_tokens || 0) : s.models.sort === "output" ? (b.max_output_tokens || 0) - (a.max_output_tokens || 0) : 0) || compareModelIdentity(a, b, platformOrder));
  const { rows, footer } = paginate(filtered, "models", 20);
  const body = rows
    .map(
      (m) =>
        `<tr><td class="cell-model"><div class="cell-main mono" title="${e(routeId(m))}">${e(m.id)}</div><div class="cell-sub mono truncate" title="${e(routeId(m))}">${e(routeId(m))}</div></td><td class="cell-platform">${regionTag(m.platform)}<div class="cell-sub mono">${e(m.platform || "—")}</div></td><td class="cell-pricing">${pricingCell(m)}</td><td class="num mono">${m.context_tokens ? e(compact(m.context_tokens)) : "—"}</td><td class="num mono">${m.max_output_tokens ? e(compact(m.max_output_tokens)) : "—"}</td><td><div class="tags model-capabilities">${m.supports_images ? tag(L("视觉", "Vision"), "blue") : ""}${m.supports_tools ? tag(L("工具", "Tools"), "outline") : ""}${m.supports_thinking ? tag(L("思考", "Reasoning"), "outline") : ""}${!m.supports_images && !m.supports_tools && !m.supports_thinking ? '<span class="muted">—</span>' : ""}</div></td><td><div class="row-actions">${iconButton("copy", L("复制完整模型 ID", "Copy full model ID"), "copy-model", `data-model="${e(routeId(m))}"`)}${iconButton("chat", L("测试此模型", "Try this model"), "try-model", `data-model="${e(routeId(m))}"`)}${iconButton("more", L("模型详情", "Model details"), "model-detail", `data-model="${e(routeId(m))}"`)}</div></td></tr>`,
    )
    .join("");
  $("modelResults").innerHTML = rows.length
    ? `<div class="table-wrap" tabindex="0" role="region" aria-label="${e(L("模型列表，可滚动", "Scrollable model list"))}"><table class="model-catalog-table"><thead><tr><th>${e(L("模型", "Model"))}</th><th>${e(L("来源平台", "Source"))}</th><th>${e(L("积分倍率", "Credit rate"))}</th><th class="num">${e(L("上下文", "Context"))}</th><th class="num">${e(L("最大输出", "Max output"))}</th><th>${e(L("能力", "Capabilities"))}</th><th class="num">${e(L("操作", "Actions"))}</th></tr></thead><tbody>${body}</tbody></table></div>${footer}`
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
  const order = platforms().map((p) => p.id);
  columns.sort((a,b) => compareModelIdentity({id: "", platform: owners.get(a)?.platform}, {id: "", platform: owners.get(b)?.platform}, order) || a.localeCompare(b));
  const matching = filterModelCatalog(models(), s.models);
  const allowed = new Set(matching.map((model) => model.id));
  const rows = (m.models || []).filter((id) => allowed.has(id)).sort((a,b) => a.localeCompare(b));
  if (!columns.length || !rows.length) {
    $("modelResults").innerHTML = empty(
      L("没有匹配的账号与模型", "No matching accounts and models"),
    );
    return;
  }
  const header = columns
    .map((key) => {
      const a = owners.get(key);
      const remaining = remainingCooldown(a?.cooldown_secs, s.receivedAt.accounts);
      const cooling = a?.cooldown_secs > 0 && !["disabled", "blocked"].includes(a.state) ? `<span class="matrix-head-note" data-until="${s.receivedAt.accounts + a.cooldown_secs * 1000}">${e(remaining ? countdown(remaining) : L("等待状态更新", "Awaiting update"))}</span>` : "";
      return `<th><span class="matrix-head-name">${e(a?.identity?.nickname || a?.label || key)}</span><span class="matrix-head-note">${e(a ? regionName(a.platform) : L("未记录来源", "Source unavailable"))} · ${n(a?.in_flight ?? m.in_flight?.[key])} / ${a ? (a.max_concurrency || "∞") : (m.max_concurrency || "∞")}</span>${a ? tag(...accountStatus(a)) : tag(L("状态未知", "Unknown state"))}${cooling}</th>`;
    })
    .join("");
  const body = rows
    .map(
      (id) =>
        `<tr><td class="mono">${e(id)}</td>${columns
          .map((key) => {
            const cool = m.cooldowns?.[key]?.[id] || 0,
              supported = (m.matrix?.[key] || []).includes(id);
            if (owners.get(key) && !matching.some((model) => model.id === id && model.platform === owners.get(key).platform)) return `<td><span class="unsupported" title="${e(L("此平台不符合当前筛选", "This platform does not match the filters"))}">—</span></td>`;
            const state = availabilityState(owners.get(key), supported, cool, s.receivedAt.matrix);
            if (state.kind === "cooling") return `<td><span class="cooldown" data-until="${s.receivedAt.matrix + cool * 1000}">${e(countdown(state.remaining))}</span></td>`;
            if (state.kind === "supported") return `<td><span class="supported" aria-label="${e(L("目录支持", "Catalog supported"))}">✓</span></td>`;
            const text = ({ disabled: L("已停用", "Disabled"), blocked: L("需登录", "Sign in"), account_unavailable: L("账号不可用", "Account unavailable"), awaiting_update: L("等待更新", "Awaiting update"), unknown: L("状态未知", "Unknown state"), unsupported: "—" })[state.kind];
            return `<td><span class="${["awaiting_update", "account_unavailable"].includes(state.kind) ? "cooldown" : "unsupported"}">${e(text)}</span></td>`;
          })
          .join("")}</tr>`,
    )
    .join("");
  $("modelResults").innerHTML =
    `${cardHead(L("模型 × 账号", "Models × accounts"), L("✓ 表示当前账号目录支持；停用、需登录和冷却会单独标注。", "✓ means the account catalogue supports this model. Disabled, login-required and cooling states are shown separately."))}<div class="table-wrap" tabindex="0" role="region" aria-label="${e(L("账号可用性，可滚动", "Scrollable account availability"))}"><table class="matrix"><thead><tr><th>${e(L("模型", "Model"))}</th>${header}</tr></thead><tbody>${body}</tbody></table></div><div class="table-footer">${e(L("目录支持不代表剩余额度，实际调用结果以日志为准。", "Catalog support does not guarantee quota. Check request logs for actual results."))}</div>`;
}

function accountStatus(a) {
  if (a.state === "disabled") return [L("已停用", "Disabled"), ""];
  if (accountReadiness([a]).ready) return [L("就绪", "Ready"), "green"];
  if (a.reason === "quota_exhausted" || a.reason === "quota")
    return [L("额度耗尽", "Quota exhausted"), "amber"];
  if (a.reason === "unauthorized" || a.reason === "auth")
    return [L("需要登录", "Login required"), "red"];
  if (a.cooldown_secs > 0) return [L("冷却中", "Cooling down"), "amber"];
  return [L("暂不可用", "Unavailable"), "red"];
}
function credentialStatus(a) {
  return { sign_in: L("需要重新登录", "Sign in again"), unknown: L("未提供信息", "No information"), refresh: L("等待自动刷新", "Awaiting refresh"), signed_in: L("已登录", "Signed in") }[credentialState(a)];
}
function accountPage() {
  const all = accounts().filter((a) => s.accounts.platform === "all" || a.platform === s.accounts.platform),
    health = accountReadiness(all);
  return `<section class="card resource-workspace account-workspace"><div class="health-summary metric-strip">${[["ready", L("就绪账号", "Ready accounts"), health.ready, "positive"], ["attention", L("需关注", "Need attention"), health.attention, health.attention ? "negative" : ""], ["disabled", L("已停用", "Disabled"), health.disabled, ""]].map(([id, label, count, tone]) => `<button class="item metric-filter ${s.accounts.status === id ? "selected" : ""}" data-action="account-status" data-value="${id}" aria-pressed="${s.accounts.status === id}" ${!s.cache.accounts ? "disabled" : ""}><span>${e(label)}${icon("arrow")}</span><strong class="${tone}">${s.cache.accounts ? n(count) : "—"}</strong></button>`).join("")}<span class="item"><span>${e(L("处理中的请求", "Active requests"))}</span><strong>${s.cache.accounts ? n(all.reduce((sum, a) => sum + (a.in_flight || 0), 0)) : "—"}</strong></span></div><div class="workspace-controls">
  <div class="toolbar"><div class="toolbar-group">${platformTabs("accounts", s.accounts.platform)}</div></div>
  <div class="toolbar"><div class="toolbar-group">${search("accountSearch", s.accounts.query, L("搜索昵称、账号或 UID…", "Search name, account or UID…"))}<select id="accountStatusFilter" class="select" aria-label="${e(L("账号状态", "Account status"))}">${options(
    [
      ["all", L("全部状态", "All states")],
      ["ready", L("就绪", "Ready")],
      ["attention", L("需关注", "Attention")],
      ["disabled", L("已停用", "Disabled")],
    ],
    s.accounts.status,
  )}</select></div><div class="toolbar-group"><span class="result-count" id="accountCount"></span>${densityControl()}<div class="segmented">${[["table", L("表格", "Table")], ["cards", L("卡片", "Cards")]].map(([id, title]) => `<button data-action="account-view" data-value="${id}" class="${s.accounts.view === id ? "active" : ""}" aria-pressed="${s.accounts.view === id}">${e(title)}</button>`).join("")}</div></div></div><div id="activeAccountFilters" class="active-filters" hidden></div></div><div class="${s.accounts.view === "table" ? "resource-table" : "account-grid"}" id="accountCards"></div></section>`;
}
function updateAccountCards() {
  if (!$("accountCards")) return;
  $("pageContent").querySelectorAll('[data-action="account-status"]').forEach(button => {
    const selected = s.accounts.status === button.dataset.value;
    button.classList.toggle("selected", selected);
    button.setAttribute("aria-pressed", String(selected));
  });
  renderActiveFilters("accounts");
  const q = s.accounts.query.trim().toLowerCase();
  const rows = accounts().filter(
    (a) =>
      (s.accounts.platform === "all" || a.platform === s.accounts.platform) &&
      `${a.label} ${a.identity?.nickname || ""} ${a.identity?.uid || ""}`
        .toLowerCase()
        .includes(q) &&
      (s.accounts.status === "all" ||
        (s.accounts.status === "ready" && accountReadiness([a]).ready) ||
        (s.accounts.status === "disabled" && a.state === "disabled") ||
        (s.accounts.status === "attention" &&
          !accountReadiness([a]).ready &&
          a.state !== "disabled")),
  );
  $("accountCount").textContent = L(
    `${rows.length} 个账号`,
    `${rows.length} accounts`,
  );
  if (s.accounts.view === "table") {
    $("accountCards").innerHTML = rows.length ? `<div class="table-wrap" tabindex="0" role="region" aria-label="${e(L("账号列表，可滚动", "Scrollable account list"))}"><table class="account-table"><thead><tr><th>${e(L("账号", "Account"))}</th><th>${e(L("平台", "Platform"))}</th><th>${e(L("调度状态", "Scheduling"))}</th><th>${e(L("登录状态", "Credentials"))}</th><th>${e(L("并发", "Concurrency"))}</th><th class="num">${e(L("累计调用", "Requests"))}</th><th class="num">${e(L("操作", "Actions"))}</th></tr></thead><tbody>${rows.map((a) => {
      const index = accounts().indexOf(a), name = a.identity?.nickname || a.label;
      const ambiguous = accounts().filter((other) => other.label === a.label).length > 1;
      const usage = s.cache.metrics?.by_account?.find((row) => row.key === a.label);
      const max = a.max_concurrency || 0, current = a.in_flight || 0;
      return `<tr><td><div class="account-table-name"><span class="account-avatar">${e(name.slice(0, 1))}</span><div><strong>${e(name)}</strong><div class="cell-sub mono">${e(a.label)}</div></div></div><div class="cell-sub">${e(a.identity?.source === "desktop" ? L("桌面应用", "Desktop app") : L("凭证文件", "Credential file"))}</div></td><td>${regionTag(a.platform)}</td><td>${tag(...accountStatus(a))}${Object.keys(a.model_cooldowns || {}).length ? `<div class="cell-sub">${Object.keys(a.model_cooldowns).length} ${e(L("个模型冷却", "models cooling"))}</div>` : ""}${a.cooldown_secs > 0 ? `<div class="cell-sub" data-until="${(s.receivedAt.accounts || Date.now()) + a.cooldown_secs * 1000}">${e(countdown(remainingCooldown(a.cooldown_secs, s.receivedAt.accounts)))}</div>` : ""}</td><td><span class="${a.identity?.refresh_expired ? "negative" : ""}">${e(credentialStatus(a))}</span><div class="cell-sub">${a.identity?.expires_at ? e(timestamp(a.identity.expires_at, true)) : "—"}</div></td><td class="account-concurrency"><span class="numeric">${current} / ${max || "∞"}</span><div class="load-track"><span style="width:${max ? Math.min(100, current / max * 100) : 0}%"></span></div></td><td class="num numeric">${ambiguous ? `<span class="muted" title="${e(L("同标识跨平台聚合，无法单独归属", "This label is aggregated across platforms"))}">—</span>` : n(usage?.total)}${!ambiguous && usage?.failed ? `<div class="cell-sub negative">${n(usage.failed)} ${e(L("失败", "failed"))}</div>` : ""}</td><td><div class="row-actions"><button class="button small" data-action="account-requests" data-platform="${e(a.platform)}" data-label="${e(a.label)}">${e(L("记录", "Logs"))}</button>${iconButton("more", L("账号详情与操作", "Account details and actions"), "account-detail", `data-index="${index}"`)}</div></td></tr>`;
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
        return `<article class="card account-card"><div class="account-card-top"><div class="account-avatar">${e(name.slice(0, 1).toUpperCase())}</div><div class="account-name"><strong title="${e(name)}">${e(name)}</strong><small>${e(platformName(a.platform))}</small></div>${iconButton("more", L("账号详情与操作", "Account details and actions"), "account-detail", `data-index="${index}"`)}</div><div class="account-state">${tag(state, color, true)}<div class="account-load">${e(L("并发", "Active"))} ${e(load)}<div class="load-track"><span style="width:${max ? Math.min(100, ((a.in_flight || 0) / max) * 100) : 0}%"></span></div></div></div>${a.cooldown_secs > 0 ? `<div class="account-note">${e(L("整号冷却剩余", "Account cooldown"))} <span data-until="${(s.receivedAt.accounts || Date.now()) + a.cooldown_secs * 1000}">${e(countdown(remainingCooldown(a.cooldown_secs, s.receivedAt.accounts)))}</span></div>` : ""}<div class="account-kv"><span>${e(L("登录状态", "Credentials"))}</span><span>${e(credentialStatus(a))}</span></div><div class="account-kv"><span>${e(L("来源", "Source"))}</span><span>${e(a.identity?.source === "desktop" ? L("桌面应用", "Desktop app") : a.identity?.source === "file" ? L("凭证文件", "Credential file") : a.identity?.source || "—")}</span></div><div class="account-kv"><span>UID</span><span title="${e(a.identity?.uid || "")}">${e(a.identity?.uid || "—")}</span></div><button class="button small" data-action="account-detail" data-index="${index}">${e(L("查看详情与管理", "Details & management"))}${icon("arrow")}</button></article>`;
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
  const active = s.cache.platforms?.active || platforms(), planned = s.cache.platforms?.planned || [];
  const knownAccounts = !!s.cache.accounts;
  return `<section class="connected-providers"><div class="resource-section-head"><div><h2>${e(L("已接入平台", "Connected platforms"))}</h2><span class="resource-count">${active.length}</span></div><a class="text-link" href="#connection">${e(L("客户端接入指南", "Client setup"))}${icon("arrow")}</a></div><div class="provider-list">${active.map((p) => {
    const pool = accounts().filter((a) => a.platform === p.id), health = accountReadiness(pool);
    const count = s.cache.models ? models().filter((m) => m.platform === p.id).length : p.models;
    return `<article class="card provider-row"><div class="provider-heading"><div class="provider-emblem" aria-hidden="true">${icon("platforms")}</div><div class="provider-identity"><h2>${e(platformName(p.id))}</h2><span class="provider-id mono">${e(p.id)}</span></div><span class="provider-state ${p.status === "active" ? "positive" : "muted"}"><i></i>${e(p.status === "unauthenticated" ? L("待登录", "Sign in needed") : p.status === "active" ? L("已接入", "Integrated") : L("需关注", "Attention"))}</span></div><div class="provider-resources"><div><span>${e(L("模型目录", "Models"))}</span><strong>${count == null ? "—" : n(count)}<small>${e(L("个模型", "models"))}</small></strong></div><div><span>${e(L("就绪账号 / 已启用", "Ready / enabled accounts"))}</span><strong>${knownAccounts ? `${health.ready}<small>/ ${health.enabled}</small>` : "—"}</strong></div><div><span>${e(L("账号总数", "Total accounts"))}</span><strong>${knownAccounts ? n(health.total) : "—"}</strong></div><div class="provider-actions"><button class="button" data-action="platform-accounts" data-value="${e(p.id)}">${e(L("管理账号", "Accounts"))}</button><button class="button provider-model-action" data-action="platform-models" data-value="${e(p.id)}">${e(L("浏览模型", "Models"))}${icon("arrow")}</button></div></div><div class="provider-footer"><span class="provider-origin"><span>${e(L("上游地址", "Upstream"))}</span><code>${e(p.base_url || "—")}</code></span><span class="provider-prefix"><span>${e(L("路由前缀", "Route prefix"))}</span><code>${e(p.id)}/</code>${iconButton("copy", L("复制路由前缀", "Copy route prefix"), "copy-model", `data-model="${e(p.id)}/"`)}</span></div></article>`;
  }).join("")}</div></section><section class="routing-section"><div class="resource-section-head"><div><h2>${e(L("模型路由", "Model routing"))}</h2></div><span>${e(L("同名模型，按调用方式选择来源", "Choose the source through the model ID"))}</span></div>${routePolicy()}</section>${planned.length ? `<details class="planned-platforms"><summary>${e(L("后续平台接入", "Upcoming integrations"))}<span>${planned.map((p) => e(p.name)).join(" · ")}</span></summary>${planned.map((p) => `<p><strong>${e(p.name)}</strong> · ${e(p.note || p.id)}</p>`).join("")}</details>` : ""}`;
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
function renderActiveFilters(kind) {
  const target = $(kind === "accounts" ? "activeAccountFilters" : "activeRequestFilters");
  if (!target) return;
  const filters = s[kind], keys = activeFilterKeys(filters, kind === "accounts" ? ["query", "platform", "status"] : ["query", "platform", "status", "protocol", "model", "account", "range"]);
  const titles = { query: L("搜索", "Search"), platform: L("平台", "Platform"), status: L("状态", "Status"), protocol: L("协议", "Protocol"), model: L("模型", "Model"), account: L("账号", "Account"), range: L("时间", "Range") };
  const labels = { ready: L("就绪", "Ready"), attention: L("需关注", "Attention"), disabled: L("已停用", "Disabled"), ok: L("成功", "Success"), failed: L("失败", "Failed") };
  const label = key => key === "platform" ? platformName(filters[key]) : key === "protocol" ? protocolName(filters[key]) : key === "range" ? L(`最近 ${filters[key]} 分钟`, `Last ${filters[key]} min`) : key === "account" ? (filters[key].startsWith("label:") ? filters[key].slice(6) : (() => { try { const [p, a] = JSON.parse(filters[key]); return `${platformName(p)} / ${a || "—"}`; } catch { return filters[key]; } })()) : key === "status" ? labels[filters[key]] || filters[key] : filters[key];
  target.hidden = !keys.length;
  target.innerHTML = `<span>${e(L("当前筛选", "Active filters"))}</span>${keys.map(key => `<button class="filter-chip" data-action="clear-filter" data-kind="${kind}" data-key="${key}" aria-label="${e(L("移除", "Remove") + " " + titles[key] + ": " + label(key))}"><span>${e(titles[key])}: ${e(label(key))}</span>${icon("close")}</button>`).join("")}<button class="text-link" data-action="reset-${kind === "accounts" ? "account" : "request"}-filters">${e(L("清空", "Clear all"))}</button>`;
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
  return `<section class="card resource-workspace request-workspace"><div class="card-head request-table-head"><div><h2>${e(L("调用记录", "Request records"))}</h2><p>${e(L("网关仅保留最近 200 条；筛选与导出作用于这些记录", "The gateway retains the latest 200 records; filters and exports apply to this sample"))}</p></div><div class="toolbar-group"><select id="requestSort" class="select" aria-label="${e(L("记录排序", "Request sort"))}">${options([["time", L("最新优先", "Newest first")], ["latency", L("耗时优先", "Longest duration")], ["tokens", L("Token 优先", "Most tokens")]], s.requests.sort)}</select><select id="requestPageSize" class="select" aria-label="${e(L("每页记录数", "Records per page"))}">${options([["20", "20 / " + L("页", "page")], ["50", "50 / " + L("页", "page")], ["100", "100 / " + L("页", "page")]], String(s.requests.size))}</select>${densityControl()}${button(L("导出", "Export"), "export-requests", "small", "requests")}</div></div><div class="request-filters"><div class="toolbar"><div class="toolbar-group">${search("requestSearch", s.requests.query, L("搜索模型、账号、状态码或错误…", "Search model, account, status or error…"))}<select id="requestPlatformFilter" class="select" aria-label="${e(L("平台筛选", "Platform filter"))}">${platformOptions(s.requests.platform)}</select><select id="requestStatusFilter" class="select" aria-label="${e(L("调用状态", "Request status"))}">${options(
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
  )}</select></div></div><details class="advanced-filters" id="requestAdvanced" ${[s.requests.model, s.requests.account, s.requests.range].some(v => v !== "all") ? "open" : ""}><summary>${icon("settings")}${e(L("更多筛选", "More filters"))}<span class="filter-count" id="requestFilterCount"></span>${icon("chevron")}</summary><div class="toolbar filter-secondary"><div class="toolbar-group"><select id="requestModelFilter" class="select" aria-label="${e(L("模型筛选", "Model filter"))}">${options([["all", L("全部模型", "All models")], ...modelOptions.sort().map((id) => [id, id])], s.requests.model)}</select><select id="requestAccountFilter" class="select" aria-label="${e(L("账号筛选", "Account filter"))}">${options([["all", L("全部账号", "All accounts")], ...accountOptions], s.requests.account)}</select><select id="requestRangeFilter" class="select" aria-label="${e(L("记录时间范围", "Request time range"))}">${options([["all", L("全部缓存记录", "All buffered records")], ["15", L("最近 15 分钟", "Last 15 minutes")], ["30", L("最近 30 分钟", "Last 30 minutes")], ["60", L("最近 1 小时", "Last hour")]], s.requests.range)}</select></div>${button(L("清空筛选", "Reset filters"), "reset-request-filters", "ghost", "close")}</div></details><div id="activeRequestFilters" class="active-filters" hidden></div></div><div class="sample-summary metric-strip" id="requestSampleStats"></div><div class="resource-table" id="requestResults"></div></section>`;
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
  return `<div class="table-wrap" tabindex="0" role="region" aria-label="${e(L("调用记录，可滚动", "Scrollable request records"))}"><table class="request-table"><thead><tr><th>${e(L("时间", "Time"))}</th><th>${e(L("模型与平台", "Model & platform"))}</th><th>${e(L("协议", "Protocol"))}</th><th>${e(L("状态", "Status"))}</th><th class="num">${e(L("耗时", "Duration"))}</th><th class="num">${e(L("输入 / 输出", "Input / output"))}</th><th></th></tr></thead><tbody>${rows
    .map((row) => {
      // Use a stable key instead of object identity: overview and logs have separate array copies.
      const key = requestKey(row);
      return `<tr class="request-click" data-action="request-detail" data-key="${e(key)}" tabindex="0" aria-label="${e(`${row.model} · ${timestamp(row.time)} · ${L("调用详情", "Request details")}`)}"><td class="mono">${e(timestamp(row.time))}<div class="cell-sub">${e(new Date(row.time).toLocaleDateString(undefined, { month: "2-digit", day: "2-digit" }))}</div></td><td class="cell-model"><div class="cell-main mono" title="${e(row.model)}">${e(row.model)}</div><div class="cell-sub">${row.platform ? regionTag(row.platform) : e(L("来源未记录", "Source unrecorded"))} ${e(row.account || "")}</div>${row.model_requested ? `<div class="cell-sub requested-model" title="${e(row.model_requested)}">${e(L("请求", "Requested"))}: ${e(row.model_requested)}</div>` : ""}</td><td><span style="font-size:11px">${e(protocolName(row.protocol))}</span><div class="cell-sub">${e(row.stream ? L("流式", "Streaming") : L("非流式", "Non-streaming"))}</div></td><td>${tag(`${row.status} · ${row.ok ? L("成功", "OK") : L("失败", "Failed")}`, row.ok ? "green" : "red")} ${!row.ok && row.error ? `<span class="table-short-error" title="${e(row.error)}">${e(row.error.slice(0, 120))}</span>` : ""}</td><td class="num mono">${e(duration(row.duration_ms))}${row.gen_ms > 0 && row.output_tokens > 0 ? `<div class="cell-sub">${(row.output_tokens / (row.gen_ms / 1000)).toFixed(1)} tok/s</div>` : ""}</td><td class="num mono">${n(row.input_tokens)} <span class="muted">/</span> ${n(row.output_tokens)}</td><td>${iconButton("chevron", L("调用详情", "Request details"), "request-detail", `data-key="${e(key)}"`)}</td></tr>`;
    })
    .join("")}</tbody></table></div>`;
}
function updateRequestTable() {
  if (!$("requestResults")) return;
  renderActiveFilters("requests");
  const filtered = filteredRequests(),
    { rows, footer } = paginate(filtered, "requests", s.requests.size);
  $("requestResults").innerHTML = requestTable(rows) + footer;
  const stats = summarizeRequests(filtered);
  const advancedCount = [s.requests.model, s.requests.account, s.requests.range].filter(v => v !== "all").length;
  $("requestFilterCount").textContent = advancedCount ? String(advancedCount) : "";
  $("requestSampleStats").innerHTML = `<span><span>${e(L("匹配记录", "Matched records"))}</span><strong>${n(stats.total)}</strong><small>/ ${recentRequests().length}</small></span><span><span>${e(L("成功率", "Success rate"))}</span><strong>${stats.total ? (stats.success_rate * 100).toFixed(1) + "%" : "—"}</strong></span><span><span>${e(L("失败", "Failed"))}</span><strong class="${stats.failed ? "negative" : ""}">${n(stats.failed)}</strong></span><span><span>${e(L("P95 完整耗时", "P95 total duration"))}</span><strong>${e(duration(stats.p95))}</strong></span><span><span>${e(L("输出 Token", "Output tokens"))}</span><strong>${compact(stats.output_tokens)}</strong></span>`;
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
    `<div class="detail-hero"><div class="platform-avatar ${m.platform === "workbuddy-global" ? "global" : ""}">${icon("models")}</div><div><h2>${e(m.display_name || m.id)}</h2><p>${e(platformName(m.platform))}</p></div></div><dl class="details-list">${detailItem(L("完整模型 ID", "Full model ID"), routeId(m), "mono")}${detailItem(L("原始模型名", "Original model name"), m.id, "mono")}${detailItem(L("费率类型", "Rate type"), pricingName(modelPricing(m).status))}${detailItem(L("积分倍率", "Credit multiplier"), modelPricing(m).multiplier !== undefined ? "×" + modelPricing(m).multiplier : modelPricing(m).status === "dynamic" ? L("随所选模型变化", "Depends on selected model") : L("未知", "Unknown"))}${detailItem(L("上游费率标注", "Original rate label"), modelPricing(m).raw || L("未提供", "Not reported"))}${detailItem(L("费率来源与时间", "Rate source and time"), modelPricing(m).observedAt ? L("上游模型配置 · ", "Upstream configuration · ") + timestamp(modelPricing(m).observedAt, true) + (modelPricing(m).stale ? L(" · 已超过 24 小时，需更新", " · Older than 24 hours, refresh needed") : "") : L("无可核实信息", "No verified metadata"))}${detailItem(L("上下文窗口", "Context window"), m.context_tokens ? n(m.context_tokens) + " tokens" : L("未提供", "Unreported"))}${detailItem(L("最大输出", "Maximum output"), m.max_output_tokens ? n(m.max_output_tokens) + " tokens" : L("未提供", "Unreported"))}${detailItem(L("单请求图片护栏", "Images per request guard"), m.image_limit ? m.image_limit.enabled ? `${n(m.image_limit.max_images)} ${L("张（含历史与工具结果）", "images, including history and tool results")}` : L("已关闭（不代表上游无限制）", "Disabled; upstream limits may still apply") : L("未取得护栏信息，上游上限未知", "No guard metadata; upstream limit unknown"))}${m.image_limit ? detailItem(L("护栏来源", "Guard source"), m.image_limit.source === "config" ? L("本地配置覆盖", "Local configuration override") : L("内置实测 · ", "Built-in empirical · ") + m.image_limit.observed_on) : ""}${m.image_limit ? detailItem(L("图片超限处理", "Image overflow handling"), m.image_limit.enabled ? imageOverflowName(m.image_limit.overflow_policy) : L("护栏未启用", "Guard disabled")) : ""}${detailItem(L("模型能力", "Capabilities"), [m.supports_images ? L("视觉", "Vision") : "", m.supports_tools ? L("工具调用", "Tools") : "", m.supports_thinking ? L("思考", "Reasoning") : ""].filter(Boolean).join(" / ") || L("未提供", "Unreported"))}${detailItem(L("裸模型名默认路由", "Default route for bare ID"), m.default_route ? L("默认优先使用此平台", "This platform is preferred") : L("完整 ID 可固定到此平台", "Use the full ID to select this platform"))}${m.description ? detailItem(L("说明", "Description"), m.description) : ""}</dl><div class="detail-actions"><button class="button" data-action="copy-model" data-model="${e(id)}">${icon("copy")}${e(L("复制完整 ID", "Copy full ID"))}</button><button class="button primary" data-action="try-model" data-model="${e(id)}">${icon("chat")}${e(L("测试模型", "Try model"))}</button></div>`,
  );
}
function showAccountDetail(index) {
  const a = accounts()[index];
  if (!a) return;
  const [status, color] = accountStatus(a),
    identity = a.identity || {};
  showDrawer(
    L("账号详情", "Account details"),
    `<div class="detail-hero"><div class="account-avatar">${e((identity.nickname || a.label).slice(0, 1))}</div><div><h2>${e(identity.nickname || a.label)}</h2><p>${e(platformName(a.platform))}</p></div></div><div class="tags" style="margin-top:17px">${tag(status, color, true)}${tag(credentialStatus(a), "outline")}</div><dl class="details-list">${detailItem(L("账号标识", "Account label"), a.label, "mono")}${detailItem("UID", identity.uid)}${accountUsageDetails(a)}${detailItem(L("当前并发", "Active requests"), `${a.in_flight || 0} / ${a.max_concurrency || L("不限", "Unlimited")}`)}${detailItem(L("状态原因", "State reason"), a.reason || "—")}${a.cooldown_secs ? detailItem(L("整号冷却", "Account cooldown"), countdown(remainingCooldown(a.cooldown_secs, s.receivedAt.accounts))) : ""}${detailItem(L("登录令牌到期", "Access token expiry"), identity.expires_at ? timestamp(identity.expires_at, true) : "—")}${detailItem(L("凭证位置", "Credential path"), identity.credential_path || "—", "mono")}${a.last_error ? detailItem(L("最近错误", "Last error"), a.last_error, "error-text") : ""}${
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
  const row = recentRequests().find(r => requestKey(r) === key);
  if (!row) return;
  const images = imageRecordSummary(row), issue = requestIssue(row);
  const hints = {
    images: L("已明确命中图片数量护栏。检查图片数量与处理策略；仅发送“继续”不会减少 ZCode 携带的历史图片。", "The image count guard rejected this request. Review image counts and overflow policy; sending Continue alone does not reduce history."),
    image_structure: L("图片结构无法可靠匹配，网关未自动清理。检查图片数据与请求结构。", "Images could not be matched reliably. The gateway did not trim them; review image data and request structure."),
    auth: L("检查客户端 API Key、账号登录状态及上游访问权限。", "Review the client API key, account sign-in and upstream permissions."),
    rate: L("检查上游额度与限流信息、账号冷却状态。原样重试可能再次失败。", "Review upstream quotas, rate limits and account cooldown; an unchanged retry may fail again."),
    unknown: L("原因尚未确定。保留上游原文，请结合实际平台、模型、图片数据和其他请求参数排查。", "The cause is unconfirmed. Use the original provider message together with the actual route, images and other parameters."),
  };
  const section = (title, items, note = "") => `<section class="inspection-section"><h3>${e(title)}</h3><dl class="details-list">${items}</dl>${note ? `<p class="inspection-note">${e(note)}</p>` : ""}</section>`;
  const speed = row.gen_ms > 0 && row.output_tokens ? `${(row.output_tokens / (row.gen_ms / 1000)).toFixed(1)} tok/s` : L("未测量", "Not measured");
  showDrawer(L("调用详情", "Request details"),
    `<div class="detail-hero"><div class="platform-avatar">${icon("requests")}</div><div><h2>${e(row.model)}</h2><p>${e(timestamp(row.time, true))}</p></div></div><div class="detail-status">${tag(`${row.status} · ${row.ok ? L("成功", "Success") : L("失败", "Failed")}`, row.ok ? "green" : "red")}${regionTag(row.platform)}${tag(row.stream ? L("流式", "Streaming") : L("非流式", "Non-streaming"))}</div>
    ${row.error ? `<section class="inspection-error"><h3>${e(L("错误详情", "Error details"))}</h3><pre>${e(row.error)}</pre>${issue ? `<p>${e(hints[issue.hint])}</p>` : ""}</section>` : ""}
    <div class="inspection-kpis"><div><span>${e(L("完整耗时", "Total duration"))}</span><strong>${e(duration(row.duration_ms))}</strong></div><div><span>${e(L("输出 Token", "Output tokens"))}</span><strong>${compact(row.output_tokens)}</strong></div><div><span>${e(L("生成速度", "Generation speed"))}</span><strong>${e(speed)}</strong></div></div>
    ${section(L("请求与实际路由", "Request and actual route"), detailItem(L("客户端请求模型", "Requested model"), row.model_requested || row.model, "mono") + detailItem(L("实际模型", "Actual model"), row.model, "mono") + detailItem(L("实际平台", "Actual platform"), row.platform || L("未记录", "Unrecorded"), "mono") + detailItem(L("账号", "Account"), row.account || "—") + detailItem(L("协议", "Protocol"), protocolName(row.protocol)) + detailItem(L("输出方式", "Response mode"), row.stream ? L("流式", "Streaming") : L("非流式", "Non-streaming")))}
    ${section(L("Token 用量", "Token usage"), detailItem(L("输入 Token", "Input tokens"), n(row.input_tokens)) + detailItem(L("输出 Token", "Output tokens"), n(row.output_tokens)) + detailItem(L("思考 Token", "Reasoning tokens"), n(row.reasoning_tokens)))}
    ${section(L("图片与超限策略", "Images and overflow policy"), detailItem(L("本次图片数量", "Request image count"), images.received == null ? L("未记录", "Unrecorded") : `${n(images.received)} ${L("张（含历史与工具结果）", "images, including history and tool results")}`) + detailItem(L("图片数量护栏", "Image count guard"), images.limit == null ? L("未记录或无已知护栏", "Unrecorded or no known guard") : images.limit > 0 ? `${n(images.limit)} ${L("张 / 请求", "images / request")}` : L("已关闭", "Disabled")) + detailItem(L("处理后图片数量", "Prepared image count"), images.forwarded == null ? L("未记录或未转发", "Unrecorded or not forwarded") : `${n(images.forwarded)} ${L("张", "images")}`) + detailItem(L("本次省略的旧图", "Older images omitted"), images.removed ? `${n(images.removed)} ${L("张", "images")}` : row.forwarded_image_count == null ? L("未记录", "Unrecorded") : L("未省略", "None omitted")) + detailItem(L("图片超限处理", "Image overflow handling"), row.image_overflow_policy ? imageOverflowName(row.image_overflow_policy) : L("未记录", "Unrecorded")), images.removed ? L("仅本次转发省略旧图；文字和工具结果保留，ZCode 本地历史未修改。", "Old images were omitted only for this forwarding attempt; text, tool results and local history remain intact.") : "")}
    <div class="inspection-actions">${row.account ? `<button class="button" data-action="request-account-logs" data-platform="${e(row.platform || "")}" data-label="${e(row.account)}">${icon("requests")}${e(L("此账号的调用", "Requests for this account"))}</button>` : ""}<button class="button" data-action="request-model-logs" data-platform="${e(row.platform || "")}" data-model="${e(row.model)}">${icon("models")}${e(L("此模型的调用", "Requests for this model"))}</button></div>`);
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
  const available = platforms();
  const selected = available.some((p) => p.id === s.accounts.platform)
    ? s.accounts.platform : available[0]?.id;
  modal(
    L("添加账号", "Add account"),
    `<p>${e(L("选择账号所属的平台，然后在浏览器中完成授权。", "Choose the account's platform, then authorize in your browser."))}</p><form id="addAccountForm"><fieldset class="login-platforms"><legend>${e(L("选择登录平台", "Choose a login platform"))}</legend>${available.map((p) => `<label class="login-platform"><input type="radio" name="loginPlatform" value="${e(p.id)}" ${p.id === selected ? "checked" : ""} required><span class="platform-mark" aria-hidden="true">${e(platformMark(p.id))}</span><span class="login-platform-copy"><strong>${e(platformName(p.id))}</strong><small>${e(p.id)}${p.status === "unauthenticated" ? e(L(" · 尚未登录", " · Not signed in")) : ""}</small></span>${icon("check")}</label>`).join("")}</fieldset><p class="login-platform-note">${e(available.length ? L("国内版与国际版分别授权，账号会加入所选平台的独立账号池。", "China and Global require separate authorization. Accounts join the selected platform's own pool.") : L("尚未取得可登录的平台，请关闭窗口并刷新账号页面后重试。", "No login platforms are available. Close this dialog and refresh the accounts page to retry."))}</p><div class="modal-actions">${button(L("取消", "Cancel"), "close-modal")}<button class="button primary" type="submit" ${available.length ? "" : "disabled"}>${icon("external")}${e(L("开始登录", "Start login"))}</button></div></form>`,
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
      throw new Error(await responseError(response, s.lang));
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
        return refresh(currentResources(), true);
      case "command-search":
        return commandSearch();
      case "toggle-sidebar":
        s.sidebarCollapsed = !s.sidebarCollapsed;
        save("agent2api_sidebar_collapsed", String(s.sidebarCollapsed));
        return chrome();
      case "toggle-density":
        s.density = s.density === "compact" ? "comfortable" : "compact";
        save("agent2api_density", s.density);
        chrome();
        draw(true);
        $("pageContent").querySelector('[data-action="toggle-density"]')?.focus({ preventScroll: true });
        return;
      case "jump-page":
        $("modal").close();
        location.hash = value;
        return;
      case "trend-range":
        s.trendMinutes = Number(value);
        return draw();
      case "dashboard-scope":
        s.dashboardScope = value === "all" ? "all" : "window";
        if (value !== "all") s.trendMinutes = Number(value) === 15 ? 15 : 30;
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
      case "attention-accounts":
        Object.assign(s.accounts, { platform: "all", query: "", status: "attention" });
        location.hash = "accounts";
        if (s.page === "accounts") draw();
        return;
      case "account-status":
        s.accounts.status = s.accounts.status === value ? "all" : value;
        return draw();
      case "reset-account-filters":
        Object.assign(s.accounts, { platform: "all", query: "", status: "all" });
        return draw();
      case "clear-filter": {
        const keys = kind === "accounts" ? ["query", "platform", "status"] : kind === "requests" ? ["query", "platform", "status", "protocol", "model", "account", "range"] : [];
        const key = target.dataset.key;
        if (!keys.includes(key)) return;
        s[kind][key] = key === "query" ? "" : "all";
        if (kind === "requests") s.requests.page = 1;
        return draw();
      }
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
      case "model-summary-filter":
        s.models.pricing = value;
        s.models.page = 1;
        draw();
        return;
      case "reset-model-filters":
        Object.assign(s.models, { query: "", platform: "all", capability: "all", pricing: "all", page: 1 });
        draw();
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
      case "platform-accounts":
        Object.assign(s.accounts, { platform: value, query: "", status: "all" });
        location.hash = "accounts";
        return;
      case "model-view":
        s.models.view = value;
        draw();
        if (value === "matrix") return refresh(currentResources());
        return;
      case "paginate":
        s[kind].page += Number(value);
        if (kind === "models") updateModelTable();
        else updateRequestTable();
        return;
      case "reload-models":
        target.disabled = true;
        await api("/api/config", { refresh_models: true });
        await refresh(currentResources());
        if (s.errors.models || s.errors.matrix) throw (s.errors.models || s.errors.matrix);
        toast(s.cache.models?.pricing_snapshot ? L("模型清单已更新；费率保留原读取时间", "Catalogue updated; rate observation times unchanged") : L("模型目录已更新", "Model catalog updated"));
        return;
      case "account-detail":
        $("modal").close();
        return showAccountDetail(Number(index));
      case "request-account-logs":
        $("detailDialog").close();
        return openRequestFilters({ platform: platform || "all", account: accountKey({ platform, account: label }) });
      case "request-model-logs":
        $("detailDialog").close();
        return openRequestFilters({ platform: platform || "all", model });
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
    const platform = new FormData(event.target).get("loginPlatform");
    if (platform) startLogin(platform);
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
$("refreshButton").addEventListener("click", () => refresh(currentResources(), true));
$("copyEndpoint").addEventListener("click", () => copy(`${gatewayURL()}/v1`));
window.addEventListener("hashchange", navigate);
document.addEventListener("visibilitychange", () => {
  if (!document.hidden)
    refresh(s.page === "chat" ? ["status"] : currentResources());
});
setInterval(() => {
  if (document.hidden || $("modal").open || $("detailDialog").open || !s.refreshEvery || Date.now() - s.lastPoll < s.refreshEvery * 1000) return;
  const keys = currentResources(true);
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
