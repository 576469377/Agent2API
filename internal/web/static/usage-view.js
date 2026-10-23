import { cachedUsage, curveSeries, curvePath, lifetimeUsage, heatmapColumns, liveGeneration, tokenUsageMissing } from "./usage.js";

export function liveRequestStatus(row, L, now) {
  const labels = {
    reading: L("正在读取请求", "Reading request"), routing: L("正在选择服务", "Selecting service"),
    queued: L("排队等待", "Queued"), connecting: L("正在连接上游", "Connecting"),
    uploading: L("正在上传请求", "Uploading request"), waiting_headers: L("等待上游响应", "Awaiting response headers"),
    waiting_output: L("等待首段输出", "Awaiting first output"),
  };
  if (labels[row.phase]) {
    let detail = row.phase === "queued" && row.queue_ms > 0 ? ` · ${(row.queue_ms / 1000).toFixed(1)}s` : "";
    if (row.phase === "uploading" && row.request_bytes > 0) {
      const size = bytes => bytes < 1048576 ? `${(bytes / 1024).toFixed(1)} KB` : `${(bytes / 1048576).toFixed(1)} MB`;
      detail += ` · ${size(row.uploaded_bytes || 0)} / ${size(row.request_bytes)}`;
      const last = row.upload_last_progress_at_ms || row.upload_started_at_ms;
      const idle = last > 0 ? Math.max(0, Math.floor((now - last) / 1000)) : 0;
      if (idle >= 5) detail += ` · ${L(`${idle}秒无进展`, `No progress for ${idle}s`)}`;
      if (row.uploaded_bytes >= row.request_bytes && !row.upload_completed_at_ms) detail += ` · ${L("等待发送完成", "Finishing send")}`;
    }
    return labels[row.phase] + detail;
  }
  return !row.last_output_at ? L("等待首段输出", "Awaiting first output") : now - Date.parse(row.last_output_at) >= 3000 ? L("等待后续输出", "Awaiting more output") : L("正在接收", "Receiving");
}

export function livePhaseGroups(active, L, now) {
  const groups = new Map();
  for (const row of active) {
    const label = liveRequestStatus(row, L, now).split(" · ")[0];
    const key = row.phase === "streaming" && row.last_output_at && now - Date.parse(row.last_output_at) < 3000 ? "receiving" : label;
    const group = groups.get(key) || { label, count: 0, receiving: key === "receiving" };
    group.count++;
    groups.set(key, group);
  }
  return [...groups.values()];
}

function liveRequestElapsed(row, now) {
  const age = Math.max(0, now - Date.parse(row.started_at));
  if (!Number.isFinite(age)) return "—";
  const seconds = Math.floor(age / 1000);
  return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}

export function usageModelLabel(model, L) {
  if (model.key === "other") return L("其他模型", "Other models");
  const name = ["(unattributed)", "(unknown)"].includes(model.model) ? L("未记录模型", "Unrecorded model") : model.model || L("未记录模型", "Unrecorded model");
  return `${model.platform || L("未记录平台", "Unrecorded platform")}/${name}`;
}

export function usageTokenValue(row, format) {
  return tokenUsageMissing(row) ? "—" : (row.unknown_token_requests > 0 ? "≥ " : "") + format(row.tokens);
}

export function usageDayView(day, ui) {
  const { e, L, n, compact } = ui;
  return `<div class="usage-day-rows">${day.models.map(m => {
    const calls = m.total === 0 && m.estimated_tokens > 0 ? L("Token 用量", "Token usage") : `${n(m.total)} ${L("次调用", "requests")}`;
    const detail = tokenUsageMissing(m) ? L("Token 未上报", "Token usage unavailable") : `${n(m.input_tokens)} ${L("输入", "input")} / ${n(m.output_tokens)} ${L("输出", "output")}`;
    return `<div class="usage-token-row"><i style="background:${m.color}"></i><div><strong>${e(usageModelLabel(m, L))}</strong><small>${e(calls)} · ${e(detail)}</small></div><b>${e(usageTokenValue(m, compact))}${tokenUsageMissing(m) ? "" : " tokens"}</b></div>`;
  }).join("") || `<p class="muted">${e(day.coverage === "unknown" ? L("没有可恢复的明细", "No recoverable details") : L("没有已记录的调用", "No recorded requests"))}</p>`}</div>`;
}

export function usageWeekView(days, cumulative, ui) {
  const { e, L, n } = ui;
  return `<p class="scope-note">${e(cumulative ? L("累计范围：当前日历起点至所选日期。", "Cumulative range: calendar start through the selected date.") : L("周日到周六，按当前记录汇总。", "Sunday through Saturday, using available records."))}</p><div class="usage-day-rows">${days.filter(day => day.total > 0).map(day => `<div class="usage-token-row"><div><strong>${e(day.date)}</strong></div><b>${n(day.total)} ${e(L("次调用", "requests"))} · ${e(usageTokenValue(day, n))}${tokenUsageMissing(day) ? "" : " tokens"}</b></div>`).join("") || `<p class="muted">${e(L("暂无明细", "No details"))}</p>`}</div>`;
}

export function lifetimeUsageView(metrics, ui) {
  const { e, L, n, compact } = ui, usage = lifetimeUsage(metrics);
  if (!usage) return "";
  const card = (name, key, value, note, color) => `<article class="usage-kpi" style="--usage-color:${color}"><span>${e(name)}</span><strong data-lifetime="${key}" title="${n(value)}">${e(compact(value))}</strong><small>${note}</small></article>`;
  return `<section class="usage-workspace usage-lifetime-overview" aria-label="${e(L("累计用量", "Lifetime usage"))}">
    <div class="usage-controls"><div class="usage-title-row"><h2>${e(L("用量总览", "Usage overview"))}</h2><span class="scope-pill">${e(L("累计", "All time"))}</span></div><div class="usage-header-actions"><button class="usage-help-button" data-action="usage-methodology">${e(L("统计口径", "About these metrics"))}</button><button class="button small" data-action="lifetime-details">${e(L("累计明细", "Lifetime details"))}</button></div></div>
    <div class="usage-kpis">${card(L("累计 Token", "Total tokens"), "tokens", usage.tokens, `${n(usage.tokens)} tokens`, "#438ff4")}${card(L("调用次数", "Requests"), "total", usage.total, `${n(usage.failed)} ${e(L("次失败", "failures"))}`, "#36b989")}${card(L("输入 Token", "Input tokens"), "input", usage.input, `${n(usage.input)} tokens`, "#9b78ec")}${card(L("输出 Token", "Output tokens"), "output", usage.output, `${n(usage.output)} tokens`, "#ef9860")}</div>
  </section>`;
}

// Detailed provenance belongs to a deliberate inspection, separate from charts.
export function usageMethodologyView(metrics, state, ui) {
  const { e, L, n } = ui, usage = cachedUsage(metrics, state), storage = metrics?.storage;
  if (!usage) return "";
  const item = (title, body) => `<div><dt>${e(title)}</dt><dd>${e(body)}</dd></div>`;
  const snapshots = usage.sources.filter(source => source.kind === "snapshot" && source.snapshot_requests > 0);
  const started = new Date(usage.trackingSince).toISOString().replace("T", " ").slice(0, 19);
  return `<dl class="usage-methodology">
    ${item(L("统计范围", "Scope"), L("总览与累计明细采用网关保存的累计指标。时间与平台筛选应用于每日用量、趋势和模型占比。", "Overview and lifetime details use saved gateway counters. Date and platform filters apply to daily usage, trends and model shares."))}
    ${item(L("Token 口径", "Token calculation"), L("输入 Token + 输出 Token。思考 Token 已包含在输出中。积分消耗以平台费率为准。", "Input + output tokens. Reasoning is included in output tokens. Credits follow each platform’s rates."))}
    ${item(L("每日记录", "Daily records"), usage.preview ? L("当前使用最近 200 条请求样本。启用新版后端可保存逐日用量。", "Currently using the latest 200 request samples. Upgrade the backend to retain daily usage.") : L(`按 UTC 日期聚合，保留 ${usage.retention} 天；连续采集始于 ${started} UTC。归档日志按完成日期归属。`, `Aggregated by UTC date for ${usage.retention} days. Continuous collection began at ${started} UTC. Archived logs use completion dates.`))}
    ${item(L("数据覆盖", "Coverage"), L("按已保存的用量汇总。未上报 Token 的调用仍计入次数；对应模型没有 Token 记录时曲线留空，同日其他用量照常显示。含未上报调用的合计以 ≥ 表示已记录用量。", "Usage is aggregated from saved data. Requests without token usage still count toward requests. Only the affected model's token series has a gap; other recorded usage remains visible. Totals containing unreported usage show ≥ for the recorded amount."))}
    ${item(L("接收速度", "Receive speed"), L("实时输出按最近 3 秒实际收到的字符数计算，含思考与工具参数，自动刷新时每秒采样。Token/s 按同平台、同模型的完成样本估算并标记 ≈；最终 Token 采用上游用量。完成速度是网关接流到用量结束的平均接收速率，含网络等待与客户端背压，不代表模型内部解码速度。", "Live rates count characters received over the last 3 seconds, including reasoning and tool arguments, sampled every second. Token/s is calibrated from completed requests for the same platform/model and marked ≈; final tokens use upstream usage. Completed rates measure receipt from stream acceptance through final usage, including network waits and client backpressure, not model-internal decoding."))}
    ${item(L("累计分组", "Lifetime grouping"), L("累计明细按原始模型名、协议或账号标识分组。同名项合并展示；每日用量按平台与模型分别统计。调用记录保留最近 200 条。", "Lifetime details group by original model name, protocol or account label. Identical names are combined; daily usage separates platforms and models. Request logs retain the latest 200 entries."))}
    ${usage.allocation?.method ? item(L("历史用量归集", "Historical usage allocation"), L(`${usage.allocation.start} — ${usage.allocation.end} UTC，均摊 ${n(usage.allocation.total)} 次调用、${n(usage.allocation.input_tokens + usage.allocation.output_tokens)} Token。按指定规则统一归入 workbuddy/deepseek-v4.1-flash（国内版）。归集范围为累计与逐日记录的差额，属于统计分配；原始记录及实测活跃天数不变。`, `${usage.allocation.start} — ${usage.allocation.end} UTC: ${n(usage.allocation.total)} requests and ${n(usage.allocation.input_tokens + usage.allocation.output_tokens)} tokens distributed uniformly. The difference between lifetime and dated records is assigned to workbuddy/deepseek-v4.1-flash (China) by the configured allocation rule. This is a statistical allocation; original observations and measured activity counts are preserved.`)) : ""}
    ${usage.allocation?.excess && Object.values(usage.allocation.excess).some(value => value > 0) ? item(L("历史范围差异", "Historical scope difference"), L("部分归档指标大于当前累计值，已保留原记录，不进行负数分摊。", "Some archived metrics exceed current lifetime counters. Original records are retained without negative allocation.")) : ""}
    ${storage ? item(L("统计存储", "Statistics storage"), ({ ready: L("已启用", "Enabled"), new: L("等待首次写入", "Awaiting first save"), disabled: L("未启用", "Disabled"), recovery: L("独立恢复文件", "Recovery file") })[storage.status] || "—") + (storage.file ? item(L("当前文件", "Current file"), storage.file) : "") + (storage.loaded_from ? item(L("读取来源", "Loaded from"), storage.loaded_from) : "") + (storage.last_saved_at ? item(L("最近写入", "Last saved"), new Date(storage.last_saved_at).toLocaleString()) : "") + (storage.load_error ? item(L("读取异常", "Load error"), storage.load_error) : "") + (storage.save_error ? item(L("写入异常", "Save error"), storage.save_error) : "") : ""}
  </dl>${snapshots.length ? `<section class="usage-source-details"><h3>${e(L("历史快照", "Historical snapshots"))}</h3>${snapshots.map(source => `<div class="usage-source-row"><strong>${e(source.name)}</strong><span>${n(source.snapshot_requests)} ${e(L("次调用", "requests"))}</span><small>${n(source.snapshot_input + source.snapshot_output)} tokens</small></div>`).join("")}</section>` : ""}<div class="modal-actions"><button class="button" data-action="usage-allocation-settings">${e(L("统计设置", "Usage settings"))}</button></div>`;
}

export function usageView(metrics, state, ui) {
  const { e, L, n, compact, icon } = ui;
  const usage = cachedUsage(metrics, state);
  if (!usage) return "";
  const historyLabel = L("全部记录", "All records");
  const rangeName = state.range === "all" ? historyLabel : L(`近 ${state.range} 天`, `Last ${state.range} days`);
  const dateLabel = (date) => `${Number(date.slice(5, 7))}/${Number(date.slice(8))}`;
  const label = model => usageModelLabel(model, L);
  const buttons = (action, choices, selected) => `<div class="segmented">${choices.map(([value, text]) => `<button data-action="${action}" data-value="${e(value)}" aria-pressed="${selected === value}" class="${selected === value ? "active" : ""}">${e(text)}</button>`).join("")}</div>`;
  const start = usage.days[0]?.date || "—", end = usage.days.at(-1)?.date || "—";
  const tokenValue = usageTokenValue(usage, compact);
  const series = curveSeries(usage, state.metric, state.hidden), visible = series.filter((s) => !s.hidden);
  const peak = Math.max(1, ...visible.flatMap((s) => s.values.filter((v) => v != null)));
  const scale = Math.pow(10, Math.floor(Math.log10(peak)));
  const ymax = state.metric === "total" ? Math.max(4, Math.ceil(peak / 4) * 4) : Math.ceil(peak / scale) * scale;
  const plot = (viewWidth, left, right, mobile = false) => {
    const width = (right - left) / Math.max(1, usage.days.length - 1);
    const x = (i) => left + i * width, y = (v) => 238 - v / ymax * 196;
    const grid = [0, .25, .5, .75, 1].map((ratio) => `<line class="usage-grid" x1="${left}" x2="${right}" y1="${y(ratio * ymax)}" y2="${y(ratio * ymax)}"/><text x="${left - 12}" y="${y(ratio * ymax) + 4}" text-anchor="end">${e(compact(ymax * ratio))}</text>`).join("");
    const ticks = [...new Set([0, ...(mobile ? [Math.round((usage.days.length - 1) / 2)] : Array.from({ length: 4 }, (_, i) => Math.round((usage.days.length - 1) * (i + 1) / 5))), usage.days.length - 1])];
    const axis = ticks.map((i) => `<text x="${x(i)}" y="266" text-anchor="${i === 0 ? "start" : i === usage.days.length - 1 ? "end" : "middle"}">${e(dateLabel(usage.days[i].date))}</text>`).join("");
    const paths = visible.map((m) => `<path class="usage-curve" stroke="${m.color}" d="${curvePath(m.values, x, y)}"/>${m.values.map((v, i) => v > 0 ? `<circle cx="${x(i)}" cy="${y(v)}" r="2.5" fill="${m.color}" stroke="var(--surface)" stroke-width="1.5"/>` : "").join("")}`).join("");
    const hits = usage.days.map((day, i) => {
      const description = day.coverage === "unknown" ? L("无数据", "No data") : `${tokenUsageMissing(day) ? L("Token 未上报", "Token usage unavailable") : usageTokenValue(day, n)+" tokens"} · ${n(day.total)} ${L("次调用", "requests")}`;
      return `<rect class="chart-hit" data-chart-point="${i}" data-kind="usage" data-x="${x(i)}" data-view-width="${viewWidth}" x="${Math.max(left, x(i) - width / 2)}" y="36" width="${Math.min(width, right - Math.max(left, x(i) - width / 2)) || width}" height="206" tabindex="0" role="button" aria-label="${e(day.date + " · " + description)}"/>`;
    }).join("");
    return `<div class="chart-shell usage-chart-shell ${mobile ? "usage-mobile-chart" : "usage-desktop-chart"}"><svg viewBox="0 0 ${viewWidth} 284" class="usage-chart" role="group" aria-label="${e(L("每日模型用量趋势，可用方向键逐日查看", "Daily model usage; use arrow keys to inspect dates"))}">${grid}${axis}${paths}<line class="chart-crosshair" x1="${left}" x2="${left}" y1="36" y2="238" hidden/>${hits}</svg><div class="chart-tooltip usage-chart-tooltip" hidden role="status"></div></div>`;
  };
  const lineChart = plot(1000, 64, 968) + plot(500, 54, 478, true);

  // Calendar always shows the retention window, independently of the curve range.
  const heatMetric = state.heatMetric || "tokens", heatMode = state.heatMode || "daily";
  const columns = heatmapColumns(usage.calendar, heatMode, heatMetric);
  const weeks = columns;
  const labels = columns.map((column,i) => {
    const date=column.monthDate;
    const changed=i===0||date?.slice(0,7)!==columns[i-1].monthDate?.slice(0,7);
    return changed ? {index:i,date} : null;
  }).filter(Boolean).slice(-12);
  const monthLabels=labels.map(({index,date})=>`<span style="grid-column:${index+1}">${e(L(`${Number(date.slice(5,7))}月`,new Date(date+"T00:00:00Z").toLocaleDateString("en",{month:"short",timeZone:"UTC"})))}</span>`).join("");
  const heatCells=!state.calendarExpanded ? "" : columns.map((column,index)=>`<div class="usage-heat-week ${heatMode!=="daily"?"column-hover":""}">${column.cells.map(cell=>{
    if(cell.coverage==="blank") return '<span class="usage-heat-blank"></span>';
    const day=cell.day;
    const tokenSummary=heatMode==="daily"?day:{tokens:cell.value,unknown_token_requests:column.unknown_token_requests};
    const amount=cell.coverage==="unknown"?L("无数据", "No data"):heatMetric==="tokens"?tokenUsageMissing(tokenSummary)?L("Token 未上报","Token usage unavailable"):`${usageTokenValue(tokenSummary,n)} tokens`:`${n(cell.value)} ${L("次调用","requests")}`;
    const title=heatMode==="daily" ? `${day.date} UTC · ${amount}` : heatMode==="weekly" ? `${column.start} — ${column.end} UTC · ${L("本周","This week")} ${amount}` : `${L("截至","Through")} ${column.end} UTC · ${L("累计", "Cumulative")} ${amount}`;
    const activityOnly=heatMetric==="tokens"&&tokenUsageMissing(tokenSummary);
    const action=heatMode==="daily"?"usage-day":"usage-week";
    return `<button class="usage-heat-cell level-${cell.level} ${cell.coverage === "unknown" ? "unknown" : ""} ${activityOnly?"activity-only":""}" data-action="${action}" data-value="${heatMode==="daily"?day.date:index}" title="${e(title+(activityOnly?L(" · 仅调用记录", " · request records only"):""))}" aria-label="${e(title)}"></button>`;
  }).join("")}</div>`).join("");
  const donutTop = usage.models.filter((m) => m.tokens > 0).slice(0, 5);
  const remainder = usage.tokens - donutTop.reduce((sum, m) => sum + m.tokens, 0);
  const slices = [...donutTop, ...(remainder > 0 ? [{ key: "other", model: "other", color: "#8493a7", tokens: remainder }] : [])];
  let offset = 0;
  const gradient = slices.map((m) => { const from = offset; offset += m.tokens / usage.tokens * 100; return `${m.color} ${from}% ${offset}%`; }).join(",");
  const donut = `<div class="usage-token-donut ${usage.tokens ? "" : "empty"}" style="--distribution:${usage.tokens ? `conic-gradient(${gradient})` : "var(--surface-alt)"}" role="img" aria-label="${e(L(`所选范围 ${n(usage.tokens)} Token 的模型占比`, `Model share of ${n(usage.tokens)} tokens in this range`))}"><div><strong>${compact(usage.tokens)}</strong><span>tokens</span><small>${e(rangeName)}</small></div></div>`;
  const legend = slices.map((m) => `<div class="usage-token-row"><i style="background:${m.color}"></i><div><strong title="${e(label(m))}">${e(label(m))}</strong><small>${n(m.tokens)} tokens</small></div><b>${(m.tokens / usage.tokens * 100).toFixed(1)}%</b></div>`).join("");
  const kpi = (name, value, note, color) => `<article class="usage-kpi" style="--usage-color:${color}"><span>${e(name)}</span><strong>${e(value)}</strong><small>${note}</small></article>`;
  const calendar = `<details class="card usage-calendar" id="usageCalendarDetails" ${state.calendarExpanded ? "open" : ""}><summary class="usage-calendar-summary" id="usageCalendarToggle"><span><strong>${e(L("活动日历", "Activity calendar"))}</strong><small>${e(L(`近 ${usage.retention} 天 · UTC`, `Last ${usage.retention} days · UTC`))}</small></span><span>${e(L("每日 / 每周 / 累计", "Daily / weekly / cumulative"))} ${icon("chevron")}</span></summary>${state.calendarExpanded ? `<div class="usage-calendar-body"><div class="usage-card-head"><div><h3>${e(heatMetric === "total" ? L("调用活动", "Request activity") : L("Token 活动", "Token activity"))}</h3><p>${e(L(`近 ${usage.retention} 天 · UTC`, `Last ${usage.retention} days · UTC`))}</p></div><div class="usage-heat-controls">${buttons("usage-heat-mode", [["daily", L("每日", "Daily")], ["weekly", L("每周", "Weekly")], ["cumulative", L("累计", "Cumulative")]], heatMode)}${buttons("usage-heat-metric", [["tokens", "Token"], ["total", L("调用", "Requests")]], heatMetric)}</div></div><div class="usage-calendar-scroll"><div class="usage-calendar-content" style="--heat-weeks:${weeks.length}"><div class="usage-heat-months">${monthLabels}</div><div class="usage-heat-body"><div class="usage-weekdays"><span>${e(L("日", "S"))}</span><span></span><span>${e(L("二", "T"))}</span><span></span><span>${e(L("四", "T"))}</span><span></span><span>${e(L("六", "S"))}</span></div><div class="usage-heat-weeks">${heatCells}</div></div></div></div><div class="usage-calendar-foot"><span>${e(heatMode === "daily" ? L("每日用量", "Daily usage") : heatMode === "weekly" ? L("每周合计", "Weekly totals") : L("逐周累计", "Cumulative by week"))}</span><div class="usage-heat-key"><span>${e(L("少", "Less"))}</span>${[0, 1, 2, 3, 4].map((i) => `<i class="usage-heat-cell level-${i}"></i>`).join("")}<span>${e(L("多", "More"))}</span></div></div></div>` : ""}</details>`;
  return `<div class="usage-workspace">
    <div class="usage-controls usage-period-controls"><div><div class="usage-title-row"><h2>${e(L("用量趋势", "Usage trends"))}</h2></div><p>${e(`${start} — ${end} · UTC`)}</p></div><div class="usage-filter-controls"><label><span class="sr-only">${e(L("用量平台", "Usage platform"))}</span><select id="usagePlatform" class="select"><option value="all" ${state.platform === "all" ? "selected" : ""}>${e(L("全部平台", "All platforms"))}</option>${usage.platforms.map((id) => `<option value="${e(id || "unknown")}" ${(state.platform === id || (!id && state.platform === "unknown")) ? "selected" : ""}>${e(id || L("未记录平台", "Unrecorded platform"))}</option>`).join("")}</select></label>${buttons("usage-range", [["7", L("近 7 天", "7 days")], ["30", L("近 30 天", "30 days")], ["all", historyLabel]], state.range)}</div></div>
    <div class="usage-kpis usage-period-kpis">${kpi(L("Token 用量", "Token usage"), tokenValue, tokenUsageMissing(usage) ? e(L("Token 未上报", "Token usage unavailable")) : `${e(L("输入", "Input"))} ${compact(usage.input_tokens)} <span>·</span> ${e(L("输出", "Output"))} ${compact(usage.output_tokens)}`, "#438ff4")}${kpi(L("调用次数", "Requests"), n(usage.total), `${n(usage.failed)} ${e(L("次失败", "failures"))}`, "#36b989")}${kpi(L("单日峰值", "Peak day"), usage.peak ? compact(usage.peak.tokens) : "—", usage.peak ? e(`${usage.peak.date} · UTC`) : e(L("尚无 Token 记录", "No token records")), "#9b78ec")}${kpi(L("活跃天数", "Active days"), n(usage.activeDays), `${e(L("连续活跃", "Longest streak"))} ${usage.longestStreak} ${e(L("天", "days"))}`, "#ef9860")}</div>
    <div class="usage-analysis-grid"><section class="card usage-trend"><div class="usage-card-head"><div><h3>${e(L("每日用量", "Daily usage"))}</h3><p>${e(L("按平台与模型", "By platform and model"))}</p></div>${buttons("usage-metric", [["tokens", "Token"], ["total", L("调用", "Requests")]], state.metric)}</div><div class="usage-curve-legend">${series.map((m) => `<button data-action="usage-curve" data-value="${e(m.key)}" aria-pressed="${!m.hidden}" class="${m.hidden ? "is-hidden" : ""}" title="${e(label(m))}"><i style="background:${m.color}"></i><span>${e(label(m))}</span></button>`).join("")}</div>${lineChart}${!usage.total ? `<p class="usage-empty-note">${e(L("该时段暂无调用记录", "No requests in this period"))}</p>` : ""}<div class="usage-trend-foot"><span>${e(state.metric === "tokens" ? L("输入 + 输出 Token", "Input + output tokens") : L("已完成调用", "Completed requests"))}</span><span>${e(L("UTC", "UTC"))}</span></div></section>
    <section class="card usage-breakdown"><div class="usage-card-head"><div><h3>${e(L("模型用量", "Model usage"))}</h3><p>${e(L("Token 占比", "Token share"))}</p></div><span class="scope-pill">${usage.models.length} ${e(L("个模型", "models"))}</span></div><div class="usage-token-layout">${donut}<div class="usage-token-legend">${legend || `<p class="muted">${e(L("暂无 Token 用量", "No token usage"))}</p>`}</div></div>${usage.models.length ? `<details class="usage-model-details"><summary>${e(L("全部模型明细", "All model details"))}<span>${usage.models.length}${icon("chevron")}</span></summary><div class="table-wrap"><table><thead><tr><th>${e(L("平台 / 模型", "Platform / model"))}</th><th class="num">${e(L("调用", "Requests"))}</th><th class="num">${e(L("输入 Token", "Input tokens"))}</th><th class="num">${e(L("输出 Token", "Output tokens"))}</th><th class="num">${e(L("占比", "Share"))}</th><th></th></tr></thead><tbody>${usage.models.map((m) => `<tr><td><span class="usage-model-name"><i style="background:${m.color}"></i><span title="${e(label(m))}">${e(label(m))}</span></span></td><td class="num numeric">${m.total === 0 && m.estimated_tokens > 0 ? "—" : n(m.total)}</td><td class="num numeric">${tokenUsageMissing(m) ? e(L("未上报", "Not reported")) : n(m.input_tokens)}</td><td class="num numeric">${tokenUsageMissing(m) ? e(L("未上报", "Not reported")) : n(m.output_tokens)}</td><td class="num numeric">${!tokenUsageMissing(m) && usage.tokens ? (m.tokens / usage.tokens * 100).toFixed(1) + "%" : "—"}</td><td>${(m.estimated_requests || m.estimated_tokens) && m.total <= m.estimated_requests ? `<span class="muted">—</span>` : `<button class="button small" data-action="usage-model-requests" data-platform="${e(m.platform)}" data-model="${e(m.model)}">${e(L("记录", "Logs"))}</button>`}</td></tr>`).join("")}</tbody></table></div></details>` : ""}</section></div>
    ${calendar}
  </div>`;
}
export function usageTooltip(metrics, state, index, ui) {
  const { e, L, n } = ui, usage = cachedUsage(metrics, state), day = usage?.days[index];
  if (!day) return "";
  if (day.coverage === "unknown") return `<strong>${e(day.date)} · UTC</strong><p>${e(L("该日期暂无记录", "No records for this date"))}</p>`;
  const series = curveSeries(usage, state.metric, state.hidden).filter((m) => !m.hidden && m.values[index] > 0);
  return `<strong>${e(day.date)} · UTC</strong>${series.map((m) => `<div><i style="background:${m.color}"></i><span>${e(usageModelLabel(m, L))}</span><b>${state.metric === "tokens" && m.missingTokens[index] ? "≥ " : ""}${n(m.values[index])}</b></div>`).join("")}<div class="usage-tooltip-total"><span>${e(L("当日全部模型合计", "All models for this day"))}</span><b>${state.metric === "tokens" ? e(usageTokenValue(day, n)) + (tokenUsageMissing(day) ? "" : " tokens") : n(day.total) + " " + e(L("次", "requests"))}</b></div>`;
}

export function liveUsageView(data,error,points,ui) {
  const {e,L,n,timestamp}=ui,live=liveGeneration(data);
  if(ui.stale) return `<div class="usage-live-placeholder">${e(L("实时数据已过期，等待更新", "Live data is stale; awaiting an update"))}<button class="button small" data-action="refresh-live">${e(L("刷新", "Refresh"))}</button></div>`;
  if(!live||error) return `<div class="usage-live-placeholder">${e(error || L("正在连接…", "Connecting…"))}${error ? `<button class="button small" data-action="refresh-live">${e(L("重试", "Retry"))}</button>` : ""}</div>`;
  const calibrated=live.tokensPerSecond!=null, speed=calibrated?live.tokensPerSecond:live.charactersPerSecond;
  const values=points||[],maximum=Math.max(1,...values.map(p=>p.value??0));
  const first=values[0]?.time||0,last=values.at(-1)?.time||1;
  let pen=false;
  const path=values.map(point=>{if(point.value==null){pen=false;return "";}const command=pen?"L":"M";pen=true;return `${command}${(8+(point.time-first)/Math.max(1,last-first)*284).toFixed(1)},${(64-point.value/maximum*52).toFixed(1)}`;}).join(" ");
  const graph=values.length>1?`<svg viewBox="0 0 300 76" class="live-sparkline" role="img" aria-label="${e(L("本页最近 30 秒实际输出速度（字符/s）", "Observed output speed in the last 30 seconds, characters/s"))}"><path d="${path}"/></svg>`:"";
  return `<div class="usage-live-stats">
    <div><span>${e(L("进行中", "In flight"))}</span><strong>${n(live.inFlight)}<small>${e(L("个请求", "requests"))}</small></strong></div>
    <div><span>${e(calibrated?L("接收速度 · 估算", "Receive speed · estimated"):L("接收速度", "Receive speed"))}</span><strong>${calibrated?"≈ ":""}${speed.toFixed(1)}<small>${calibrated?"tokens/s":L("字符/s", "chars/s")}</small></strong></div>
    <div><span>${e(L("上次测得速度", "Last measured rate"))}</span><strong>${live.lastSpeed==null?"—":live.lastSpeed.toFixed(1)}<small>tokens/s</small></strong><small>${live.lastCompleted?e(timestamp(live.lastCompleted.time)):e(L("暂无数据", "No data"))}</small></div>
    <div class="live-sparkline-wrap">${graph}<small>${e(L("近 30 秒 · 字符/s", "Last 30 seconds · chars/s"))}</small></div>
  </div>${live.active.length ? `<div class="usage-live-phases">${livePhaseGroups(live.active,L,live.sampledAt).map(group=>`<span class="live-phase-badge ${group.receiving ? "receiving" : ""}"><span class="status-dot"></span>${e(group.label)}<b>${n(group.count)}</b></span>`).join("")}</div><details id="liveRequestDetails" class="live-request-details"><summary id="liveRequestToggle">${e(L(`${n(live.active.length)} 个活跃请求`, `${n(live.active.length)} active requests`))}<span>${e(L("查看明细", "View details"))}</span></summary><div class="live-request-list">${live.active.map(row=>`<div class="live-request-row"><div><div class="live-request-identity"><span class="status-dot"></span><span class="live-model" title="${e(`${row.platform||""}/${row.model||""}`)}">${e(row.model?`${row.platform||L("路由中", "Routing")}/${row.model}`:L("请求解析中", "Reading request"))}</span></div><div class="live-request-status"><span>${e(liveRequestStatus(row,L,live.sampledAt))}</span><span class="live-elapsed">${e(L("已用时", "Elapsed"))} ${e(liveRequestElapsed(row,live.sampledAt))}</span>${row.attempt_count>1?`<span>${n(row.attempt_count)} ${e(L("次尝试", "attempts"))}</span>`:""}</div></div><div class="live-request-output"><strong>${row.estimated_output_tokens==null?e(L("Token 待结算", "Tokens pending")):"≈ "+n(row.estimated_output_tokens)+" tokens"}</strong><small>${n(row.output_characters)} ${e(L("字符", "characters"))}</small></div></div>`).join("")}</div></details>` : ""}
  <div class="usage-live-footer"><span>${e(live.active.length?L("实时更新", "Live updates"):L("暂无进行中的请求", "No active requests"))}</span><span>${e(L("更新 ", "Updated "))}${e(timestamp(data.sampled_at))}</span></div>`;
}
