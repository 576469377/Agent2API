/* Agent2API 控制台
 *
 * 无框架、无构建步骤：所有数据来自网关自身的 /api/* 与 /v1/* 接口，
 * 图表用原生 SVG 绘制。唯一可选的外部资源是 KaTeX（检测到数学公式时
 * 才从 CDN 懒加载；离线时公式回退为等宽原文显示，不影响其他功能）。
 */
(() => {
  'use strict';

  const $ = (id) => document.getElementById(id);

  const state = {
    page: 'overview',
    status: null,
    metrics: null,
    platforms: null,
    models: null,
    config: null,
    modelsFilter: '',
    reqFilter: '',
  };

  let timer = null;

  /* ────────────────── HTTP ────────────────── */

  async function api(path, options) {
    const res = await fetch(path, options);
    if (!res.ok) {
      const detail = await res.json().catch(() => null);
      throw new Error((detail && detail.error && detail.error.message) || `HTTP ${res.status}`);
    }
    return res.json();
  }
  const get = (p) => api(p);
  const post = (p, body) =>
    api(p, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });

  /* ────────────────── 路由 ────────────────── */

  function switchPage(name) {
    state.page = name;
    document.querySelectorAll('.nav-item').forEach((b) =>
      b.classList.toggle('active', b.dataset.page === name));
    document.querySelectorAll('.page').forEach((p) =>
      p.classList.toggle('active', p.id === 'page-' + name));

    stopAutoRefresh();
    if (name === 'overview') { loadOverview(); startAutoRefresh(); }
    if (name === 'platforms') loadPlatforms();
    if (name === 'models') loadModels();
    if (name === 'requests') loadRequests();
    if (name === 'settings') loadSettings();
  }

  function startAutoRefresh() {
    timer = setInterval(() => { if (state.page === 'overview') loadOverview(true); }, 5000);
  }
  function stopAutoRefresh() { if (timer) { clearInterval(timer); timer = null; } }

  /* ────────────────── 概览 ────────────────── */

  async function loadOverview(silent) {
    try {
      const [st, mt] = await Promise.all([get('/api/status'), get('/api/metrics')]);
      state.status = st;
      state.metrics = mt;
      renderStatus(st);
      renderStats(mt);
      renderCharts(mt);
      renderRanks(mt);
      renderRecentShort(mt);
      $('ovLive').textContent = '更新于 ' + new Date().toLocaleTimeString('zh-CN');
    } catch (err) {
      if (!silent) showHealth(err.message);
    }
  }

  function renderStatus(st) {
    $('brandSub').textContent = st.platform + (st.account ? ' · ' + st.account : '');
    $('sideMeta').textContent = 'v' + st.version + ' · ' + st.go_version;
    $('healthText').textContent = '运行中';
    setDot('ok');
    $('ovSub').textContent =
      `监听 ${st.listen} · 已运行 ${fmtDuration(st.uptime_sec)} · 鉴权${st.auth_enabled ? '已启用' : '未启用'}`;
  }

  // fmtTPS 渲染解码速度。tps_samples 为 0 表示从未测到生成耗时
  // （例如刚重启且指标文件是旧格式），此时显示「—」而不是 "0.0 tok/s"。
  function fmtTPS(m) {
    if (!m.tps_samples) return { v: '—', sub: '暂无生成数据' };
    return { v: Number(m.avg_tps || 0).toFixed(1) + ' tok/s', sub: `基于 ${m.tps_samples} 次生成` };
  }

  function renderStats(m) {
    const tps = fmtTPS(m);
    const cards = [
      { k: '请求总数', v: fmtNum(m.total), sub: `近 1 分钟 ${m.last_minute_rpm}` },
      { k: '成功率', v: (m.success_rate * 100).toFixed(1) + '%', sub: `失败 ${m.failed} 次`, cls: m.failed ? 'err' : 'ok' },
      { k: '平均延迟', v: fmtMs(m.avg_latency_ms), sub: '全部请求' },
      { k: '解码速度', v: tps.v, sub: tps.sub },
      { k: '进行中', v: m.in_flight, sub: '当前并发' },
      { k: '输入 Token', v: fmtNum(m.input_tokens), sub: '' },
      { k: '输出 Token', v: fmtNum(m.output_tokens), sub: m.reasoning_tokens ? `思考 ${fmtNum(m.reasoning_tokens)}` : '' },
    ];
    $('statGrid').innerHTML = cards
      .map((c) => `<div class="stat ${c.cls || ''}">
        <div class="k">${esc(c.k)}</div>
        <div class="v">${esc(String(c.v))}</div>
        <div class="sub">${esc(c.sub || '')}</div>
      </div>`).join('');
  }

  // chartData 保存每个图表当前的数据，供悬停命中查询。
  //
  // 为什么不把监听挂在 SVG 上：renderCharts 每 5s 用 innerHTML 重建整个 SVG，
  // 挂在 SVG 上的监听会随之销毁。容器节点（#chartReq/#chartTok）始终存在，
  // 所以事件委托在容器上、数据也存在这里，就能免疫重建。
  const chartData = Object.create(null);

  function renderCharts(m) {
    const s = m.series || [];
    const reqHost = $('chartReq'), tokHost = $('chartTok');

    if (!s.length) {
      chartData.chartReq = null;
      chartData.chartTok = null;
      reqHost.innerHTML = emptyChart('暂无数据');
      tokHost.innerHTML = emptyChart('暂无数据');
      hideTip(reqHost); hideTip(tokHost);
      return;
    }

    const reqBox = chartBox(reqHost);
    const tokBox = chartBox(tokHost);

    chartData.chartReq = { series: s, box: reqBox, mode: 'line', accessor: (b) => b.total };
    chartData.chartTok = { series: s, box: tokBox, mode: 'bar', accessor: (b) => b.input_tokens + b.output_tokens };

    reqHost.innerHTML = lineChart(s, (b) => b.total, reqBox, { color: '#2563eb', label: '请求趋势：最近 30 分钟每分钟请求数' });
    tokHost.innerHTML = barChart(s, [
      (b) => b.input_tokens,
      (b) => b.output_tokens,
    ], [
      { name: '输入', color: '#93b4f7' },
      { name: '输出', color: '#2563eb' },
    ], tokBox);

    // 数据刚刚更新，旧的悬停位置已指向错位的桶 —— 直接收起气泡，
    // 下一次 pointermove 会在几毫秒内重新出现。
    hideTip(reqHost); hideTip(tokHost);
  }

  function renderRanks(m) {
    $('rankProto').innerHTML = rankRows(m.by_protocol || []);
    $('rankModel').innerHTML = rankRows(m.by_model || []);
  }

  function rankRows(rows) {
    if (!rows.length) return '<div class="rank-row"><span class="rank-name">暂无数据</span></div>';
    const max = Math.max(...rows.map((r) => r.total));
    return rows.slice(0, 8).map((r) => `
      <div class="rank-row">
        <span class="rank-name" title="${esc(r.key)}">${esc(r.key)}</span>
        <span class="rank-val">${r.total} 次 · ${(r.success_rate * 100).toFixed(0)}%</span>
        <span class="rank-bar"><i style="width:${(r.total / max) * 100}%"></i></span>
      </div>`).join('');
  }

  function renderRecentShort(m) {
    const rows = (m.recent || []).slice(0, 8);
    $('recentShort').innerHTML = rows.length
      ? `<div class="recent-mini">${rows.map(recentRow).join('')}</div>`
      : '<div class="rank-row"><span class="rank-name">暂无请求记录</span></div>';
  }

  function recentRow(r) {
    const t = new Date(r.time).toLocaleTimeString('zh-CN');
    return `<div class="recent-row">
      <span class="badge ${r.ok ? 'ok' : 'err'}">${r.ok ? '成功' : r.status || '失败'}</span>
      <span class="mono" style="min-width:56px;color:var(--text-faint)">${esc(t)}</span>
      <span style="color:var(--text-dim)">${esc(r.protocol)}</span>
      <span class="tag">${esc(r.model || '-')}</span>
      <span class="spacer"></span>
      <span style="color:var(--text-faint)">${fmtMs(r.duration_ms)}</span>
    </div>`;
  }

  /* ────────────────── SVG 图表 ────────────────── */

  // 图表布局常量。注意 viewBox 尺寸不再是固定的 600x140 —— 每次渲染都按容器
  // 实测像素尺寸重建，使 viewBox 单位与 CSS 像素 1:1。
  //
  // 旧实现用固定 viewBox + preserveAspectRatio="none" 把图拉伸到容器，
  // 横向与纵向缩放比不同（1920px 视口下横向 1.32x / 纵向 1.06x），
  // 导致 SVG 里的文字被横向拉伸约 1.25 倍 —— 这就是「字体是扁的」的根因。
  const W_FALLBACK = 600, H_FALLBACK = 148;
  const PAD = { l: 8, r: 12, t: 12, b: 20 };

  // chartBox 量测容器的绘制尺寸（CSS 像素）。
  // 每 5s 重绘时重新量测，所以窗口缩放无需 ResizeObserver；
  // 首帧可能尚未布局（宽度 0），此时回退到默认值，下一次刷新自然纠正。
  function chartBox(host) {
    if (!host) return { w: W_FALLBACK, h: H_FALLBACK };
    const w = Math.round(host.clientWidth), h = Math.round(host.clientHeight);
    return { w: w > 40 ? w : W_FALLBACK, h: h > 40 ? h : H_FALLBACK };
  }

  // svgWrap 以「实测像素 = viewBox 单位」渲染，因此不存在非等比拉伸。
  // 不要再加 preserveAspectRatio="none" —— 那正是文字变形的来源。
  function svgWrap(box, inner, label) {
    return `<svg viewBox="0 0 ${box.w} ${box.h}" width="${box.w}" height="${box.h}"
      role="img" aria-label="${esc(label || '趋势图')}"
      style="width:100%;height:100%">${inner}</svg>`;
  }

  function emptyChart(text) {
    return `<div style="height:100%;display:grid;place-items:center;color:var(--text-faint);font-size:12.5px">${esc(text)}</div>`;
  }

  // crosshair 是悬停时的十字准线层。它必须每次渲染重建（SVG 会被整体替换），
  // 且作为最后一个子元素以便盖在图形之上。实际位置在悬停时用 transform 设置。
  function crosshair(box) {
    return `<g class="xh" style="display:none">
      <line class="xh-line" x1="0" y1="${PAD.t}" x2="0" y2="${box.h - PAD.b}" vector-effect="non-scaling-stroke"/>
      <circle class="xh-dot" cx="0" cy="0" r="3.5"/>
    </g>`;
  }

  function lineChart(series, accessor, box, opts) {
    const W = box.w, H = box.h;
    const values = series.map(accessor);
    const max = Math.max(1, ...values);
    const n = values.length;
    // n === 1 时不能除以 n-1（会得到 NaN），单点居中放置。
    const dx = n > 1 ? (W - PAD.l - PAD.r) / (n - 1) : 0;
    const xy = values.map((v, i) => {
      const x = n > 1 ? PAD.l + i * dx : (W - PAD.l - PAD.r) / 2;
      const y = H - PAD.b - (v / max) * (H - PAD.t - PAD.b);
      return [x, y];
    });
    const line = xy.map((p, i) => `${i ? 'L' : 'M'}${p[0].toFixed(1)} ${p[1].toFixed(1)}`).join(' ');
    const area = `${line} L ${xy[n - 1][0].toFixed(1)} ${H - PAD.b} L ${xy[0][0].toFixed(1)} ${H - PAD.b} Z`;
    const grid = [0.25, 0.5, 0.75].map((f) => {
      const y = PAD.t + f * (H - PAD.t - PAD.b);
      return `<line class="grid" x1="${PAD.l}" y1="${y}" x2="${W - PAD.r}" y2="${y}" vector-effect="non-scaling-stroke"/>`;
    }).join('');
    const last = xy[n - 1];
    return svgWrap(box, `
      ${grid}
      ${xAxis(series, xy, box)}
      <path d="${area}" fill="${opts.color}" opacity="0.10"/>
      <path d="${line}" fill="none" stroke="${opts.color}" stroke-width="2" vector-effect="non-scaling-stroke"/>
      <circle cx="${last[0].toFixed(1)}" cy="${last[1].toFixed(1)}" r="3" fill="${opts.color}"/>
      <text class="note" x="${W - PAD.r}" y="12" text-anchor="end">峰值 ${max}</text>
      ${crosshair(box)}
    `, opts.label);
  }

  function barChart(series, accessors, defs, box) {
    const W = box.w, H = box.h;
    const n = series.length;
    const max = Math.max(1, ...series.flatMap((s) => accessors.map((a) => a(s))));
    const slot = (W - PAD.l - PAD.r) / Math.max(1, n);
    const bw = Math.max(1.5, (slot * 0.62) / accessors.length);
    const cx = (i) => PAD.l + i * slot + slot / 2;
    let bars = '';
    for (let i = 0; i < n; i++) {
      accessors.forEach((acc, si) => {
        const v = acc(series[i]) || 0;
        const h = (v / max) * (H - PAD.t - PAD.b);
        const x = PAD.l + i * slot + slot * 0.19 + si * bw;
        const y = H - PAD.b - h;
        bars += `<rect x="${x.toFixed(1)}" y="${y.toFixed(1)}" width="${bw.toFixed(1)}" height="${Math.max(h, 0.5).toFixed(1)}" fill="${defs[si].color}" rx="1"/>`;
      });
    }
    const legend = defs
      .map((s, i) => `<rect x="${PAD.l + i * 62}" y="3" width="8" height="8" rx="2" fill="${s.color}"/>
        <text class="legend" x="${PAD.l + i * 62 + 12}" y="11">${esc(s.name)}</text>`)
      .join('');
    const xyBars = series.map((_, i) => [cx(i), 0]);
    return svgWrap(box, `
      <line class="baseline" x1="${PAD.l}" y1="${H - PAD.b}" x2="${W - PAD.r}" y2="${H - PAD.b}" vector-effect="non-scaling-stroke"/>
      ${bars}${legend}${xAxis(series, xyBars, box)}
      <text class="note" x="${W - PAD.r}" y="11" text-anchor="end">峰值 ${fmtNum(max)}</text>
      ${crosshair(box)}
    `, 'Token 消耗趋势');
  }

  // xAxis 在时间轴底部绘制「-30m / -15m / 现在」这类相对标签。
  function xAxis(series, xy, box) {
    const H = box.h;
    const n = series.length;
    if (!n) return '';
    const lastMin = series[n - 1].minute;
    const idxs = [0, Math.floor((n - 1) / 2), n - 1].filter((v, i, a) => a.indexOf(v) === i);
    return idxs.map((i) => {
      const rel = lastMin - series[i].minute;
      const lbl = rel === 0 ? '现在' : '-' + rel + 'm';
      const anchor = i === 0 ? 'start' : i === n - 1 ? 'end' : 'middle';
      return `<text class="ax" x="${xy[i][0].toFixed(1)}" y="${H - 5}" text-anchor="${anchor}">${lbl}</text>`;
    }).join('');
  }

  /* ── 图表悬停：十字准线 + 数据气泡 ── */

  // tpsLabel 把一分钟桶的生成量换算成解码速度。
  // 没有生成耗时/输出 token 时返回「—」而不是 "0.0 tok/s" ——
  // 旧版指标文件没有 gen_* 字段，显示 0 会是在说谎。
  function tpsLabel(b) {
    if (!b.total) return '—';
    if (!b.gen_ms || !b.gen_tok) return '—';
    return (b.gen_tok / (b.gen_ms / 1000)).toFixed(1) + ' tok/s';
  }

  // ensureTooltip 在容器上创建一次气泡节点（不在 SVG 内，故不受重绘影响）。
  function ensureTooltip(host) {
    let t = host.querySelector('.chart-tip');
    if (!t) {
      t = document.createElement('div');
      t.className = 'chart-tip';
      t.setAttribute('aria-hidden', 'true');
      host.appendChild(t);
    }
    return t;
  }

  function hideTip(host) {
    const t = host && host.querySelector('.chart-tip');
    if (t) t.classList.remove('show');
    const g = host && host.querySelector('.xh');
    if (g) g.style.display = 'none';
  }

  // pxToViewBox 把鼠标的页面坐标反投影到 viewBox 坐标。
  //
  // 这是 SVG 视口变换的逆运算：viewBox 坐标 vx 会被映射到
  // r.left + vx * (r.width / box.w)。不能用 ev.offsetX —— 它给的是元素内
  // 像素偏移，与 viewBox 单位之间差一个缩放因子。
  function pxToViewBox(host, box, ev) {
    const r = host.getBoundingClientRect();
    if (!r.width || !r.height) return null;
    return {
      vx: ((ev.clientX - r.left) / r.width) * box.w,
      vy: ((ev.clientY - r.top) / r.height) * box.h,
    };
  }

  // nearestIndex 取距离光标最近的桶下标。
  // 折线图的点落在 slot 边界上（间距均分 n-1），柱状图占满 slot，故取整方式不同。
  function nearestIndex(box, vx, n, mode) {
    if (n <= 1) return 0;
    const inner = box.w - PAD.l - PAD.r;
    if (mode === 'line') {
      const step = inner / (n - 1);
      return Math.min(n - 1, Math.max(0, Math.round((vx - PAD.l) / step)));
    }
    const slot = inner / n;
    return Math.min(n - 1, Math.max(0, Math.floor((vx - PAD.l) / slot)));
  }

  function tipHTML(b, series) {
    const lastMin = series[series.length - 1].minute;
    const rel = lastMin - b.minute;
    const t = new Date(b.minute * 60000);
    const hh = String(t.getHours()).padStart(2, '0');
    const mm = String(t.getMinutes()).padStart(2, '0');
    const relLabel = rel === 0 ? '现在' : `${rel} 分钟前`;
    const rows = [
      ['请求', `${fmtNum(b.total)} 次`],
      ['输入', fmtNum(b.input_tokens)],
      ['输出', fmtNum(b.output_tokens)],
      ['TPS', tpsLabel(b)],
    ];
    return `<div class="tip-time">${esc(relLabel)} · ${hh}:${mm}</div>` +
      rows.map(([k, v]) => `<div class="tip-row"><span class="tip-k">${esc(k)}</span><span class="tip-v">${esc(v)}</span></div>`).join('');
  }

  function onChartMove(host, ev) {
    const d = chartData[host.id];
    if (!d || !d.series || !d.series.length) return;
    const v = pxToViewBox(host, d.box, ev);
    if (!v) return;

    const idx = nearestIndex(d.box, v.vx, d.series.length, d.mode);
    const b = d.series[idx];
    if (!b) return;

    // 准线：整层平移，线本身跨满高度，圆点单独定位。
    const inner = d.box.w - PAD.l - PAD.r;
    const xView = d.mode === 'line'
      ? (d.series.length > 1 ? PAD.l + ((idx * inner) / (d.series.length - 1)) : d.box.w / 2)
      : PAD.l + (idx + 0.5) * (inner / d.series.length);

    const g = host.querySelector('.xh');
    if (g) {
      g.style.display = '';
      const line = g.querySelector('.xh-line');
      const dot = g.querySelector('.xh-dot');
      if (line) { line.setAttribute('x1', xView.toFixed(1)); line.setAttribute('x2', xView.toFixed(1)); }
      if (dot) {
        const maxV = d.mode === 'line'
          ? Math.max(1, ...d.series.map(d.accessor))
          : Math.max(1, ...d.series.flatMap((s) => [s.input_tokens, s.output_tokens]));
        const val = d.mode === 'line' ? d.accessor(b) : (b.input_tokens + b.output_tokens);
        const y = d.box.h - PAD.b - (val / maxV) * (d.box.h - PAD.t - PAD.b);
        dot.setAttribute('cx', xView.toFixed(1));
        dot.setAttribute('cy', y.toFixed(1));
      }
    }

    const tip = ensureTooltip(host);
    tip.innerHTML = tipHTML(b, d.series);
    tip.classList.add('show');

    // 气泡位置用像素坐标：默认在光标右上，右/上越界时翻转并夹紧。
    const r = host.getBoundingClientRect();
    const px = ev.clientX - r.left, py = ev.clientY - r.top;
    let left = px + 14;
    if (left + tip.offsetWidth > r.width) left = px - tip.offsetWidth - 14;
    let top = py - tip.offsetHeight - 12;
    if (top < 0) top = py + 16;
    tip.style.left = Math.max(0, Math.min(left, Math.max(0, r.width - tip.offsetWidth))) + 'px';
    tip.style.top = Math.max(0, Math.min(top, Math.max(0, r.height - tip.offsetHeight))) + 'px';
  }

  // 监听挂在容器上（一次性），因此图表每 5s 重绘也不会丢交互。
  ['chartReq', 'chartTok'].forEach((id) => {
    const host = $(id);
    if (!host) return;
    host.addEventListener('pointermove', (ev) => onChartMove(host, ev));
    host.addEventListener('pointerleave', () => hideTip(host));
  });

  /* ────────────────── 平台 ────────────────── */

  async function loadPlatforms() {
    try {
      state.platforms = await get('/api/platforms');
      renderPlatforms(state.platforms);
    } catch (err) { showHealth(err.message); }
  }

  function renderPlatforms(d) {
    $('platformList').innerHTML = `<div class="platform-grid">${
      (d.active || []).map(platformCard).join('')}</div>`;
    $('plannedList').innerHTML = `<div class="platform-grid">${
      (d.planned || []).map((p) => `
        <div class="platform planned">
          <div class="platform-top">
            <span class="platform-name">${esc(p.name)}</span>
            <span class="tag">规划中</span>
          </div>
          <dl class="platform-kv">
            <dt>标识</dt><dd>${esc(p.id)}</dd>
            <dt>说明</dt><dd>${esc(p.note || '')}</dd>
          </dl>
        </div>`).join('')}</div>`;
  }

  function platformCard(p) {
    const badge = p.status === 'active'
      ? '<span class="badge ok">正常</span>'
      : p.status === 'degraded' ? '<span class="badge" style="background:#fef3c7;color:#92400e">降级</span>'
      : '<span class="badge err">异常</span>';
    return `<div class="platform">
      <div class="platform-top">
        <span class="platform-name">${esc(p.name)}</span>${badge}
      </div>
      <dl class="platform-kv">
        <dt>标识</dt><dd>${esc(p.id)}</dd>
        <dt>上游</dt><dd>${esc(p.base_url || '-')}</dd>
        <dt>账号</dt><dd>${esc(p.account || '-')}</dd>
        <dt>模型</dt><dd>${p.models ?? '-'} 个</dd>
        ${p.notes ? `<dt>备注</dt><dd>${esc(p.notes)}</dd>` : ''}
      </dl>
    </div>`;
  }

  /* ────────────────── 模型 ────────────────── */

  async function loadModels() {
    try {
      state.models = await get('/api/models');
      renderModels();
    } catch (err) { showHealth(err.message); }
  }

  function renderModels() {
    const d = state.models || {};
    const all = d.models || [];
    const q = state.modelsFilter.trim().toLowerCase();
    const rows = q ? all.filter((m) => m.id.toLowerCase().includes(q)) : all;
    $('modelsSub').textContent = `当前平台可用模型 · 共 ${all.length} 个`;
    $('modelTable').innerHTML = `
      <thead><tr>
        <th>模型 ID</th><th>名称</th><th class="num">上下文</th><th class="num">最大输出</th>
        <th>能力</th>
      </tr></thead>
      <tbody>${rows.map((m) => `
        <tr>
          <td class="mono">${esc(m.id)}</td>
          <td>${esc(m.display_name || '-')}</td>
          <td class="num">${m.context_tokens ? fmtNum(m.context_tokens) : '-'}</td>
          <td class="num">${m.max_output_tokens ? fmtNum(m.max_output_tokens) : '-'}</td>
          <td>
            ${m.supports_tools ? '<span class="tag on">工具</span>' : ''}
            ${m.supports_thinking ? '<span class="tag on">思考</span>' : ''}
            ${m.supports_images ? '<span class="tag on">视觉</span>' : ''}
            ${m.is_default ? '<span class="tag">默认</span>' : ''}
          </td>
        </tr>`).join('')}</tbody>`;
  }

  /* ────────────────── 调用日志 ────────────────── */

  async function loadRequests() {
    try {
      state.metrics = await get('/api/metrics');
      renderRequests();
    } catch (err) { showHealth(err.message); }
  }

  function renderRequests() {
    const m = state.metrics || {};
    let rows = m.recent || [];
    if (state.reqFilter === 'fail') rows = rows.filter((r) => !r.ok);
    if (state.reqFilter === 'ok') rows = rows.filter((r) => r.ok);
    $('reqTable').innerHTML = `
      <thead><tr>
        <th>时间</th><th>状态</th><th>协议</th><th>模型</th><th>方式</th>
        <th class="num">耗时</th><th class="num">输入</th><th class="num">输出</th><th>错误</th>
      </tr></thead>
      <tbody>${rows.length ? rows.map((r) => `
        <tr>
          <td class="mono">${esc(new Date(r.time).toLocaleString('zh-CN'))}</td>
          <td><span class="badge ${r.ok ? 'ok' : 'err'}">${r.ok ? '成功' : (r.status || '失败')}</span></td>
          <td>${esc(r.protocol)}</td>
          <td class="mono">${esc(r.model || '-')}</td>
          <td>${r.stream ? '流式' : '非流式'}</td>
          <td class="num">${fmtMs(r.duration_ms)}</td>
          <td class="num">${r.input_tokens || '-'}</td>
          <td class="num">${r.output_tokens || '-'}</td>
          <td style="color:var(--err);max-width:280px;overflow:hidden;text-overflow:ellipsis">${esc(r.error || '')}</td>
        </tr>`).join('') : '<tr><td colspan="9" style="text-align:center;color:var(--text-faint);padding:22px">暂无记录</td></tr>'}</tbody>`;
  }

  /* ────────────────── 设置 ────────────────── */

  async function loadSettings() {
    try {
      state.config = await get('/api/config');
      renderSettings(state.config);
      const st = state.status || await get('/api/status');
      $('usageView').innerHTML = kvRows([
        ['Chat Completions', `POST http://${esc(st.listen)}/v1/chat/completions`],
        ['Responses', `POST http://${esc(st.listen)}/v1/responses`],
        ['Anthropic Messages', `POST http://${esc(st.listen)}/v1/messages`],
        ['模型列表', `GET http://${esc(st.listen)}/v1/models`],
      ]);
    } catch (err) { showHealth(err.message); }
  }

  function renderSettings(c) {
    $('swSanitize').checked = !!c.sanitize;
    $('configView').innerHTML = kvRows([
      ['监听地址', c.listen],
      ['访问鉴权', c.auth_enabled ? '已启用' : '未启用'],
      ['内容脱敏', c.sanitize ? '开启' : '关闭'],
      ['上游平台', c.upstream.platform],
      ['上游地址', c.upstream.base_url],
      ['流式空闲超时', c.upstream.stream_idle_timeout],
      ['流式总超时', c.upstream.stream_total_timeout],
      ['请求超时', c.upstream.request_timeout],
      ['模型缓存', c.upstream.model_cache_ttl],
    ]);
  }

  function kvRows(pairs) {
    return `<dl class="kv">${pairs
      .map(([k, v]) => `<dt>${esc(k)}</dt><dd>${v}</dd>`).join('')}</dl>`;
  }

  /* ────────────────── 对话 ────────────────── */

  const el = {
    messages: $('messages'), input: $('input'), send: $('btnSend'), stop: $('btnStop'),
    status: $('status'), banner: $('banner'), model: $('model'),
  };
  let history = [], controller = null, busy = false;

  async function loadChatModels() {
    try {
      const d = await get('/v1/models');
      const ids = (d.data || []).map((m) => m.id).sort();
      el.model.innerHTML = ids.map((id) => `<option value="${esc(id)}">${esc(id)}</option>`).join('');
      el.model.value = ids.find((i) => i === 'auto') || ids[0] || '';
    } catch (err) { showBanner('获取模型列表失败：' + err.message); }
  }

  let bannerTimer = null;

  function showBanner(msg) {
    el.banner.textContent = msg;
    el.banner.classList.add('show');
    // 清掉上一次的定时器，否则连续两条提示时会被前一个提前收起。
    if (bannerTimer) clearTimeout(bannerTimer);
    bannerTimer = setTimeout(() => { el.banner.classList.remove('show'); bannerTimer = null; }, 6000);
  }

  function autoResize() {
    el.input.style.height = 'auto';
    el.input.style.height = Math.min(el.input.scrollHeight, 220) + 'px';
  }

  async function send() {
    if (busy) return;
    const text = el.input.value.trim();
    if (!text) return;
    hideEmpty();
    el.banner.classList.remove('show');
    // 同一个对象既进 history 也挂在 DOM 节点上，删除时可按引用精确摘除。
    const hist = { role: 'user', content: text };
    history.push(hist);
    addUser(text, hist);
    el.input.value = ''; autoResize();
    await streamChat();
  }

  async function streamChat() {
    setBusy(true);
    const started = performance.now();
    const msg = addAssistant();
    controller = new AbortController();

    const body = {
      model: el.model.value, messages: buildChatMessages(),
      stream: true, stream_options: { include_usage: true },
    };
    const t = parseFloat($('temperature').value); if (!isNaN(t)) body.temperature = t;
    const mt = parseInt($('maxTokens').value, 10); if (!isNaN(mt)) body.max_tokens = mt;
    if ($('effort').value) body.reasoning_effort = $('effort').value;

    try {
      const res = await fetch('/v1/chat/completions', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body), signal: controller.signal,
      });
      if (!res.ok) {
        const d = await res.json().catch(() => null);
        throw new Error((d && d.error && d.error.message) || `HTTP ${res.status}`);
      }
      const reader = res.body.getReader();
      const dec = new TextDecoder('utf-8');
      let buf = '', content = '', thinking = '', usage = null;
      const tools = new Map();
      const tick = setInterval(() => {
        el.status.textContent = ((performance.now() - started) / 1000).toFixed(1) + 's';
      }, 100);

      try {
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          buf += dec.decode(value, { stream: true });
          const lines = buf.split('\n');
          buf = lines.pop() ?? '';
          for (const line of lines) {
            const s = line.trim();
            if (!s.startsWith('data:')) continue;
            const p = s.slice(5).trim();
            if (!p || p === '[DONE]') continue;
            let j; try { j = JSON.parse(p); } catch { continue; }
            if (j.usage) usage = j.usage;
            if (j.error) throw new Error(j.error.message || '上游错误');
            const ch = (j.choices || [])[0]; if (!ch) continue;
            const d = ch.delta || {};
            if (d.reasoning_content) { thinking += d.reasoning_content; msg.setThinking(thinking); }
            if (d.content) { content += d.content; msg.setContent(content); }
            if (Array.isArray(d.tool_calls)) {
              for (const tc of d.tool_calls) {
                const i = tc.index ?? 0;
                const cur = tools.get(i) || { id: '', name: '', args: '' };
                if (tc.id) cur.id = tc.id;
                if (tc.function && tc.function.name) cur.name = tc.function.name;
                if (tc.function && tc.function.arguments) cur.args += tc.function.arguments;
                tools.set(i, cur);
              }
              msg.setTools([...tools.values()]);
            }
            if (ch.finish_reason) msg.setDone(ch.finish_reason);
          }
          el.messages.scrollTop = el.messages.scrollHeight;
        }
      } finally { clearInterval(tick); }

      msg.finish();
      if (usage) {
        el.status.textContent = `${((performance.now() - started) / 1000).toFixed(1)}s · 输入 ${usage.prompt_tokens || 0} / 输出 ${usage.completion_tokens || 0} tokens`;
      }
      const am = { role: 'assistant', content };
      if (tools.size) {
        am.tool_calls = [...tools.values()].map((x, i) => ({
          id: x.id || 'call_' + i, type: 'function',
          function: { name: x.name, arguments: x.args },
        }));
      }
      history.push(am);
      msg.bindHist(am); // 让「删除」按钮能按引用把这条从 history 摘掉
    } catch (err) {
      if (err.name === 'AbortError') { msg.finish(); el.status.textContent = '已停止'; }
      else {
        msg.setError(err.message); showBanner(err.message);
        if (history.length && history[history.length - 1].role === 'user') history.pop();
      }
    } finally { controller = null; setBusy(false); }
  }

  function buildChatMessages() {
    const sys = $('system').value.trim();
    return (sys ? [{ role: 'system', content: sys }] : []).concat(history);
  }

  function setBusy(v) {
    busy = v;
    el.send.style.display = v ? 'none' : '';
    el.stop.style.display = v ? '' : 'none';
    el.model.disabled = v;
    setDot(v ? 'busy' : 'ok');
  }

  // clearChat 就地清空对话页：中止在途请求、清 history 与 DOM、恢复空状态。
  //
  // 与 location.reload() 的关键区别是**不重载页面**：重载会把 state.page 重置为
  // overview（见文件末尾的启动流程），于是用户点「清空」却被弹回概览页。
  function clearChat() {
    // 1) 先中止在途流。顺序重要：abort 触发的 catch 在微任务里跑，
    //    此时 history 已被清空，它的 pop() 会因为 length 检查而成为空操作；
    //    若反过来先清 history，catch 可能操作到语义已失效的数组。
    if (controller) { controller.abort(); controller = null; }

    // 2) 移除所有消息节点，但保留 #empty（它只会被隐藏，见 hideEmpty）。
    Array.from(el.messages.querySelectorAll('.msg')).forEach((n) => n.remove());

    // 3) 重置会话状态与界面状态
    history.length = 0;
    busy = false;
    setBusy(false);
    el.status.textContent = '';
    el.banner.classList.remove('show');

    // 4) 恢复空状态并回到顶部
    showEmpty();
    el.messages.scrollTop = 0;
    el.input.focus();

    // 注意：不动 #model 与各采样参数 —— 那是用户显式设置项，清空对话不该重置它们。
  }

  // 空状态占位只能「隐藏」，不能 remove()。
  //
  // #empty 里的 .suggestions 按钮处理器是启动时一次性绑定（非事件委托），
  // 节点一旦被摘掉就无法安全重建 —— 重新 innerHTML 出来的是死按钮。
  // 因此清空对话要能把空状态原样复活，这里就必须只切 display。
  function hideEmpty() { const e = $('empty'); if (e) e.style.display = 'none'; }
  function showEmpty() { const e = $('empty'); if (e) e.style.display = ''; }

  // addUser 把历史对象挂到节点上（w._hist），使「删除」能精确地从 history 里摘掉它。
  // 用对象引用而非下标：下标在 splice 后需要重排，引用不需要。
  function addUser(text, hist) {
    const w = document.createElement('div');
    w.className = 'msg user';
    w.innerHTML = '<div class="msg-role">你</div><div class="msg-body"></div>' +
      '<div class="msg-actions"><button data-act="del">删除</button></div>';
    w.querySelector('.msg-body').textContent = text;
    w._hist = hist || null;
    el.messages.appendChild(w);
    bindMsgActions(w);
    el.messages.scrollTop = el.messages.scrollHeight;
  }

  function addAssistant() {
    const w = document.createElement('div');
    w.className = 'msg assistant';
    w.innerHTML = '<div class="msg-role">助手</div><div class="content"></div>' +
      '<div class="msg-actions"><button data-act="copy">复制</button><button data-act="del">删除</button></div>';
    el.messages.appendChild(w);
    const contentEl = w.querySelector('.content');
    let thinkEl = null, thinkBody = null, textEl = null, toolsEl = null, raw = '';

    const api = {
      setThinking(t) {
        if (!thinkEl) {
          thinkEl = document.createElement('details');
          thinkEl.className = 'thinking'; thinkEl.open = true;
          thinkEl.innerHTML = '<summary>思考中…</summary><div class="body"></div>';
          thinkBody = thinkEl.querySelector('.body');
          contentEl.appendChild(thinkEl);
        }
        thinkBody.textContent = t;
      },
      setContent(t) {
        if (!textEl) { textEl = document.createElement('div'); textEl.className = 'md'; contentEl.appendChild(textEl); }
        raw = t;
        textEl.dataset.md = t;
        mdSource.set(textEl, t);
        textEl.innerHTML = renderMarkdown(t) + '<span class="cursor"></span>';
        // KaTeX 尚未就绪时登记本节点，待其加载完成后统一升级；
        // 已就绪则确保它不在待升级集合里（幂等）。
        if (katexState !== 'ready' && window.katex === undefined) pendingMath.add(textEl);
        else pendingMath.delete(textEl);
      },
      setTools(calls) {
        if (!toolsEl) { toolsEl = document.createElement('div'); contentEl.appendChild(toolsEl); }
        toolsEl.innerHTML = calls.filter((c) => c.name)
          .map((c) => `<div class="tool-chip">🔧 ${esc(c.name)}(${esc(c.args || '')})</div>`).join('');
      },
      setDone() {
        if (thinkEl) thinkEl.querySelector('summary').textContent = '思考过程';
      },
      // bindHist 在 assistant 的历史对象构造完成后调用，把引用挂到消息节点上，
      // 使「删除」按钮能精确地从 history 中移除这一条。
      bindHist(h) { w._hist = h; },
      finish() {
        const c = contentEl.querySelector('.cursor'); if (c) c.remove();
        if (thinkEl) thinkEl.querySelector('summary').textContent = '思考过程';
      },
      setError(m) {
        const e = document.createElement('div');
        e.style.cssText = 'color:#991b1b;font-size:13.5px';
        e.textContent = '出错了：' + m;
        contentEl.appendChild(e);
      },
    };
    bindMsgActions(w, () => raw);
    el.messages.scrollTop = el.messages.scrollHeight;
    return api;
  }

  function bindMsgActions(w, getText) {
    w.querySelectorAll('.msg-actions button').forEach((b) => {
      b.addEventListener('click', async () => {
        if (b.dataset.act === 'del') {
          // 删除必须同时把消息从 history 摘掉，否则模型下一轮仍「记得」它。
          // 按对象引用查而不是按下标：splice 之后无需重排任何下标。
          if (w._hist) {
            const i = history.indexOf(w._hist);
            if (i >= 0) history.splice(i, 1);
          }
          w.remove();
          // 删光后恢复空状态（旧实现是 location.reload()，会把用户弹回概览页）。
          if (!el.messages.querySelector('.msg')) { showEmpty(); el.messages.scrollTop = 0; }
        } else if (b.dataset.act === 'copy' && getText) {
          try {
            await navigator.clipboard.writeText(getText());
            b.textContent = '已复制'; setTimeout(() => (b.textContent = '复制'), 1500);
          } catch { showBanner('复制失败'); }
        }
      });
    });
  }

  /* ────────────────── 工具 ────────────────── */

  function esc(s) {
    return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;')
      .replace(/>/g, '&gt;').replace(/"/g, '&quot;');
  }
  function fmtNum(n) {
    if (n >= 1e8) return (n / 1e8).toFixed(1) + '亿';
    if (n >= 1e4) return (n / 1e4).toFixed(1) + '万';
    return String(n);
  }
  function fmtMs(ms) {
    const v = Number(ms) || 0;
    return v >= 1000 ? (v / 1000).toFixed(2) + 's' : Math.round(v) + 'ms';
  }
  function fmtDuration(sec) {
    const s = Number(sec) || 0;
    if (s < 60) return s + ' 秒';
    if (s < 3600) return Math.floor(s / 60) + ' 分钟';
    if (s < 86400) return (s / 3600).toFixed(1) + ' 小时';
    return (s / 86400).toFixed(1) + ' 天';
  }
  function setDot(s) { $('dot').className = 'dot ' + s; }
  function showHealth(msg) { $('healthText').textContent = msg; setDot('err'); }

  function renderMarkdown(src) {
    const fences = [];
    let text = String(src).replace(/```(\w*)\n?([\s\S]*?)```/g, (_, lang, code) => {
      fences.push(code.replace(/\n$/, ''));
      return '\u0000F' + (fences.length - 1) + '\u0000';
    });
    // 数学公式：先于其他语法提取为占位符，避免被 Markdown/转义破坏
    const math = [];
    const stash = (html) => { math.push(html); return '\u0000M' + (math.length - 1) + '\u0000'; };
    text = text.replace(/\$\$([\s\S]+?)\$\$|\\\[([\s\S]+?)\\\]/g, (_, a, b) =>
      stash(mathHTML(a !== undefined ? a : b, true)));
    text = text.replace(/\\\(([\s\S]+?)\\\)/g, (_, m) => stash(mathHTML(m, false)));
    text = text.replace(/(^|[^\w$])\$([^$\n]+?)\$(?!\w)/g, (m, pre, body) => {
      // $...$ 仅在具备 LaTeX 特征（上下标/命令/花括号）时才当公式，避免误伤 "$5"、"美元" 等
      if (/^\s|\s$/.test(body) || !/[\^_\\{}]|\\frac|\\sqrt|\\sum|\\int|\\left/.test(body)) return m;
      return pre + stash(mathHTML(body, false));
    });
    text = esc(text);
    text = text.replace(/`([^`\n]+)`/g, '<code>$1</code>');
    text = text.replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>');
    text = text.replace(/^###\s+(.+)$/gm, '<h3>$1</h3>');
    text = text.replace(/^##\s+(.+)$/gm, '<h2>$1</h2>');
    text = text.replace(/^#\s+(.+)$/gm, '<h1>$1</h1>');
    text = text.replace(/^&gt;\s?(.+)$/gm, '<blockquote>$1</blockquote>');
    text = text.replace(/\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g,
      '<a href="$2" target="_blank" rel="noopener">$1</a>');
    text = text.replace(/(^|\n)((?:[-*]\s+.+\n?)+)/g, (_, pre, block) =>
      pre + '<ul>' + block.trim().split('\n').map((li) =>
        '<li>' + li.replace(/^[-*]\s+/, '') + '</li>').join('') + '</ul>');
    text = text.split(/\n{2,}/).map((b) => {
      if (!b.trim()) return '';
      if (/^\s*<(h\d|ul|ol|blockquote|pre)/.test(b)) return b;
      return '<p>' + b.replace(/\n/g, '<br>') + '</p>';
    }).join('');
    return text
      .replace(/\u0000M(\d+)\u0000/g, (_, i) => math[Number(i)])
      .replace(/\u0000F(\d+)\u0000/g, (_, i) =>
        `<pre><button class="copy-code" data-code="${encodeURIComponent(fences[Number(i)])}">复制</button>` +
        `<code>${esc(fences[Number(i)])}</code></pre>`);
  }

  /* ── 数学公式渲染：KaTeX 按需懒加载，加载失败/离线时回退为等宽原文 ── */

  let katexState = ''; // '' | 'loading' | 'ready' | 'failed'
  let katexRetryAt = 0; // 'failed' 后的退避截止时间戳（ms）
  const KATEX_RETRY_MS = 30000;
  const KATEX_VERSION = '0.16.11';

  // pendingMath 登记「公式还没升级成功」的 .md 节点（升级后即移除）。
  // mdSource 保存每个节点的最新原始 Markdown。
  //
  // 为什么按节点身份登记，而不是 KaTeX 就绪时扫一遍 document.querySelectorAll：
  // 流式渲染的 setContent 每次增量都会重写 innerHTML，但复用的是同一个 textEl 节点，
  // 所以节点身份在整条流里是稳定的 —— 登记不会因为重写 innerHTML 而失效。
  // 早期实现靠一次性扫描 DOM，只要扫描时机与节点创建/替换的微任务错开就会漏掉，
  // 漏掉的那条消息的公式就永远停在等宽原文。
  const pendingMath = new Set();
  const mdSource = new WeakMap();

  function ensureKatex() {
    if (katexState === 'loading' || katexState === 'ready') return;
    // 'failed' 允许重试（CDN 抖动/短暂离线是可恢复的），但要退避，
    // 否则离线状态下每渲染一段含公式的内容都会插一个注定失败的 script，形成请求风暴。
    if (katexState === 'failed' && Date.now() < katexRetryAt) return;

    katexState = 'loading';
    if (!document.getElementById('katex-css')) {
      const css = document.createElement('link');
      css.id = 'katex-css';
      css.rel = 'stylesheet';
      css.href = `https://cdn.jsdelivr.net/npm/katex@${KATEX_VERSION}/dist/katex.min.css`;
      document.head.appendChild(css);
    }
    const s = document.createElement('script');
    s.src = `https://cdn.jsdelivr.net/npm/katex@${KATEX_VERSION}/dist/katex.min.js`;
    s.onload = () => {
      katexState = 'ready';
      flushPendingMath();
    };
    s.onerror = () => {
      katexState = 'failed';
      katexRetryAt = Date.now() + KATEX_RETRY_MS;
    };
    document.head.appendChild(s);
  }

  // flushPendingMath 只重渲登记在册、且仍在文档中的节点。
  // 每个节点只会被升级一次（渲染后立刻从集合移除），
  // 否则会退化成「每次渲染都重登记」的 O(增量 × 节点)。
  function flushPendingMath() {
    pendingMath.forEach((n) => {
      if (!n.isConnected) { pendingMath.delete(n); return; } // 消息已被删除/清空
      const src = mdSource.get(n);
      if (src === undefined) { pendingMath.delete(n); return; }
      const hasCursor = !!n.querySelector('.cursor');
      n.innerHTML = renderMarkdown(src) + (hasCursor ? '<span class="cursor"></span>' : '');
      pendingMath.delete(n);
    });
  }

  function mathHTML(tex, display) {
    ensureKatex();
    if (window.katex) {
      try {
        return `<span class="${display ? 'math-display' : 'math-inline'}">` +
          window.katex.renderToString(tex, { displayMode: display, throwOnError: false }) + '</span>';
      } catch { /* 渲染失败回退原文 */ }
    }
    return `<code class="math-raw">${esc(tex)}</code>`;
  }

  el.messages.addEventListener('click', async (e) => {
    const b = e.target.closest('.copy-code'); if (!b) return;
    try {
      await navigator.clipboard.writeText(decodeURIComponent(b.dataset.code));
      b.textContent = '已复制'; setTimeout(() => (b.textContent = '复制'), 1500);
    } catch { showBanner('复制失败'); }
  });

  /* ────────────────── 事件绑定 ────────────────── */

  $('nav').addEventListener('click', (e) => {
    const b = e.target.closest('.nav-item'); if (b) switchPage(b.dataset.page);
  });
  document.querySelectorAll('[data-goto]').forEach((b) =>
    b.addEventListener('click', () => switchPage(b.dataset.goto)));

  $('btnRefresh').addEventListener('click', () => loadOverview());
  $('btnReloadPlatforms').addEventListener('click', loadPlatforms);
  $('btnReloadModels').addEventListener('click', loadModels);
  $('btnReloadReq').addEventListener('click', loadRequests);
  $('reqFilter').addEventListener('change', (e) => { state.reqFilter = e.target.value; renderRequests(); });
  $('modelFilter').addEventListener('input', (e) => { state.modelsFilter = e.target.value; renderModels(); });

  $('btnChatSettings').addEventListener('click', () => $('chatParams').classList.toggle('show'));
  $('btnClear').addEventListener('click', clearChat);
  el.send.addEventListener('click', send);
  el.stop.addEventListener('click', () => { if (controller) controller.abort(); });
  el.input.addEventListener('input', autoResize);
  el.input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); send(); }
  });
  document.querySelectorAll('.suggestions button').forEach((b) =>
    b.addEventListener('click', () => { el.input.value = b.dataset.prompt; el.input.focus(); autoResize(); }));

  $('swSanitize').addEventListener('change', async (e) => {
    try {
      const r = await post('/api/config', { sanitize: e.target.checked });
      renderSettings(r.config);
      $('healthText').textContent = '设置已生效';
    } catch (err) {
      e.target.checked = !e.target.checked;
      showBanner('保存失败：' + err.message);
    }
  });
  $('btnRefreshModels').addEventListener('click', async () => {
    try {
      await post('/api/config', { refresh_models: true });
      await loadModels();
      $('healthText').textContent = '模型清单已刷新';
    } catch (err) { showBanner('刷新失败：' + err.message); }
  });

  /* ────────────────── 启动 ────────────────── */

  loadChatModels();
  switchPage('overview');
})();
