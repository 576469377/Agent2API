/* Agent2API 控制台
 *
 * 无框架、无构建步骤：所有数据来自网关自身的 /api/* 与 /v1/* 接口，
 * 图表用原生 SVG 绘制。唯一可选的外部资源是 KaTeX（检测到数学公式时
 * 才从 CDN 懒加载；离线时公式回退为等宽原文显示，不影响其他功能）。
 */
(() => {
  'use strict';

  const $ = (id) => document.getElementById(id);

  /* ────────────────── i18n 与主题 ──────────────────
     静态文本走 data-i18n 声明式替换；JS 拼接的动态文案走 t(key)。
     两者共用同一本字典；偏好存 localStorage，切换即时生效。 */

  const I18N = {
    en: {
      'nav.overview': 'Overview', 'nav.requests': 'Logs', 'nav.accounts': 'Accounts',
      'nav.platforms': 'Platforms', 'nav.models': 'Models', 'nav.chat': 'Chat', 'nav.settings': 'Settings',
      'page.overview': 'Overview', 'page.requests': 'Request Logs', 'page.accounts': 'Accounts',
      'page.platforms': 'Platforms', 'page.models': 'Models', 'page.chat': 'Chat', 'page.settings': 'Settings',
      'sub.overview': 'Gateway health and usage statistics',
      'sub.chat': 'Chat with the gateway directly to verify protocol behavior',
      'sub.accounts': 'Upstream accounts in the pool and their login state',
      'sub.platforms': 'Connected upstream platforms, and what is planned',
      'sub.models': 'Models available on the current platform',
      'sub.requests': 'Last 200 requests',
      'sub.settings': 'Runtime options and effective configuration',
      'theme.auto': 'System', 'theme.light': 'Light', 'theme.dark': 'Dark',
      'group.ops': 'Operations', 'group.upstream': 'Upstream', 'group.tools': 'Tools',
      'stat.peak': 'Peak', 'stat.total': 'Requests', 'stat.success': 'Success rate', 'stat.latency': 'Avg latency',
      'stat.tps': 'Decode speed', 'stat.inflight': 'In flight', 'stat.in': 'Input tokens',
      'stat.out': 'Output tokens', 'stat.failed': 'failed', 'stat.rpm': 'last 1 min',
      'stat.all': 'all requests', 'stat.now': 'current concurrency',
      'stat.nogen': 'no generation data', 'stat.based': 'based on', 'stat.gen': 'generations',
      'stat.think': 'thinking', 'chart.req': 'Request trend', 'chart.tok': 'Token usage',
      'chart.win': 'last 30 min', 'chart.in': 'Input', 'chart.out': 'Output',
      'chart.proto': 'By protocol', 'chart.model': 'By model', 'chart.acct': 'Account usage',
      'chart.recent': 'Recent requests', 'chart.viewall': 'View all', 'chart.byreq': 'by requests',
      'btn.refresh': 'Refresh', 'btn.add': 'Add account', 'btn.reload': 'Reload', 'btn.pull': 'Pull',
      'btn.save': 'Save', 'btn.clearkey': 'Clear', 'btn.send': 'Send', 'btn.stop': 'Stop',
      'btn.clear': 'Clear', 'btn.params': 'Params',
      'acct.next': 'next up', 'acct.ok': 'OK', 'acct.ratelimited': 'Rate limited',
      'acct.unauthorized': 'Auth failed', 'acct.disabled': 'Disabled',
      'acct.relogin': 'Re-login', 'acct.disable': 'Disable', 'acct.enable': 'Enable',
      'acct.reset': 'Reset cooldown', 'acct.state': 'State', 'acct.cool': 'Cooldown left',
      'acct.modelslimited': 'models limited',
      'acct.usage': 'Usage', 'acct.success': 'Success rate', 'acct.lasterr': 'Last error',
      'acct.src': 'Source', 'acct.srcdesktop': 'Desktop client', 'acct.srcfile': 'Pool file',
      'login.ok': 'valid until', 'login.expiring': 'expiring soon (auto-refresh)',
      'login.dead': 'Login expired, re-login required',
      'req.time': 'Time', 'req.status': 'Status', 'req.proto': 'Protocol', 'req.model': 'Model',
      'req.acct': 'Account', 'req.mode': 'Mode', 'req.dur': 'Duration', 'req.err': 'Error',
      'req.stream': 'stream', 'req.nostream': 'final', 'req.none': 'No records yet',
      'req.loading': 'Loading', 'req.loadfail': 'Failed to load', 'req.nofilter': 'No records match the filter',
      'req.tryall': 'Try switching back to "All"',
      'mx.title': 'Account × model matrix', 'mx.accounts': 'accounts', 'mx.models': 'models',
      'mx.cooling': 'rate-limited cells', 'mx.ok': 'supported', 'mx.unsupported': 'not supported',
      'mx.ratelimited': 'rate limited (cooldown)', 'mx.nodata': 'No matrix data',
      'mx.nodatahint': 'Requires pool mode; switch back to list view for single-account mode',
      'mx.accooled': 'accounts fully cooled', 'mx.acccool': 'whole account cooled (not per-model)',
      'mdl.id': 'Model ID', 'mdl.name': 'Name', 'mdl.ctx': 'Context', 'mdl.maxout': 'Max output',
      'mdl.cap': 'Capabilities', 'mdl.tools': 'tools', 'mdl.think': 'thinking', 'mdl.vision': 'vision',
      'mdl.default': 'default', 'mdl.nomodel': 'No models returned by upstream',
      'set.runtime': 'Runtime switches', 'set.effective': 'Effective configuration',
      'set.access': 'Access endpoints', 'set.note': 'Listen address, upstream URL and timeouts require a restart — the console does not fake it.',
      'toast.saved': 'Settings saved', 'toast.keyrefreshed': 'Model list refreshed',
      'toast.keysaved': 'Key saved', 'toast.keycleared': 'Key cleared',
      'time.now': 'now', 'time.ago': (m) => `-${m}m`,
      'mx.minutesago': (m) => `${m} min ago`,
      'tip.reqs': 'Requests',
    },
    zh: {
      'nav.overview': '概览', 'nav.requests': '调用日志', 'nav.accounts': '账号',
      'nav.platforms': '平台', 'nav.models': '模型', 'nav.chat': '对话', 'nav.settings': '设置',
      'page.overview': '概览', 'page.requests': '调用日志', 'page.accounts': '账号',
      'page.platforms': '平台', 'page.models': '模型', 'page.chat': '对话', 'page.settings': '设置',
      'sub.overview': '网关运行状况与用量统计',
      'sub.chat': '直接与网关对话，验证协议与模型表现',
      'sub.accounts': '号池中的上游账号与登录状态',
      'sub.platforms': '已接入的上游平台，以及后续规划',
      'sub.models': '当前平台可用模型',
      'sub.requests': '最近 200 条请求',
      'sub.settings': '运行期可调项与当前生效配置',
      'theme.auto': '跟随系统', 'theme.light': '浅色', 'theme.dark': '深色',
      'group.ops': '运营', 'group.upstream': '上游', 'group.tools': '工具',
      'stat.peak': '峰值', 'stat.total': '请求总数', 'stat.success': '成功率', 'stat.latency': '平均延迟',
      'stat.tps': '解码速度', 'stat.inflight': '进行中', 'stat.in': '输入 Token',
      'stat.out': '输出 Token', 'stat.failed': '失败', 'stat.rpm': '近 1 分钟',
      'stat.all': '全部请求', 'stat.now': '当前并发',
      'stat.nogen': '暂无生成数据', 'stat.based': '基于', 'stat.gen': '次生成',
      'stat.think': '思考', 'chart.req': '请求趋势', 'chart.tok': 'Token 消耗',
      'chart.win': '最近 30 分钟', 'chart.in': '输入', 'chart.out': '输出',
      'chart.proto': '协议分布', 'chart.model': '模型调用排行', 'chart.acct': '账号用量排行',
      'chart.recent': '最近请求', 'chart.viewall': '查看全部', 'chart.byreq': '按请求数',
      'btn.refresh': '刷新', 'btn.add': '添加账号', 'btn.reload': '刷新', 'btn.pull': '拉取',
      'btn.save': '保存', 'btn.clearkey': '清除', 'btn.send': '发送', 'btn.stop': '停止',
      'btn.clear': '清空', 'btn.params': '参数',
      'acct.next': '下一个使用', 'acct.ok': '正常', 'acct.ratelimited': '限流中',
      'acct.unauthorized': '鉴权失效', 'acct.disabled': '已停用',
      'acct.relogin': '重新登录', 'acct.disable': '停用', 'acct.enable': '启用',
      'acct.reset': '清除冷却', 'acct.state': '状态', 'acct.cool': '剩余冷却',
      'acct.modelslimited': '个模型限流',
      'acct.usage': '用量', 'acct.success': '成功率', 'acct.lasterr': '最近错误',
      'acct.src': '来源', 'acct.srcdesktop': '桌面客户端', 'acct.srcfile': '号池文件',
      'login.ok': '有效至', 'login.expiring': '凭证即将过期（网关会自动刷新）',
      'login.dead': '登录已失效，需重新登录',
      'req.time': '时间', 'req.status': '状态', 'req.proto': '协议', 'req.model': '模型',
      'req.acct': '账号', 'req.mode': '方式', 'req.dur': '耗时', 'req.err': '错误',
      'req.stream': '流式', 'req.nostream': '非流式', 'req.none': '还没有请求记录',
      'req.loading': '加载中', 'req.loadfail': '加载失败', 'req.nofilter': '没有符合筛选条件的记录',
      'req.tryall': '试试切回「全部」',
      'mx.title': '账号 × 模型矩阵', 'mx.accounts': '个账号', 'mx.models': '个模型',
      'mx.cooling': '个格子限流中', 'mx.ok': '支持', 'mx.unsupported': '不支持',
      'mx.ratelimited': '限流冷却中', 'mx.nodata': '暂无矩阵数据',
      'mx.nodatahint': '需要号池模式；单账号模式请切回列表视图',
      'mx.accooled': '个账号整号冷却', 'mx.acccool': '整号冷却（非按模型）',
      'mdl.id': '模型 ID', 'mdl.name': '名称', 'mdl.ctx': '上下文', 'mdl.maxout': '最大输出',
      'mdl.cap': '能力', 'mdl.tools': '工具', 'mdl.think': '思考', 'mdl.vision': '视觉',
      'mdl.default': '默认', 'mdl.nomodel': '上游未返回任何模型',
      'set.runtime': '运行期开关', 'set.effective': '当前生效配置',
      'set.access': '接入方式', 'set.note': '监听地址、上游地址、超时等需要重启生效，请用配置文件或启动参数修改，控制台不做假动作。',
      'toast.saved': '设置已生效', 'toast.keyrefreshed': '模型清单已刷新',
      'toast.keysaved': '密钥已保存', 'toast.keycleared': '密钥已清除',
      'time.now': '现在', 'time.ago': (m) => `-${m}m`,
      'mx.minutesago': (m) => `${m} 分钟前`,
      'tip.reqs': '请求',
    },
  };

  let LANG = localStorage.getItem('agent2api_lang') || 'zh';
  const THEME_KEY = 'agent2api_theme';

  // t 取当前语言的文案；函数型词条支持参数；缺键回落中文原文。
  function t(key, ...args) {
    const v = (I18N[LANG] && I18N[LANG][key]) ?? I18N.zh[key];
    return typeof v === 'function' ? v(...args) : (v ?? key);
  }

  function applyLang() {
    document.querySelectorAll('[data-i18n]').forEach((el) => {
      const v = I18N[LANG] && I18N[LANG][el.dataset.i18n];
      if (typeof v === 'string') el.textContent = v;
    });
    // 导航分组是中文写死的静态锚点，双语值都在字典里。
    const groupMap = { '运营': 'group.ops', '上游': 'group.upstream', '工具': 'group.tools',
      'Operations': 'group.ops', 'Upstream': 'group.upstream', 'Tools': 'group.tools' };
    document.querySelectorAll('.nav-group').forEach((el) => {
      const key = groupMap[el.textContent.trim()];
      if (key) el.textContent = t(key);
    });
    document.documentElement.lang = LANG === 'en' ? 'en' : 'zh-CN';
    // 当前页面的动态内容同样换语言。
    if (state.page === 'overview' && state.metrics) { renderStats(state.metrics); renderRanks(state.metrics); }
    if (state.page === 'accounts' && state.accounts) renderAccountsPage(state.accounts, state.metrics);
    if (state.page === 'models') renderModels();
    if (state.page === 'requests') renderRequests();
  }

  function applyTheme() {
    const pref = localStorage.getItem(THEME_KEY) || 'auto';
    document.documentElement.removeAttribute('data-theme');
    if (pref === 'dark' || pref === 'light') {
      document.documentElement.setAttribute('data-theme', pref);
    }
    // 图表系列色随主题变化，重画当前图表。
    if (state.page === 'overview' && state.metrics) renderCharts(state.metrics);
  }

  const state = {
    page: 'overview',
    status: null,
    metrics: null,
    platforms: null,
    models: null,
    config: null,
    modelsFilter: '',
    reqFilter: '',
    reqLoading: false,
    reqError: '',
    modelsLoading: false,
    modelsError: '',
    accounts: null,
    modelView: 'list',
    matrix: null,
  };

  let timer = null;

  /* ────────────────── HTTP ────────────────── */

  // 网关访问密钥。启用鉴权（-api-key）后，控制台也必须携带它才能调用 /api/*。
  // 存 localStorage：这是本机管理面板，密钥本来就在启动参数/配置文件里躺着，
  // 不引入 localStorage 只会让用户每次刷新都重输一遍。
  const KEY_STORE = 'agent2api_key';

  function storedKey() { try { return localStorage.getItem(KEY_STORE) || ''; } catch { return ''; } }
  function saveKey(k) {
    try { if (k) localStorage.setItem(KEY_STORE, k); else localStorage.removeItem(KEY_STORE); } catch { /* 隐私模式等 */ }
  }

  async function api(path, options = {}) {
    const key = storedKey();
    if (key) {
      const h = new Headers(options.headers || {});
      if (!h.has('X-Api-Key') && !h.has('Authorization')) h.set('X-Api-Key', key);
      options.headers = h;
    }
    const res = await fetch(path, options);
    if (!res.ok) {
      const detail = await res.json().catch(() => null);
      const err = new Error((detail && detail.error && detail.error.message) || `HTTP ${res.status}`);
      err.status = res.status;
      throw err;
    }
    return res.json();
  }
  const get = (p) => api(p);
  const post = (p, body) =>
    api(p, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });

  // showAuthPrompt 在收到 401 时引导输入密钥。成功后自动重载当前页数据。
  async function showAuthPrompt(err) {
    const k = prompt('网关已启用 API Key 鉴权，本次请求被拒绝（' + (err && err.message || '401') + '）。\n请输入访问密钥（与 -api-key 一致）：', storedKey());
    if (k === null) return false;
    const key = k.trim();
    // 必须先验证再持久化：验证请求本身失败（网关不可达）不能算通过，
    // 否则网络抖动时输错的密钥也会被存下来。
    let verified = false;
    try {
      const r = await fetch('/api/status', { headers: { 'X-Api-Key': key } });
      verified = r.ok;
    } catch { verified = false; }
    if (!verified) {
      toast('密钥未通过验证（网络异常或密钥错误），未保存', 'error');
      return false;
    }
    saveKey(key);
    location.reload();
    return true;
  }

  /* ────────────────── 路由 ────────────────── */

  // 页面元信息：面包屑与标题。
  const PAGES = {
    overview: { group: '运营', title: '概览' },
    requests: { group: '运营', title: '调用日志' },
    accounts: { group: '上游', title: '账号' },
    platforms: { group: '上游', title: '平台' },
    models: { group: '上游', title: '模型' },
    chat: { group: '工具', title: '对话' },
    settings: { group: '工具', title: '设置' },
  };

  function switchPage(name) {
    state.page = name;
    document.querySelectorAll('.nav-item').forEach((b) =>
      b.classList.toggle('active', b.dataset.page === name));
    document.querySelectorAll('.page').forEach((p) =>
      p.classList.toggle('active', p.id === 'page-' + name));

    renderCrumb(name);

    stopAutoRefresh();
    if (name === 'overview') { loadOverview(); startAutoRefresh(); }
    if (name === 'accounts') loadAccounts();
    if (name === 'platforms') loadPlatforms();
    if (name === 'models') { loadModels(); startAutoRefresh(); }
    if (name === 'requests') loadRequests();
    if (name === 'settings') loadSettings();
  }

  // renderCrumb 用 textContent 拼面包屑，不引入新的转义面。
  function renderCrumb(name) {
    const meta = PAGES[name];
    if (!meta) return;
    const el = document.querySelector('#page-' + name + ' [data-crumb]');
    if (!el) return;
    el.textContent = '';
    const g = document.createElement('span');
    g.textContent = meta.group;
    const sep = document.createElement('i');
    sep.textContent = '/';
    const t = document.createElement('strong');
    t.textContent = meta.title;
    el.append(g, sep, t);
  }

  // 自动刷新：1s tick 驱动 5s 周期。三个要点：
  //  · 倒计时让用户知道页面在自己更新；
  //  · 页面切到后台就不打接口（省电、省上游额度）；
  //  · 401 无视 silent —— 否则密钥失效时自动刷新静默失败，页面永远停在旧数据。
  let tick = 5;
  let lastUpdated = '';
  let ovFetching = false;
  let silentFails = 0;

  function startAutoRefresh() {
    stopAutoRefresh();
    tick = 5;
    timer = setInterval(() => {
      if (document.hidden) return;
      // 矩阵倒计时独立于概览自动刷新：页面在模型页时也要走。
      tickMatrixCooldowns();
      if (state.page !== 'overview') return;
      if (tick > 1) {
        tick--;
        renderLive();
        return;
      }
      tick = 5;
      if (ovFetching) return;
      ovFetching = true;
      loadOverview(true).finally(() => { ovFetching = false; });
    }, 1000);
  }

  function renderLive() {
    if (lastUpdated) $('ovLive').textContent = `更新于 ${lastUpdated} · ${tick}s 后刷新`;
  }

  function stopAutoRefresh() { if (timer) { clearInterval(timer); timer = null; } }

  /* ────────────────── 概览 ────────────────── */

  async function loadOverview(silent) {
    try {
      const [st, mt, acc] = await Promise.all([
        get('/api/status'), get('/api/metrics'),
        // 账号列表供「账号用量排行」过滤已删除的凭证残留。
        get('/api/accounts').catch(() => null),
      ]);
      if (acc) state.accounts = acc;
      state.status = st;
      state.metrics = mt;
      renderStatus(st);
      renderStats(mt);
      renderCharts(mt);
      renderRanks(mt);
      renderRecentShort(mt);
      silentFails = 0;
      lastUpdated = new Date().toLocaleTimeString('zh-CN');
      tick = 5;
      $('ovLive').textContent = '更新于 ' + lastUpdated;
    } catch (err) {
      // 401 必须立刻提示（可能是密钥失效），不能因为 silent 就吞掉。
      if (err.status === 401) { showHealth(err.message, err); return; }
      if (!silent) { showHealth(err.message, err); return; }
      // 静默轮询连续失败两次才打扰用户：偶发抖动不该弹提示。
      silentFails++;
      if (silentFails >= 2) toast('数据刷新失败：' + err.message, 'error');
    }
  }

  function renderStatus(st) {
    // 品牌副行（平台名/账号）不再展示：单平台下是冗余信息，视觉上也突兀。
    const bs = $('brandSub');
    bs.textContent = '';
    bs.style.display = 'none';
    $('sideMeta').textContent = 'v' + st.version + ' · ' + st.go_version;
    $('healthText').textContent = '运行中';
    setDot('ok');
    $('ovSub').textContent =
      `监听 ${st.listen} · 已运行 ${fmtDuration(st.uptime_sec)} · 鉴权${st.auth_enabled ? '已启用' : '未启用'}`;
  }

  // fmtTPS 渲染解码速度。tps_samples 为 0 表示从未测到生成耗时
  // （例如刚重启且指标文件是旧格式），此时显示「—」而不是 "0.0 tok/s"。
  function fmtTPS(m) {
    if (!m.tps_samples) return { v: '—', sub: t('stat.nogen') };
    return { v: Number(m.avg_tps || 0).toFixed(1) + ' tok/s', sub: `${t('stat.based')} ${m.tps_samples} ${t('stat.gen')}` };
  }

  function renderStats(m) {
    const tps = fmtTPS(m);
    const cards = [
      { k: t('stat.total'), v: fmtNum(m.total), sub: `${t('stat.rpm')} ${m.last_minute_rpm}` },
      { k: t('stat.success'), v: (m.success_rate * 100).toFixed(1) + '%', sub: `${t('stat.failed')} ${m.failed}`, cls: m.failed ? 'err' : 'ok' },
      { k: t('stat.latency'), v: fmtMs(m.avg_latency_ms), sub: t('stat.all') },
      { k: t('stat.tps'), v: tps.v, sub: tps.sub },
      { k: t('stat.inflight'), v: m.in_flight, sub: t('stat.now') },
      { k: t('stat.in'), v: fmtNum(m.input_tokens), sub: '', tip: String(m.input_tokens) },
      { k: t('stat.out'), v: fmtNum(m.output_tokens), sub: m.reasoning_tokens ? `${t('stat.think')} ${fmtNum(m.reasoning_tokens)}` : '', tip: String(m.output_tokens) },
    ];
    $('statGrid').innerHTML = cards
      .map((c) => `<div class="stat ${c.cls || ''}"${c.tip ? ` title="${esc(c.tip)}"` : ''}>
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

    // 系列色从 CSS 变量读取：暗色模式下变量被整体覆盖，图表随之换色，
    // 无需在 JS 里维护第二套主题色（与 style.css 的「JS 读 CSS 变量」约定一致）。
    const cs = getComputedStyle(document.documentElement);
    const chartLine = cs.getPropertyValue('--chart-line').trim() || '#2563eb';
    const barIn = cs.getPropertyValue('--chart-bar-in').trim() || '#93b4f7';
    const barOut = cs.getPropertyValue('--chart-bar-out').trim() || '#2563eb';

    reqHost.innerHTML = lineChart(s, (b) => b.total, reqBox, { color: chartLine, label: '请求趋势：最近 30 分钟每分钟请求数' });
    tokHost.innerHTML = barChart(s, [
      (b) => b.input_tokens,
      (b) => b.output_tokens,
    ], [
      { name: '输入', color: barIn },
      { name: '输出', color: barOut },
    ], tokBox);

    // 数据刚刚更新，旧的悬停位置已指向错位的桶 —— 直接收起气泡，
    // 下一次 pointermove 会在几毫秒内重新出现。
    hideTip(reqHost); hideTip(tokHost);
  }

  function renderRanks(m) {
    $('rankProto').innerHTML = rankRows(m.by_protocol || []);
    $('rankModel').innerHTML = rankRows(m.by_model || []);
    // 账号排行：仅号池模式才展示，且**只显示当前池里存在的账号**——
    // 已删除凭证的历史用量（如 dedupe 清掉的重复号）不该继续占排行。
    const current = new Set((state.accounts && state.accounts.accounts
      ? state.accounts.accounts.map((a) => a.label)
      : (m.by_account || []).map((g) => g.key)));
    // accounts 页没加载时兜底放行（否则首次进概览会全隐藏）。
    const acc = (m.by_account || []).filter((g) => g.key !== '(unknown)'
      && (state.accounts ? current.has(g.key) : true));
    const panel = $('panelAcctRank');
    if (acc.length) {
      panel.style.display = '';
      $('rankAccount').innerHTML = rankRows(acc);
    } else {
      panel.style.display = 'none';
    }
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
    // 局部变量叫 timeStr 而非 t——全局的 t() 是翻译函数，遮蔽会让
    // 新增的 t('req.acct') 调用变成「字符串不可调用」的运行时错误。
    const timeStr = new Date(r.time).toLocaleTimeString('zh-CN');
    return `<div class="recent-row">
      <span class="badge ${r.ok ? 'ok' : 'err'}">${r.ok ? '成功' : r.status || '失败'}</span>
      <span class="mono" style="min-width:56px;color:var(--text-faint)">${esc(timeStr)}</span>
      <span class="tag">${esc(r.model || '-')}</span>
      <span class="tag" title="${esc(t('req.acct'))}: ${esc(r.account || '-')}">${esc(r.account || '—')}</span>
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
      <text class="note" x="${W - PAD.r}" y="12" text-anchor="end">${esc(t('stat.peak'))} ${fmtNum(max)}</text>
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
      <text class="note" x="${W - PAD.r}" y="11" text-anchor="end">${esc(t('stat.peak'))} ${fmtNum(max)}</text>
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
    const tm = new Date(b.minute * 60000);
    const hh = String(tm.getHours()).padStart(2, '0');
    const mm = String(tm.getMinutes()).padStart(2, '0');
    const relLabel = rel === 0 ? t('time.now') : t('mx.minutesago')(rel);
    const rows = [
      [t('tip.reqs'), `${fmtNum(b.total)} ${t('req.acct') === '账号' ? '次' : ''}`],
      [t('stat.in'), fmtNum(b.input_tokens)],
      [t('stat.out'), fmtNum(b.output_tokens)],
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
      // 平台与号池并行拉取：号池是这一页的第一层级信息。
      const [d, acc] = await Promise.all([
        get('/api/platforms'),
        get('/api/accounts').catch(() => null),
      ]);
      state.platforms = d;
      renderAccounts(acc);
      renderPlatforms(d);
    } catch (err) { showHealth(err.message, err); }
  }

  // renderAccounts 渲染平台页的号池摘要（详情在「账号」页）。
  function renderAccounts(d) {
    const host = $('accountPool');
    const list = (d && d.accounts) || [];
    if (!list.length) {
      // 单账号模式：不占版面，直接隐藏整块。
      host.innerHTML = '';
      return;
    }
    const head = `<h3 class="sec-title">账号号池
      <span class="panel-note" style="margin-left:8px;font-weight:400">
        ${d.healthy}/${d.total} 可用 · 严格轮询${list.some((a) => a.is_next) ? ' · ▸ 为下一个使用' : ''}
      </span></h3>`;
    host.innerHTML = head + `<div class="acct-grid">${list.map(accountCard).join('')}</div>`;
  }

  function accountCard(st) {
    const lv = st.healthy ? 'ok' : (st.reason === 'rate_limited' ? 'warn' : 'err');
    const label = st.healthy ? '正常'
      : st.reason === 'rate_limited' ? '限流中'
      : st.reason === 'unauthorized' ? '鉴权失效'
      : st.reason === 'disabled' ? '已停用' : '不可用';
    const cool = st.cooldown_secs > 0 ? fmtCountdown(st.cooldown_secs) : '—';
    return `<div class="acct" data-level="${lv}"${st.is_next ? ' data-next' : ''}>
      <div class="acct-top">
        <span class="acct-name" title="${esc(st.label)}">${st.is_next ? '▸ ' : ''}${esc(st.label)}</span>
        <span class="badge ${lv}">${label}</span>
      </div>
      <dl class="acct-kv">
        <dt>状态</dt><dd>${esc(st.state || (st.healthy ? 'ready' : 'cooldown'))}</dd>
        <dt>剩余冷却</dt><dd>${cool}</dd>
        ${st.last_error ? `<dt>最近错误</dt><dd title="${esc(st.last_error)}">${esc(st.last_error)}</dd>` : ''}
      </dl>
    </div>`;
  }

  /* ────────────────── 账号管理页 ────────────────── */

  async function loadAccounts() {
    try {
      // 账号是统一号池：一次拉取所有上游的账号，platform 只是账号属性。
      const [mgr, mt] = await Promise.all([
        get('/api/accounts/manage'),
        get('/api/metrics').catch(() => null),
      ]);
      state.accounts = mgr;
      state.metrics = mt || state.metrics;
      renderAccountsPage(mgr, state.metrics);
    } catch (err) { showHealth(err.message, err); }
  }

  // usageByAccount 把 /api/metrics 的 by_account 转成 map，供账号卡片附用量。
  function usageByAccount(m) {
    const out = {};
    for (const g of (m && m.by_account) || []) out[g.key] = g;
    return out;
  }

  function renderAccountsPage(d, m) {
    const host = $('accountList');
    const list = (d && d.accounts) || [];
    const usage = usageByAccount(m);

    if (!list.length) {
      host.innerHTML = emptyPanel(t('acct.next') === '下一个使用' ? '还没有可用账号' : 'No accounts yet',
        t('acct.next') === '下一个使用' ? '把凭证文件放进号池目录，或点「添加账号」选择上游类型登录。' : 'Put credential files into the pool directory, or use "Add account".');
      $('acctSub').textContent = '号池为空';
      return;
    }

    // 号池目录：多平台时逐个列出（去重、只显示非空项）。
    const dirs = [...new Set(((d.platforms || []).map((p) => p.accounts_dir).filter(Boolean)))];
    $('acctSub').textContent =
      `${d.healthy}/${d.total} 个账号可用` + (dirs.length ? ` · 号池目录 ${dirs.join('、')}` : '')
      + (m && m.by_account && m.by_account.length ? ` · 用量累计自进程启动` : '');

    const cards = list.map((a) => accountManageCard(a, usage[a.label])).join('');
    host.innerHTML = `<div class="acct-grid acct-grid-wide">${cards}</div>`;
  }

  // accountManageCard 是账号页的卡片：调度状态 + 登录状态 + 用量 + 操作。
  function accountManageCard(a, u) {
    const id = a.identity || {};
    const lv = a.healthy ? 'ok' : (a.reason === 'rate_limited' ? 'warn'
      : a.reason === 'disabled' ? 'off' : 'err');
    // 模型级冷却：账号可能整体健康，但部分模型被限流（上游按模型限流）。
    const mc = a.model_cooldowns || {};
    const mcList = Object.entries(mc);
    let stateTxt = a.healthy ? t('acct.ok')
      : a.reason === 'rate_limited' ? `${t('acct.ratelimited')} · ${fmtCountdown(a.cooldown_secs)}`
      : a.reason === 'unauthorized' ? t('acct.unauthorized')
      : a.reason === 'disabled' ? t('acct.disabled') : t('acct.ratelimited');
    if (a.healthy && mcList.length) {
      stateTxt = `${t('acct.ok')} · ${mcList.length} ${t('acct.modelslimited')}`;
    }

    // 登录状态：refreshToken 过期是终态（必须重新登录）；access 过期网关会自查。
    let loginTxt = t('login.unknown'), loginLv = '';
    if (id.refresh_expired) { loginTxt = t('login.dead'); loginLv = 'err'; }
    else if (id.needs_refresh) { loginTxt = t('login.expiring'); loginLv = 'warn'; }
    else if (id.expires_at) { loginTxt = `${t('login.ok')} ${new Date(id.expires_at).toLocaleString(LANG === 'en' ? 'en-US' : 'zh-CN')}`; }

    const name = id.nickname || id.uid || a.label;
    // 来源徽标：桌面客户端凭证 vs 号池文件。桌面凭证的「重新登录」会
    // 覆盖桌面端登录态，必须让用户在点击前就知道。
    const srcBadge = id.source === 'desktop'
      ? `<span class="tag" title="${esc(t('acct.srcdesktop'))}">${esc(t('acct.srcdesktop'))}</span>`
      : `<span class="tag" title="${esc(t('acct.srcfile'))}">${esc(t('acct.srcfile'))}</span>`;
    const usageRow = u
      ? `<dt>用量</dt><dd>${fmtNum(u.total)} 次 · ${fmtNum(u.output_tokens)} tok 出</dd>
         <dt>成功率</dt><dd>${(u.success_rate * 100).toFixed(1)}%</dd>`
      : `<dt>用量</dt><dd>—</dd>`;

    // 操作按钮：停用/启用、清除冷却、重新登录。
    const acts = [];
    if (a.reason === 'disabled') {
      acts.push(`<button class="btn btn-xs" data-acct="enable" data-label="${esc(a.label)}">启用</button>`);
    } else {
      acts.push(`<button class="btn btn-xs" data-acct="disable" data-label="${esc(a.label)}">停用</button>`);
    }
    // 账号级限流或有模型级冷却时都给「清除冷却」入口——上游提前恢复时手动解冻。
    if ((!a.healthy && a.reason === 'rate_limited') || mcList.length) {
      acts.push(`<button class="btn btn-xs" data-acct="reset" data-label="${esc(a.label)}">${esc(t('acct.reset'))}</button>`);
    }
    const reloginTip = id.source === 'desktop'
      ? t('acct.srcdesktop')
      : t('acct.srcfile');
    acts.push(`<button class="btn btn-xs" data-acct="relogin" data-label="${esc(a.label)}" data-path="${esc(id.credential_path || '')}" title="${esc(reloginTip)}">重新登录</button>`);

    return `<div class="acct acct-manage" data-level="${lv}"${a.is_next ? ' data-next' : ''}>
      <div class="acct-top">
        <span class="acct-name" title="${esc(a.label)}">${a.is_next ? '▸ ' : ''}${esc(name)}</span>
        <span class="badge ${lv}">${stateTxt}</span>
      </div>
      <dl class="acct-kv">
        <dt>来源</dt><dd>${srcBadge}</dd>
        <dt>登录</dt><dd class="${loginLv ? 'kv-' + loginLv : ''}" title="${esc(loginTxt)}">${esc(loginTxt)}</dd>
        ${usageRow}
        ${mcList.length ? `<dt>${esc(t('acct.modelslimited'))}</dt><dd title="${esc(mcList.map(([m, s2]) => `${m} (${fmtCountdown(s2)})`).join('\n'))}">${esc(mcList.map(([m, s2]) => `${m} · ${fmtCountdown(s2)}`).join('、'))}</dd>` : ''}
        ${a.last_error ? `<dt>最近错误</dt><dd title="${esc(a.last_error)}">${esc(a.last_error)}</dd>` : ''}
      </dl>
      <div class="acct-acts">${acts.join('')}</div>
    </div>`;
  }

  function emptyPanel(title, desc) {
    return `<div class="panel" style="text-align:center;padding:38px 20px">
      <div style="color:var(--text-dim);font-size:14.5px;font-weight:600">${esc(title)}</div>
      <div style="color:var(--text-faint);font-size:13px;margin-top:5px">${esc(desc)}</div>
    </div>`;
  }

  // handleAccountAction 处理账号卡片上的操作按钮。
  async function handleAccountAction(btn) {
    const act = btn.dataset.acct;
    const label = btn.dataset.label;
    if (act === 'relogin') { await startLogin(btn.dataset.path); return; }

    btn.disabled = true;
    try {
      await post(`/api/accounts/manage?action=${encodeURIComponent(act)}`, { label });
      toast(act === 'enable' ? `已启用 ${label}` : act === 'disable' ? `已停用 ${label}` : `已清除 ${label} 的冷却`, 'success');
      await loadAccounts();
      if (state.page === 'platforms') loadPlatforms();
    } catch (err) {
      toast('操作失败：' + err.message, 'error');
    } finally { btn.disabled = false; }
  }

  // startLogin 发起设备码登录（添加账号 / 重新登录）。
  // 异步 + 轮询：用户要去浏览器完成授权，同步请求必然超时。
  async function startLogin(outPath) {
    const name = outPath ? outPath.split('/').pop() : '';
    const q = name ? `?name=${encodeURIComponent(name)}` : '';
    let sess;
    try {
      sess = await post('/api/accounts/manage' + q, {});
    } catch (err) {
      toast('无法发起登录：' + err.message, 'error');
      return;
    }
    showLoginDialog(sess);
  }

  function showLoginDialog(sess) {
    const dlg = $('loginDialog');
    const url = sess.auth_url || '';
    $('loginUrl').value = url;
    $('loginUrl').readOnly = true;
    $('loginOpen').disabled = !url;
    $('loginHint').textContent = url
      ? '在浏览器中打开下面的地址完成授权（最长等待 5 分钟）'
      : '正在获取授权地址…';
    $('loginState').textContent = '';
    dlg.showModal();

    // 轮询状态：成功/失败即停。
    let stop = false;
    dlg.addEventListener('close', () => { stop = true; }, { once: true });
    (async () => {
      for (let i = 0; i < 150 && !stop; i++) {
        await new Promise((r) => setTimeout(r, 2000));
        let s;
        try { s = await get('/api/accounts/login-status?id=' + encodeURIComponent(sess.id)); } catch { continue; }
        if (!s.auth_url && !$('loginUrl').value) { $('loginUrl').value = s.auth_url || ''; $('loginOpen').disabled = !s.auth_url; }
        if (s.status === 'pending') { $('loginState').textContent = '等待授权中…'; continue; }
        if (s.status === 'success') {
          $('loginState').textContent = '登录成功' + (s.account ? '：' + s.account : '');
          // 号池监视器会自动捡起新凭证（≤10 秒），无需重启、无需手动配置。
          if (s.accounts_dir_used) {
            toast(`账号已添加${s.account ? '：' + s.account : ''}（凭证已写入 ${s.accounts_dir_used}，稍后自动入池）`, 'success', 6000);
          } else {
            toast('账号已添加' + (s.account ? '：' + s.account : ''), 'success');
          }
          setTimeout(() => dlg.close(), 1200);
          await loadAccounts();
          if (state.page === 'platforms') loadPlatforms();
          return;
        }
        $('loginState').textContent = '登录失败：' + (s.error || '未知错误');
        toast('登录失败：' + (s.error || ''), 'error');
        return;
      }
    })();
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
      : p.status === 'degraded' ? '<span class="badge warn">降级</span>'
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
    state.modelsLoading = true;
    if (state.page === 'models') renderModels();
    if (state.modelView === 'matrix') {
      try { state.matrix = await get('/api/accounts/models'); } catch (e) { state.matrix = null; }
    }
    try {
      state.models = await get('/api/models');
      state.modelsError = '';
    } catch (err) {
      state.modelsError = err.message;
      showHealth(err.message, err);
    } finally {
      state.modelsLoading = false;
    }
    renderModels();
  }

  function renderModels() {
    if (state.modelView === 'matrix') { renderModelMatrix(); return; }
    const d = state.models || {};
    const all = d.models || [];
    const q = state.modelsFilter.trim().toLowerCase();
    const rows = q ? all.filter((m) => m.id.toLowerCase().includes(q)) : all;
    $('modelsSub').textContent = `当前平台可用模型 · 共 ${all.length} 个`;
    $('modelTable').innerHTML = `
      <thead><tr>
        <th>${t('mdl.id')}</th><th>${t('mdl.name')}</th><th class="num">${t('mdl.ctx')}</th><th class="num">${t('mdl.maxout')}</th>
        <th>${t('mdl.cap')}</th>
      </tr></thead>
      <tbody>${modelsBody(rows, all, q)}</tbody>`;
  }

  // renderModelMatrix 画「模型 × 账号」矩阵：看得出哪个账号支持哪个模型。
  //
  // 多账号场景下这个信息很关键——不同账号开放的模型集可能不同，
  // 而 /v1/models 只返回并集，看不出差异。
  // renderModelMatrix 画「模型 × 账号」矩阵。
  //
  // 三种单元格状态：支持 ✓ / 不支持 · / **限流冷却中（带倒计时）**。
  // 最后一种是关键——限流是「账号 × 模型」维度的，一个格子可能「支持但
  // 暂时被限」，必须让用户看到还要等多久，否则会以为这个组合坏了。
  function renderModelMatrix() {
    const m = state.matrix;
    const host = $('modelTable');
    if (!m || !m.accounts || !m.accounts.length) {
      $('modelsSub').textContent = t('mx.title');
      host.innerHTML = `<tbody>${emptyRow(2, state.modelsLoading ? t('req.loading') : t('mx.nodata'), t('mx.nodatahint'))}</tbody>`;
      return;
    }
    const q = state.modelsFilter.trim().toLowerCase();
    const models = q ? m.models.filter((x) => x.toLowerCase().includes(q)) : m.models;
    const cool = m.cooldowns || {};        // 模型级：账号 → 模型 → 剩余秒
    const acctCool = m.account_cooldowns || {};  // 账号级：账号 → 剩余秒（整号）

    // 统计被限的格子数（仅模型级），写进副标题——一眼看出池子健康度。
    let coolingCells = 0;
    for (const a of m.accounts) {
      for (const k of Object.keys(cool[a] || {})) if (cool[a][k] > 0) coolingCells++;
    }
    // 账号级冷却的账号数（列头标注，不重复进格子）。
    const acctCooled = m.accounts.filter((a) => (acctCool[a] || 0) > 0).length;
    let sub = `${t('mx.title')} · ${m.accounts.length} ${t('mx.accounts')} / ${m.models.length} ${t('mx.models')}`;
    if (coolingCells) sub += ` · ${coolingCells} ${t('mx.cooling')}`;
    if (acctCooled) sub += ` · ${acctCooled} ${t('mx.accooled')}`;
    $('modelsSub').textContent = sub;

    const has = {};
    for (const acc of m.accounts) has[acc] = new Set(m.matrix[acc] || []);

    // 列头：账号名 + 若整号冷却则附上剩余时间（只标一次，不污染每个格子）。
    host.innerHTML = `
      <thead><tr><th>${esc(t('mdl.id'))}</th>${m.accounts.map((a) => {
        const secs = acctCool[a] || 0;
        const badge = secs > 0
          ? ` <span class="mx-acct-cool" data-cool-until="${Date.now() + secs * 1000}" title="${esc(t('mx.acccool'))}">⏳ <span class="mx-cd">${fmtCountdown(secs)}</span></span>`
          : '';
        return `<th class="mx-acct" title="${esc(a)}">${esc(a)}${badge}</th>`;
      }).join('')}</tr></thead>
      <tbody>${models.map((id) => `<tr>
        <td class="mono">${esc(id)}</td>
        ${m.accounts.map((a) => modelCell(id, has[a].has(id), (cool[a] || {})[id])).join('')}
      </tr>`).join('')}</tbody>`;
  }

  // modelCell 渲染单个格子（模型级冷却）。
  // 仅当该「账号 × 模型」被上游限流时显示倒计时；账号级冷却不进格子，
  // 由列头统一标注（否则整列重复同一倒计时，实测「全是 2h」）。
  // 冷却态优先于 ✓：支持但暂时被限的格子显示冷却而非 ✓。
  function modelCell(model, supported, coolSecs) {
    if (coolSecs > 0) {
      // data-cool-until 让倒计时能每秒自更新（见 tickMatrixCooldowns）。
      const until = Date.now() + coolSecs * 1000;
      return `<td class="mx-cell"><span class="mx-cool" data-cool-until="${until}"
        title="${esc(t('mx.ratelimited'))}">⏳ <span class="mx-cd">${fmtCountdown(coolSecs)}</span></span></td>`;
    }
    if (!supported) return '<td class="mx-cell"><span class="mx-no" title="' + esc(t('mx.unsupported')) + '">·</span></td>';
    return `<td class="mx-cell"><span class="mx-yes" title="${esc(t('mx.ok'))}">✓</span></td>`;
  }

  // tickMatrixCooldowns 每秒刷新矩阵里的倒计时。
  //
  // 不重新拉接口（那会每秒钟打一次上游模型查询）；只就地改文本，
  // 归零的格子立即恢复成 ✓。
  function tickMatrixCooldowns() {
    if (state.page !== 'models' || state.modelView !== 'matrix') return;
    const now = Date.now();
    document.querySelectorAll('#modelTable [data-cool-until]').forEach((el) => {
      const left = Math.round((Number(el.dataset.coolUntil) - now) / 1000);
      if (left <= 0) {
        // 冷却结束：就地恢复，不重新拉接口。
        //  · 格子（<td> 内）：常态是 ✓ 或 ·，按矩阵数据判定。
        //  · 列头整号冷却徽标（<th> 内，无 td.mono）：整号恢复即可见常态，
        //    直接摘除徽标，不要误渲染 ·（那是「不支持」语义）。
        if (el.closest('th')) {
          el.remove();
          return;
        }
        const m = state.matrix;
        const row = el.closest('tr');
        const model = row?.querySelector('td.mono')?.textContent;
        const acctIdx = [...el.closest('tr').querySelectorAll('td')].indexOf(el.closest('td')) - 1;
        const acct = m?.accounts?.[acctIdx];
        const supported = !!(m && acct && (m.matrix[acct] || []).includes(model));
        el.outerHTML = supported
          ? `<span class="mx-yes" title="${esc(t('mx.ok'))}">✓</span>`
          : `<span class="mx-no" title="${esc(t('mx.unsupported'))}">·</span>`;
        return;
      }
      const cd = el.querySelector('.mx-cd');
      if (cd) cd.textContent = fmtCountdown(left);
    });
  }


  function modelsBody(rows, all, q) {
    if (state.modelsLoading && !all.length) return skeletonRows(5, 7);
    if (state.modelsError && !all.length) return emptyRow(5, '模型清单加载失败', state.modelsError);
    if (!all.length) return emptyRow(5, t('mdl.nomodel'), t('btn.reload'));
    if (!rows.length) return emptyRow(5, t('mdl.nomatch')(q), '');
    return rows.map((m) => `
        <tr>
          <td class="mono">${esc(m.id)}</td>
          <td>${esc(m.display_name || '-')}</td>
          <td class="num">${m.context_tokens ? fmtNum(m.context_tokens) : '-'}</td>
          <td class="num">${m.max_output_tokens ? fmtNum(m.max_output_tokens) : '-'}</td>
          <td>
            ${m.supports_tools ? `<span class="tag on">${esc(t('mdl.tools'))}</span>` : ''}
            ${m.supports_thinking ? `<span class="tag on">${esc(t('mdl.think'))}</span>` : ''}
            ${m.supports_images ? `<span class="tag on">${esc(t('mdl.vision'))}</span>` : ''}
            ${m.is_default ? `<span class="tag">${esc(t('mdl.default'))}</span>` : ''}
          </td>
        </tr>`).join('');
  }

  // tableState 生成「加载中 / 空 / 错误」三态之一的行。
  //
  // 「加载中」与「真的没数据」必须可区分：原实现把两者写成同一句
  //「暂无记录」，而每 5s 静默轮询会让表格反复闪这句。错误态原本
  // 只落到侧栏灰字，表格区毫无反馈。
  function skeletonRows(cols, n) {
    const widths = [46, 34, 52, 40, 30, 38, 30, 30, 80];
    let out = '';
    for (let i = 0; i < n; i++) {
      out += '<tr>';
      for (let c = 0; c < cols; c++) {
        out += `<td><span class="sk" style="width:${widths[c % widths.length]}%"></span></td>`;
      }
      out += '</tr>';
    }
    return out;
  }

  function emptyRow(cols, title, desc) {
    return `<tr><td colspan="${cols}" style="text-align:center;padding:30px 14px">
      <div style="color:var(--text-dim);font-size:13.5px;font-weight:600">${esc(title)}</div>
      ${desc ? `<div style="color:var(--text-faint);font-size:12.5px;margin-top:3px">${esc(desc)}</div>` : ''}
    </td></tr>`;
  }

  /* ────────────────── 调用日志 ────────────────── */

  async function loadRequests() {
    state.reqLoading = true;
    if (state.page === 'requests') renderRequests();
    try {
      state.metrics = await get('/api/metrics');
      state.reqError = '';
    } catch (err) {
      state.reqError = err.message;
      showHealth(err.message, err);
    } finally {
      state.reqLoading = false;
    }
    renderRequests();
  }

  function renderRequests() {
    const m = state.metrics || {};
    let rows = m.recent || [];
    if (state.reqFilter === 'fail') rows = rows.filter((r) => !r.ok);
    if (state.reqFilter === 'ok') rows = rows.filter((r) => r.ok);
    $('reqTable').innerHTML = `
      <thead><tr>
        <th>${t('req.time')}</th><th>${t('req.status')}</th><th>${t('req.proto')}</th><th>${t('req.model')}</th><th>${t('req.acct')}</th><th>${t('req.mode')}</th>
        <th class="num">${t('req.dur')}</th><th class="num">${t('stat.in')}</th><th class="num">${t('stat.out')}</th><th>${t('req.err')}</th>
      </tr></thead>
      <tbody>${reqBody(rows)}</tbody>`;
  }

  function reqBody(rows) {
    if (state.reqLoading && !rows.length) return skeletonRows(10, 6);
    if (state.reqError && !rows.length) return emptyRow(10, t('req.loadfail'), state.reqError);
    if (!rows.length) {
      const filtered = state.reqFilter;
      return emptyRow(10, filtered ? t('req.nofilter') : t('req.none'),
        filtered ? t('req.tryall') : '');
    }
    return rows.map((r) => `
      <tr${r.ok ? '' : ' class="row-err"'}>
        <td class="mono">${esc(new Date(r.time).toLocaleString('zh-CN'))}</td>
        <td><span class="badge ${r.ok ? 'ok' : 'err'}">${r.ok ? '成功' : (r.status || '失败')}</span></td>
        <td>${esc(r.protocol)}</td>
        <td class="mono">${esc(r.model || '-')}</td>
        <td class="mono">${esc(r.account || '—')}</td>
        <td>${r.stream ? t('req.stream') : t('req.nostream')}</td>
        <td class="num">${fmtMs(r.duration_ms)}</td>
        <td class="num">${r.input_tokens || '-'}</td>
        <td class="num">${r.output_tokens || '-'}</td>
        <td class="err-msg" title="${esc(r.error || '')}">${esc(r.error || '')}</td>
      </tr>`).join('');
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
    } catch (err) { showHealth(err.message, err); }
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

  /* ────────────────── Toast 通知 ──────────────────
     设置页等非对话页的错误原本写进 #banner —— 那个节点只存在于对话页的
     DOM 里，用户在设置页保存失败时什么都看不到。Toast 是全局层，与当前页无关。 */

  let toastHost = null;

  function toast(msg, type = 'info', durMs) {
    if (!toastHost) {
      toastHost = document.createElement('div');
      toastHost.className = 'toast-host';
      toastHost.setAttribute('aria-live', 'polite');
      toastHost.setAttribute('aria-atomic', 'true');
      document.body.appendChild(toastHost);
    }
    // 按类型分时长：错误多看一会儿。
    const dur = durMs || (type === 'error' ? 5000 : type === 'warning' ? 4000 : 3000);
    const t = document.createElement('div');
    t.className = 'toast ' + type;
    const body = document.createElement('div');
    body.className = 'toast-msg';
    body.textContent = msg;           // textContent：不引入转义面
    const bar = document.createElement('div');
    bar.className = 'toast-bar';
    bar.style.animationDuration = dur + 'ms';
    t.append(body, bar);

    let done = false;
    const dismiss = () => {
      if (done) return;
      done = true;
      t.classList.add('out');
      setTimeout(() => t.remove(), 220);
    };
    t.addEventListener('click', dismiss);
    toastHost.appendChild(t);
    setTimeout(dismiss, dur);
  }

  function fmtCountdown(sec) {
    const s = Math.max(0, Math.round(Number(sec) || 0));
    if (s < 60) return s + ' 秒';
    if (s < 3600) return Math.floor(s / 60) + ' 分 ' + (s % 60) + ' 秒';
    return (s / 3600).toFixed(1) + ' 小时';
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
      // 对话走 /v1/chat/completions，同样要带网关密钥（与 api() 的注入逻辑一致）。
      const headers = { 'Content-Type': 'application/json' };
      const key = storedKey();
      if (key) headers['X-Api-Key'] = key;
      const res = await fetch('/v1/chat/completions', {
        method: 'POST', headers,
        body: JSON.stringify(body), signal: controller.signal,
      });
      if (!res.ok) {
        const d = await res.json().catch(() => null);
        const err = new Error((d && d.error && d.error.message) || `HTTP ${res.status}`);
        err.status = res.status;
        if (res.status === 401) { showAuthPrompt(err).catch(() => {}); }
        throw err;
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
        e.style.cssText = 'color:var(--err-text);font-size:13.5px';
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
          } catch { toast('复制失败', 'error'); }
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
    // 英文语境用 K/M/B/T；中文用万/亿。数字单位是本地化的一部分。
    if (LANG === 'en') {
      if (n >= 1e12) return (n / 1e12).toFixed(1) + 'T';
      if (n >= 1e9) return (n / 1e9).toFixed(1) + 'B';
      if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
      if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
      return String(n);
    }
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
  function showHealth(msg, err) {
    // 401 = 已启用鉴权但没带/带错密钥：给出可行动的引导，而不是干巴巴的错误文案。
    if (err && err.status === 401 && !showAuthPrompt._pending) {
      showAuthPrompt._pending = true;
      showAuthPrompt(err).finally(() => { showAuthPrompt._pending = false; });
    }
    $('healthText').textContent = msg; setDot('err');
    // 侧栏那行 12.5px 灰字太隐蔽——重要错误同时弹 Toast。
    toast(msg, 'error');
  }

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
    // GFM 表格：表头 | ... | + 分隔行 |---|---| + 数据行。
    // 必须在段落切分之前；此时文本已 esc()，单元格内的 | 不存在歧义，
    // 行内格式（code/strong）已在前面转换，所以单元格直接沿用即可。
    text = text.replace(/(^|\n)((?:\|[^\n]*\|\s*\n)\|?\s*:?-{2,}[-|:\s]*\n(?:\|[^\n]*\|\s*)+)/g,
      (_, pre, tbl) => {
        const rows = tbl.trim().split('\n').map((r) => r.trim()).filter(Boolean);
        if (rows.length < 2) return _;
        const cells = (r) => r.replace(/^\||\|$/g, '').split('|').map((c) => c.trim());
        const head = cells(rows[0]);
        // 第二行是分隔行（形如 |---|---| 或 | --- | --- |），跳过
        const bodyRows = rows.slice(1).filter((r, i) => !(i === 0 && /^:?-+:?$/.test(r.replace(/[|\s:]/g, ''))));
        const mk = (c, tag) => '<' + tag + '>' + c + '</' + tag + '>';
        const out = ['<div class="tbl-wrap"><table class="md-table">'];
        out.push('<thead><tr>' + head.map((c) => mk(c, 'th')).join('') + '</tr></thead>');
        if (bodyRows.length) {
          out.push('<tbody>' + bodyRows.map((r) => {
            const cs = cells(r);
            // 列数对齐：缺的补空单元格，多的截断
            while (cs.length < head.length) cs.push('');
            return '<tr>' + cs.slice(0, head.length).map((c) => mk(c, 'td')).join('') + '</tr>';
          }).join('') + '</tbody>');
        }
        out.push('</table></div>');
        return pre + out.join('');
      });
    text = text.split(/\n{2,}/).map((b) => {
      if (!b.trim()) return '';
      if (/^\s*<(h\d|ul|ol|blockquote|pre|div class="tbl-wrap")/.test(b)) return b;
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
    } catch { toast('复制失败', 'error'); }
  });

  /* ────────────────── 事件绑定 ────────────────── */

  $('nav').addEventListener('click', (e) => {
    const b = e.target.closest('.nav-item'); if (b) switchPage(b.dataset.page);
  });
  document.querySelectorAll('[data-goto]').forEach((b) =>
    b.addEventListener('click', () => switchPage(b.dataset.goto)));

  $('btnRefresh').addEventListener('click', () => loadOverview());
  $('btnReloadPlatforms').addEventListener('click', loadPlatforms);
  $('btnReloadAccounts').addEventListener('click', loadAccounts);
  $('btnAddAccount').addEventListener('click', () => startLogin(''));
  // 账号卡片按钮用事件委托：卡片每次重绘，逐个绑定会漏。
  $('accountList').addEventListener('click', (e) => {
    const b = e.target.closest('[data-acct]');
    if (b) handleAccountAction(b);
  });
  $('loginOpen').addEventListener('click', () => {
    const u = $('loginUrl').value.trim();
    if (u) window.open(u, '_blank', 'noopener');
  });
  $('btnReloadModels').addEventListener('click', loadModels);
  $('btnReloadReq').addEventListener('click', loadRequests);
  $('reqFilter').addEventListener('change', (e) => { state.reqFilter = e.target.value; renderRequests(); });
  $('modelFilter').addEventListener('input', (e) => { state.modelsFilter = e.target.value; renderModels(); });
  $('modelView').addEventListener('change', (e) => { state.modelView = e.target.value; loadModels(); });

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
      toast('设置已生效', 'success');
    } catch (err) {
      e.target.checked = !e.target.checked;
      toast('保存失败：' + err.message, 'error');
    }
  });
  $('btnRefreshModels').addEventListener('click', async () => {
    try {
      await post('/api/config', { refresh_models: true });
      await loadModels();
      toast('模型清单已刷新', 'success');
    } catch (err) { toast('刷新失败：' + err.message, 'error'); }
  });

  // 设置页：网关访问密钥的管理入口。
  $('apiKeyInput').value = storedKey();
  $('btnSaveKey').addEventListener('click', () => {
    saveKey($('apiKeyInput').value.trim());
    const hasKey = !!$('apiKeyInput').value.trim();
    toast(hasKey ? '密钥已保存' : '密钥已清除', 'success');
    // 立即用新密钥验证当前页面（用户可能停在任意一页，不只概览）。
    switchPage(state.page);
  });
  $('btnClearKey').addEventListener('click', () => {
    saveKey('');
    $('apiKeyInput').value = '';
    toast('密钥已清除', 'success');
  });

  // 启动时探测网关是否启用了鉴权（/api/auth-hint 刻意不鉴权）：
  // 未启用时隐藏密钥管理行——展示一个永远用不上的输入框只会让人困惑；
  // 已启用且本地无密钥时提前亮起提示，而不是等第一次 401。
  fetch('/api/auth-hint').then((r) => (r.ok ? r.json() : null)).then((hint) => {
    if (!hint) return;
    const row = $('apiKeyInput').closest('.setting');
    if (row) row.style.display = hint.auth_enabled ? '' : 'none';
    if (hint.auth_enabled && !storedKey()) {
      $('healthText').textContent = '网关已启用鉴权：请在设置页填入访问密钥';
    }
  }).catch(() => {});

  /* ────────────────── 启动 ────────────────── */

  // 偏好初始化：先恢复主题与语言，再渲染页面。
  $('themeSel').value = localStorage.getItem(THEME_KEY) || 'auto';
  $('langSel').value = LANG;
  $('themeSel').addEventListener('change', (e) => {
    localStorage.setItem(THEME_KEY, e.target.value);
    applyTheme();
  });
  $('langSel').addEventListener('change', (e) => {
    LANG = e.target.value;
    localStorage.setItem('agent2api_lang', LANG);
    applyLang();
    applyTheme(); // 主题选项的文字也要翻译
  });
  applyLang();
  applyTheme();

  loadChatModels();
  switchPage('overview');
})();
