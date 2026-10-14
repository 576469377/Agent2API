import { buildUsage, curveSeries, curvePath } from "./usage.js";

export function usageView(metrics, state, ui) {
  const { e, L, n, compact, icon } = ui;
  const usage = buildUsage(metrics, state);
  if (!usage) return "";
  const rangeName = state.range === "all" ? L("保留历史", "Retained history") : L(`近 ${state.range} 天`, `Last ${state.range} days`);
  const dateLabel = (date) => `${Number(date.slice(5, 7))}/${Number(date.slice(8))}`;
  const label = (model) => model.key === "other" ? L("其他模型", "Other models") : `${model.platform || L("未记录平台", "Unrecorded platform")}/${model.model || L("未记录模型", "Unrecorded model")}`;
  const buttons = (action, choices, selected) => `<div class="segmented">${choices.map(([value, text]) => `<button data-action="${action}" data-value="${e(value)}" aria-pressed="${selected === value}" class="${selected === value ? "active" : ""}">${e(text)}</button>`).join("")}</div>`;
  const coverage = usage.preview ? L("当前后端仅提供最近 200 条样本；长期统计需运行新版后端。", "This gateway provides only the latest 200 samples. Upgrade the backend for durable daily statistics.") : usage.partial ? L("部分日期只有旧版保留的样本，斜纹格表示未知历史；首日从启用时间开始统计。", "Some dates contain only retained legacy samples. Hatched cells mark unknown history; tracking starts partway through the first day.") : L("范围内按天统计完整保留，随调用完成自动更新。", "Daily aggregates are retained for this range and update as requests complete.");
  const start = usage.days[0]?.date || "—", end = usage.days.at(-1)?.date || "—";
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
    const paths = visible.map((m) => `<path class="usage-curve" stroke="${m.color}" d="${curvePath(m.values, x, y)}"/>${m.values.map((v, i) => v > 0 ? `<circle cx="${x(i)}" cy="${y(v)}" r="${usage.days[i].coverage === "partial" ? 3.5 : 2.5}" fill="${m.color}" stroke="var(--surface)" stroke-width="1.5"/>` : "").join("")}`).join("");
    const hits = usage.days.map((day, i) => {
      const description = day.coverage === "unknown" ? L("历史未保留", "History unavailable") : `${n(day.tokens)} tokens · ${n(day.total)} ${L("次调用", "requests")}`;
      return `<rect class="chart-hit" data-chart-point="${i}" data-kind="usage" data-x="${x(i)}" data-view-width="${viewWidth}" x="${Math.max(left, x(i) - width / 2)}" y="36" width="${Math.min(width, right - Math.max(left, x(i) - width / 2)) || width}" height="206" tabindex="0" role="button" aria-label="${e(day.date + " · " + description)}"/>`;
    }).join("");
    return `<div class="chart-shell usage-chart-shell ${mobile ? "usage-mobile-chart" : "usage-desktop-chart"}"><svg viewBox="0 0 ${viewWidth} 284" class="usage-chart" role="group" aria-label="${e(L("每日模型用量趋势，可用方向键逐日查看", "Daily model usage; use arrow keys to inspect dates"))}">${grid}${axis}${paths}<line class="chart-crosshair" x1="${left}" x2="${left}" y1="36" y2="238" hidden/>${hits}</svg><div class="chart-tooltip usage-chart-tooltip" hidden role="status"></div></div>`;
  };
  const lineChart = plot(1000, 64, 968) + plot(500, 54, 478, true);

  // Calendar always shows the retention window, independently of the curve range.
  const cells = [...usage.calendar];
  const firstWeekday = new Date(cells[0].ms).getUTCDay();
  for (let i = 0; i < firstWeekday; i++) cells.unshift(null);
  while (cells.length % 7) cells.push(null);
  const weeks = Array.from({ length: cells.length / 7 }, (_, i) => cells.slice(i * 7, i * 7 + 7));
  const heatMetric = state.heatMetric || "tokens", maxHeat = Math.max(1, ...usage.calendar.map((d) => d[heatMetric]));
  const monthLabels = weeks.map((week, i) => {
    const day = week.find((d) => d?.date.endsWith("-01")) || (i === 0 ? week.find(Boolean) : null);
    return day ? `<span style="grid-column:${i + 1}">${e(L(`${Number(day.date.slice(5, 7))}月`, new Date(day.ms).toLocaleDateString("en", { month: "short", timeZone: "UTC" })))}</span>` : "";
  }).join("");
  const heatCells = weeks.map((week) => `<div class="usage-heat-week">${week.map((day) => {
    if (!day) return '<span class="usage-heat-blank"></span>';
    const level = day[heatMetric] > 0 ? Math.max(1, Math.ceil(day[heatMetric] / maxHeat * 4)) : 0;
    const info = day.coverage === "unknown" ? L("历史未保留", "History unavailable") : `${n(day.tokens)} tokens · ${n(day.total)} ${L("次调用", "requests")}${day.coverage === "partial" ? L(" · 部分记录", " · partial records") : ""}`;
    return `<button class="usage-heat-cell level-${level} ${day.coverage}" data-action="usage-day" data-value="${day.date}" title="${e(`${day.date} UTC · ${info}`)}" aria-label="${e(`${day.date} · ${info}`)}"></button>`;
  }).join("")}</div>`).join("");
  const donutTop = usage.models.filter((m) => m.tokens > 0).slice(0, 5);
  const remainder = usage.tokens - donutTop.reduce((sum, m) => sum + m.tokens, 0);
  const slices = [...donutTop, ...(remainder > 0 ? [{ key: "other", model: "other", color: "#8493a7", tokens: remainder }] : [])];
  let offset = 0;
  const gradient = slices.map((m) => { const from = offset; offset += m.tokens / usage.tokens * 100; return `${m.color} ${from}% ${offset}%`; }).join(",");
  const donut = `<div class="usage-token-donut ${usage.tokens ? "" : "empty"}" style="--distribution:${usage.tokens ? `conic-gradient(${gradient})` : "var(--surface-alt)"}" role="img" aria-label="${e(L(`所选范围 ${n(usage.tokens)} Token 的模型占比`, `Model share of ${n(usage.tokens)} tokens in this range`))}"><div><strong>${compact(usage.tokens)}</strong><span>tokens</span><small>${e(rangeName)}</small></div></div>`;
  const legend = slices.map((m) => `<div class="usage-token-row"><i style="background:${m.color}"></i><div><strong title="${e(label(m))}">${e(label(m))}</strong><small>${n(m.tokens)} tokens</small></div><b>${(m.tokens / usage.tokens * 100).toFixed(1)}%</b></div>`).join("");
  const kpi = (name, value, note, color) => `<article class="usage-kpi" style="--usage-color:${color}"><span>${e(name)}</span><strong>${e(value)}</strong><small>${note}</small></article>`;
  return `<div class="usage-workspace">
    <div class="usage-controls"><div><span class="usage-section-eyebrow">USAGE INSIGHTS</span><h2>${e(L("用量洞察", "Usage insights"))}</h2><p>${e(`${start} — ${end} · UTC`)}${usage.partial ? `<span class="tag amber">${e(L("含部分历史", "Partial history"))}</span>` : ""}</p></div><div class="usage-filter-controls"><label><span class="sr-only">${e(L("用量平台", "Usage platform"))}</span><select id="usagePlatform" class="select"><option value="all" ${state.platform === "all" ? "selected" : ""}>${e(L("全部平台", "All platforms"))}</option>${usage.platforms.map((id) => `<option value="${e(id || "unknown")}" ${(state.platform === id || (!id && state.platform === "unknown")) ? "selected" : ""}>${e(id || L("未记录平台", "Unrecorded platform"))}</option>`).join("")}</select></label>${buttons("usage-range", [["7", L("近 7 天", "7 days")], ["30", L("近 30 天", "30 days")], ["all", L("保留历史", "History")]], state.range)}</div></div>
    <div class="usage-kpis">${kpi(L("Token 用量", "Token usage"), compact(usage.tokens), `${e(L("输入", "Input"))} ${compact(usage.input_tokens)} <span>·</span> ${e(L("输出", "Output"))} ${compact(usage.output_tokens)}`, "#438ff4")}${kpi(L("完成调用", "Completed requests"), n(usage.total), `${n(usage.failed)} ${e(L("次失败", "failures"))}`, "#36b989")}${kpi(L("单日峰值", "Peak day"), usage.peak ? compact(usage.peak.tokens) : "—", usage.peak ? e(`${usage.peak.date} · UTC`) : e(L("尚无 Token 记录", "No token records")), "#9b78ec")}${kpi(L("活跃天数", "Active days"), n(usage.activeDays), `${e(L("范围内最长连续", "Longest streak in range"))} ${usage.longestStreak} ${e(L("天", "days"))}`, "#ef9860")}</div>
    <section class="card usage-calendar"><div class="usage-card-head"><div><h3>${e(L("Token 活动", "Token activity"))}</h3><p>${e(L(`近 ${usage.retention} 天 · 独立于下方时间范围`, `Last ${usage.retention} days · independent of the selected range`))}</p></div>${buttons("usage-heat-metric", [["tokens", "Token"], ["total", L("调用", "Requests")]], heatMetric)}</div><div class="usage-calendar-scroll"><div class="usage-calendar-content" style="--heat-weeks:${weeks.length}"><div class="usage-heat-months">${monthLabels}</div><div class="usage-heat-body"><div class="usage-weekdays"><span>${e(L("日", "S"))}</span><span></span><span>${e(L("二", "T"))}</span><span></span><span>${e(L("四", "T"))}</span><span></span><span>${e(L("六", "S"))}</span></div><div class="usage-heat-weeks">${heatCells}</div></div></div></div><div class="usage-calendar-foot"><span>${e(L("点击日期查看模型明细", "Select a date for model details"))}</span><div class="usage-heat-key"><i class="usage-heat-cell unknown"></i>${e(L("未知", "Unknown"))}<i class="usage-heat-cell partial level-1"></i>${e(L("部分", "Partial"))}<span>${e(L("少", "Less"))}</span>${[0, 1, 2, 3, 4].map((i) => `<i class="usage-heat-cell level-${i}"></i>`).join("")}<span>${e(L("多", "More"))}</span></div></div></section>
    <section class="card usage-trend"><div class="usage-card-head"><div><h3>${e(L("每日模型趋势", "Daily model trend"))}</h3><p>${e(L("悬停查看明细 · 点击图例隐藏曲线", "Hover for details · select legends to hide curves"))}</p></div>${buttons("usage-metric", [["tokens", "Token"], ["total", L("调用", "Requests")]], state.metric)}</div><div class="usage-curve-legend">${series.map((m) => `<button data-action="usage-curve" data-value="${e(m.key)}" aria-pressed="${!m.hidden}" class="${m.hidden ? "is-hidden" : ""}" title="${e(label(m))}"><i style="background:${m.color}"></i><span>${e(label(m))}</span></button>`).join("")}</div>${lineChart}${!usage.total ? `<p class="usage-empty-note">${e(L("当前范围还没有可用的调用记录。未知日期不会按零用量绘制曲线。", "No request records in this range. Unknown dates are not plotted as zero usage."))}</p>` : ""}<div class="usage-trend-foot"><span>${e(state.metric === "tokens" ? L("输入 + 输出 Token；思考 Token 不重复相加", "Input + output tokens; reasoning is not counted twice") : L("已完成的调用，包含失败请求", "Completed requests, including failures"))}</span><span>${e(L("日期按请求开始时间归属 · UTC", "Dates follow request start time · UTC"))}</span></div></section>
    <section class="card usage-breakdown"><div class="usage-card-head"><div><h3>${e(L("模型 Token 占比", "Model token share"))}</h3><p>${e(L("与上方范围、平台一致 · 包含全部模型", "Same range and platform · includes every model"))}</p></div><span class="scope-pill">${usage.models.length} ${e(L("个模型", "models"))}</span></div><div class="usage-token-layout">${donut}<div class="usage-token-legend">${legend || `<p class="muted">${e(L("尚无 Token 用量；零 Token 调用仍计入调用次数。", "No token usage; zero-token requests still count as requests."))}</p>`}</div></div>${usage.models.length ? `<details class="usage-model-details"><summary>${e(L("全部模型明细", "All model details"))}<span>${usage.models.length}${icon("chevron")}</span></summary><div class="table-wrap"><table><thead><tr><th>${e(L("实际平台 / 模型", "Actual platform / model"))}</th><th class="num">${e(L("调用", "Requests"))}</th><th class="num">${e(L("输入 Token", "Input tokens"))}</th><th class="num">${e(L("输出 Token", "Output tokens"))}</th><th class="num">${e(L("占比", "Share"))}</th><th></th></tr></thead><tbody>${usage.models.map((m) => `<tr><td><span class="usage-model-name"><i style="background:${m.color}"></i><span title="${e(label(m))}">${e(label(m))}</span></span></td><td class="num numeric">${n(m.total)}</td><td class="num numeric">${n(m.input_tokens)}</td><td class="num numeric">${n(m.output_tokens)}</td><td class="num numeric">${usage.tokens ? (m.tokens / usage.tokens * 100).toFixed(1) + "%" : "—"}</td><td><button class="button small" data-action="usage-model-requests" data-platform="${e(m.platform)}" data-model="${e(m.model)}">${e(L("记录", "Logs"))}</button></td></tr>`).join("")}</tbody></table></div></details>` : ""}</section>
    <div class="usage-data-note">${icon("info")}<div><p>${e(coverage)}</p><small>${e(L("Token 不等于积分或账单金额。统计仅涵盖经过此网关的请求。", "Tokens are not credits or billing amounts. Statistics include only requests through this gateway."))} ${!usage.preview ? e(L(`完整采集启用于 ${new Date(usage.trackingSince).toISOString().replace("T", " ").slice(0, 19)} UTC；按天历史最多保留 ${usage.retention} 天。`, `Full tracking enabled at ${new Date(usage.trackingSince).toISOString()} · retains up to ${usage.retention} days.`)) : ""}</small></div></div>
  </div>`;
}
export function usageTooltip(metrics, state, index, ui) {
  const { e, L, n } = ui, usage = buildUsage(metrics, state), day = usage?.days[index];
  if (!day) return "";
  if (day.coverage === "unknown") return `<strong>${e(day.date)} · UTC</strong><p>${e(L("历史未保留，不能视为零用量", "History unavailable; not zero usage"))}</p>`;
  const series = curveSeries(usage, state.metric, state.hidden).filter((m) => !m.hidden && m.values[index] > 0);
  return `<strong>${e(day.date)} · UTC${day.coverage === "partial" ? ` · ${e(L("部分记录", "Partial records"))}` : ""}</strong>${series.map((m) => `<div><i style="background:${m.color}"></i><span>${e(m.key === "other" ? L("其他模型", "Other models") : `${m.platform || L("未记录平台", "Unrecorded")}/${m.model}`)}</span><b>${n(m.values[index])}</b></div>`).join("")}<div class="usage-tooltip-total"><span>${e(L("当日全部模型合计", "All models for this day"))}</span><b>${n(day[state.metric])} ${state.metric === "tokens" ? "tokens" : e(L("次", "requests"))}</b></div>`;
}
