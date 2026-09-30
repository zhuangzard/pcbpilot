// pcbpilot console — dependency-free single page (no build step, no CDN).
'use strict';

const S = {
  status: null, projects: null, runs: null, decisions: [], templates: null,
  activity: [], hello: null, online: false, page: 'monitor', route: [], wd: null, wdTab: 'timeline',
  kbResult: null, kbQuery: '',
};

// ── helpers ───────────────────────────────────────────────────────────────
const $ = (sel, el = document) => el.querySelector(sel);
function esc(s) {
  return String(s == null ? '' : s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
function pill(text, cls) { return `<span class="pill ${esc(cls || text)}">${esc(text)}</span>`; }
function ago(t) {
  if (!t) return '—';
  const d = (Date.now() - new Date(t).getTime()) / 1000;
  if (!isFinite(d)) return '—';
  if (d < 0) return '刚刚';
  if (d < 60) return Math.round(d) + ' 秒前';
  if (d < 3600) return Math.round(d / 60) + ' 分钟前';
  if (d < 86400) return Math.round(d / 3600) + ' 小时前';
  return Math.round(d / 86400) + ' 天前';
}
function dur(ms) {
  if (!ms && ms !== 0) return '—';
  const s = Math.round(ms / 1000);
  if (s < 60) return s + 's';
  const m = Math.floor(s / 60);
  if (m < 60) return m + 'm ' + (s % 60) + 's';
  const h = Math.floor(m / 60);
  if (h < 48) return h + 'h ' + (m % 60) + 'm';
  return Math.floor(h / 24) + 'd ' + (h % 24) + 'h';
}
function hhmmss(t) { const d = new Date(t); return isNaN(d) ? '' : d.toLocaleTimeString('zh-CN', { hour12: false }); }
function fmtTime(t) { const d = new Date(t); return !t || isNaN(d) || d.getFullYear() < 2000 ? '—' : d.toLocaleString('zh-CN', { hour12: false }); }

let token = null;
async function api(path, opts = {}) {
  const headers = Object.assign({}, opts.headers || {});
  if (token) headers['X-Pcbpilot-Token'] = token; else headers['X-Pcbpilot-Csrf'] = '1';
  if (opts.json !== undefined) { headers['Content-Type'] = 'application/json'; opts.body = JSON.stringify(opts.json); }
  const r = await fetch(path, { method: opts.method || 'GET', headers, body: opts.body, credentials: 'same-origin' });
  if (r.status === 401) { showAuth(); throw new Error('unauthorized'); }
  const text = await r.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch (e) { data = { raw: text }; }
  if (!r.ok) { const e = new Error((data && data.error && data.error.message) || ('HTTP ' + r.status)); e.data = data; e.status = r.status; throw e; }
  return data;
}

// ── auth: #token=… → cookie, then forget the token ─────────────────────────
async function initAuth() {
  const m = location.hash.match(/token=([0-9a-f]{32,})/);
  if (m) {
    token = m[1];
    try { await api('/api/session', { method: 'POST' }); } catch (e) { /* shown below */ }
    token = null;
    // Keep any route that came with the token (#/decisions&token=… or #token=…).
    const rest = location.hash.replace(/[#&?]?token=[0-9a-f]+/, '').replace(/^#?[&?]?/, '');
    history.replaceState(null, '', location.pathname + '#' + (rest.startsWith('/') ? rest : '/'));
  }
}
function showAuth() {
  S.authNeeded = true;
  $('#app').innerHTML = `<div class="card"><h1>需要控制台令牌</h1>
  <p>在终端运行 <code>pcbpilot console open</code>（或 <code>pcbpilot console url</code> 后粘贴链接）。
  链接 <code>#token=</code> 片段不会发送到服务器；页面会把它换成仅本机可用的 HttpOnly cookie。</p></div>`;
}

// ── theme ─────────────────────────────────────────────────────────────────
function applyTheme(t) { if (t) document.documentElement.dataset.theme = t; else delete document.documentElement.dataset.theme; }
try { applyTheme(localStorage.getItem('pcbpilot-theme')); } catch (e) {}
$('#theme').addEventListener('click', () => {
  const cur = document.documentElement.dataset.theme || (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
  const next = cur === 'dark' ? 'light' : 'dark';
  applyTheme(next);
  try { localStorage.setItem('pcbpilot-theme', next); } catch (e) {}
});

// ── live stream (SSE) with offline banner + auto reconnect ─────────────────
let es = null, lastSeq = 0;
function banner(text, ok) {
  const b = $('#banner');
  if (!text) { b.hidden = true; return; }
  b.hidden = false; b.textContent = text; b.className = 'banner' + (ok ? ' ok' : '');
}
function connect() {
  if (es) es.close();
  es = new EventSource('/api/events');
  es.addEventListener('hello', ev => {
    const h = JSON.parse(ev.data).data;
    const restarted = S.hello && (S.hello.pid !== h.pid || S.hello.startedAt !== h.startedAt);
    S.hello = h; S.online = true;
    $('#live-dot').className = 'dot live';
    banner(restarted ? `daemon 已重启（pid ${h.pid}），已重新连接` : '', true);
    if (restarted) setTimeout(() => banner(''), 4000);
    refreshAll();
  });
  es.addEventListener('activity', ev => {
    const e = JSON.parse(ev.data);
    if (e.seq <= lastSeq) return;
    lastSeq = e.seq;
    S.activity.unshift(e.data);
    if (S.activity.length > 1000) S.activity.length = 1000;
    pushStream(e.data);
    debounce('projects', 1500, refreshProjects);
  });
  es.addEventListener('run', () => debounce('runs', 800, refreshRuns));
  es.addEventListener('ask', () => debounce('asks', 200, refreshDecisions));
  es.addEventListener('project', ev => {
    const d = JSON.parse(ev.data).data || {};
    debounce('projects', 500, refreshProjects);
    if (S.wd && d.workDir === S.wd.workDir.id) debounce('wd', 500, () => loadWorkDir(S.wd.workDir.id));
  });
  es.addEventListener('status', () => debounce('status', 300, refreshStatus));
  es.onerror = () => {
    S.online = false;
    $('#live-dot').className = 'dot down';
    banner('daemon 离线，正在重连…（页面会在 daemon 恢复后自动刷新）');
    // EventSource retries by itself (retry: 2000); if the browser gave up, reopen.
    if (es.readyState === EventSource.CLOSED) setTimeout(connect, 3000);
  };
}
const timers = {};
function debounce(key, ms, fn) { clearTimeout(timers[key]); timers[key] = setTimeout(fn, ms); }

// ── data refresh ──────────────────────────────────────────────────────────
async function refreshStatus() { try { S.status = await api('/api/status'); render(); } catch (e) {} }
async function refreshProjects() { try { S.projects = await api('/api/projects'); if (['monitor', 'projects'].includes(S.page)) render(); } catch (e) {} }
async function refreshRuns() { try { S.runs = await api('/api/runs'); if (['monitor', 'agents'].includes(S.page)) render(); } catch (e) {} }
async function refreshDecisions() {
  try {
    S.decisions = (await api('/api/ask')).decisions || [];
    const n = S.decisions.filter(d => d.status === 'pending').length;
    const b = $('#ask-badge'); b.hidden = !n; b.textContent = n;
    if (['decisions', 'monitor'].includes(S.page)) render();
  } catch (e) {}
}
async function refreshActivity() { try { S.activity = (await api('/api/activity?limit=500')).activity || []; } catch (e) {} }
async function refreshAll() {
  await Promise.all([refreshStatus(), refreshProjects(), refreshRuns(), refreshDecisions(), refreshActivity()]);
  if (S.page === 'workdir' && S.route[1]) await loadWorkDir(S.route[1]);
  render();
}
setInterval(() => { if (S.online && S.page === 'monitor') renderDaemonClock(); }, 1000);
setInterval(() => { if (S.online && document.visibilityState === 'visible') refreshStatus(); }, 15000);

// ── router ────────────────────────────────────────────────────────────────
function route() {
  if (/token=[0-9a-f]{32,}/.test(location.hash)) { initAuth().then(() => { S.authNeeded = false; route(); refreshAll(); }); return; }
  const parts = (location.hash.replace(/^#\/?/, '') || '').split('/').filter(Boolean);
  S.route = parts;
  S.page = parts[0] === 'workdir' ? 'workdir' : (parts[0] || 'monitor');
  if (S.page === 'workdir') { S.wdTab = parts[2] || 'timeline'; loadWorkDir(parts[1]); }
  document.querySelectorAll('#nav a').forEach(a => a.classList.toggle('on', a.dataset.page === (S.page === 'workdir' ? 'projects' : S.page)));
  render();
}
window.addEventListener('hashchange', route);

let lastHTML = '';
function render() {
  if (S.authNeeded) { showAuth(); return; }
  const app = $('#app');
  const fn = { monitor: viewMonitor, projects: viewProjects, workdir: viewWorkDir, activity: viewActivity, agents: viewAgents, decisions: viewDecisions }[S.page];
  if (!fn) { app.innerHTML = '<div class="empty">未知页面</div>'; return; }
  const html = fn();
  // Unchanged view: keep the DOM (an open report preview must not reload,
  // typed input must not flicker).
  if (html === lastHTML && app.firstChild) return;
  lastHTML = html;
  const keep = captureInputs();
  app.innerHTML = html;
  restoreInputs(keep);
  bind();
  const d = S.status && S.status.daemon;
  $('#foot-daemon').textContent = d ? `daemon ${d.version} · pid ${d.pid} · :${d.port}` : '';
}
// Keep typed text across live re-renders.
function captureInputs() {
  const m = {};
  document.querySelectorAll('#app input[id], #app textarea[id], #app select[id]').forEach(el => { m[el.id] = el.type === 'checkbox' ? el.checked : el.value; });
  const f = document.activeElement && document.activeElement.id;
  return { m, f };
}
function restoreInputs(k) {
  for (const [id, v] of Object.entries(k.m)) {
    const el = document.getElementById(id);
    if (!el || el.dataset.live === '1') continue;
    if (el.type === 'checkbox') continue; else el.value = v;
  }
  if (k.f) { const el = document.getElementById(k.f); if (el) el.focus(); }
}

// ── monitor (landing) ─────────────────────────────────────────────────────
function viewMonitor() {
  const st = S.status;
  if (!st) return '<div class="empty">连接 daemon…</div>';
  const d = st.daemon || {};
  const wins = st.windows || [];
  const projects = (S.projects && S.projects.projects) || [];
  const running = projects.filter(p => p.status === 'running');
  const others = projects.filter(p => p.status !== 'running');
  const runs = (S.runs && S.runs.runs) || [];
  const activeRuns = runs.filter(r => r.status === 'running');
  const pending = S.decisions.filter(q => q.status === 'pending');
  const svc = d.service || {};
  const sp = d.serviceProbe || {};
  let svcText = svc.installed ? (svc.loaded ? '登录服务已安装并加载' : '登录服务已安装，未加载') : (svc.platform ? '未安装登录服务' : '—');
  // The service probe runs in the background (launchctl/systemctl can block
  // right after the service is reloaded); say how fresh the shown value is.
  if (sp.state === 'checking') svcText = '检查中…';
  else if (sp.state === 'stale') svcText += '（缓存 ' + ago(sp.at) + '，刷新中）';
  else if (sp.state === 'timeout') svcText = svc.platform ? svcText + '（刷新超时，显示缓存）' : '检查超时，稍后重试';
  else if (sp.state === 'error') svcText = svc.platform ? svcText + '（刷新失败）' : '检查失败';
  const hp = st.healthProbe || {};
  return `
  <div class="stats">
    ${stat(S.online ? '在线' : '离线', 'daemon', S.online ? 'ok' : 'bad')}
    ${stat(wins.length, 'EasyEDA 窗口')}
    ${stat(running.length, '运行中项目')}
    ${stat(projects.length, '项目总数（含历史）')}
    ${stat(activeRuns.length + ((S.runs && S.runs.activeSessions) || 0), '活动运行 / 会话')}
    ${stat(pending.length, '待决策', pending.length ? 'warn' : '')}
  </div>
  <div class="grid gmain">
    <div class="grid">
      <div class="grid g2">
        <div class="card"><h2>Daemon <span class="muted">后台服务</span></h2>
          <dl class="kv">
            <dt>状态</dt><dd>${S.online ? pill('online', 'ok') : pill('offline', 'bad')}</dd>
            <dt>版本</dt><dd>${esc(d.version)}</dd>
            <dt>PID</dt><dd>${esc(d.pid)}</dd>
            <dt>端口</dt><dd>${esc(d.host || '127.0.0.1')}:${esc(d.port)}</dd>
            <dt>运行时长</dt><dd id="uptime">${dur((d.uptimeSec || 0) * 1000)}</dd>
            <dt>启动于</dt><dd>${fmtTime(d.startedAt)}</dd>
            <dt>自动保存</dt><dd>${d.autosave ? pill('on ' + esc(d.autosaveDebounce), 'ok') : pill('off', 'warn')}</dd>
            <dt>服务</dt><dd>${esc(svcText)}${svc.binary ? `<br><span class="muted">${esc(svc.binary)}</span>` : ''}</dd>
            <dt>审计回填</dt><dd>${st.console && st.console.backfillDone ? '完成 ' + ago(st.console.backfillAt) : '进行中…'}</dd>
          </dl>
          ${updatesBlock(st)}
        </div>
        <div class="card"><h2>组件版本 <span class="muted">对齐检查</span></h2>
          <table><thead><tr><th>组件</th><th>版本</th><th>对齐</th></tr></thead><tbody>
          ${(st.components || []).map(c => `<tr><td>${esc(c.name)}${c.where ? `<div class="muted small">${esc(c.where)}</div>` : ''}</td>
            <td class="mono">${esc(c.version || '—')}</td><td>${pill(c.align)}${c.detail ? `<div class="muted small">${esc(c.detail)}</div>` : ''}</td></tr>`).join('') || '<tr><td colspan=3 class="muted">health 不可用</td></tr>'}
          </tbody></table>
          ${st.healthError ? `<p class="err small">health: ${esc(st.healthError)}</p>` : ''}
          ${!st.healthError && ['stale', 'timeout'].includes(hp.state) ? `<p class="muted small">health 刷新中，显示 ${esc(ago(hp.at))} 的数据</p>` : ''}
        </div>
      </div>
      <div class="card"><h2>EasyEDA 窗口 <span class="muted">connector 实时连接</span></h2>
        ${wins.length ? `<div class="scroll"><table><thead><tr><th>窗口</th><th>宿主</th><th>connector</th><th>工程</th><th>文档</th><th>连接于</th><th>最后心跳</th></tr></thead><tbody>
        ${wins.map(w => { const c = w.context || {}; return `<tr><td class="mono">${esc((w.windowId || '').slice(0, 8))}</td>
          <td>${esc(hostForm(w.easyedaVersion))}</td><td class="mono">${esc(w.connectorVersion)} ${w.connectorVersionOk === false ? pill('stale') : ''}</td>
          <td>${esc(c.projectName || '—')}<div class="muted small mono">${esc(c.projectUuid || '')}</div></td>
          <td>${esc(c.documentType || '')} <span class="muted small mono">${esc(c.documentUuid || '')}</span></td>
          <td class="nowrap">${ago(w.connectedAt)}</td><td class="nowrap">${ago(w.lastSeen)}</td></tr>`; }).join('')}
        </tbody></table></div>` : '<div class="empty">没有已连接窗口。打开 EasyEDA Pro，在设置里开启“允许外部交互”。</div>'}
      </div>
      <div class="card"><h2>运行中的项目 <span class="muted">5 分钟内有动作</span></h2>${projectTable(running, true)}</div>
      <div class="card"><h2>全部项目 <span class="muted">空闲与已结束，历史来自审计日志（重启后保留）</span></h2>${projectTable(others, false)}</div>
      <div class="card"><h2>本地工作目录 <span class="muted">离线设计链（intent / sim / pcb auto / report）的最终状态</span></h2>${workDirMini()}</div>
    </div>
    <div class="grid">
      ${pending.length ? `<div class="card"><h2>待决策 ${pill(pending.length, 'warn')}</h2>${pending.slice(0, 3).map(decisionCard).join('')}<a href="#/decisions">全部决策 →</a></div>` : ''}
      <div class="card"><h2>活动运行 <span class="muted">CLI 离线命令 / Agent 会话</span></h2>${runsMini(activeRuns)}</div>
      <div class="card"><h2>实时动作流 <span class="muted">daemon typed actions</span></h2>
        <div class="stream" id="stream">${S.activity.slice(0, 150).map(streamRow).join('') || '<div class="empty">暂无动作</div>'}</div></div>
    </div>
  </div>`;
}
function stat(n, l, cls) { return `<div class="stat"><div class="n ${cls ? 'pill ' + cls : ''}">${esc(n)}</div><div class="l">${esc(l)}</div></div>`; }
function hostForm(v) { if (!v) return '—'; const line = /^3\./.test(v) ? 'V3' : (/^4\./.test(v) ? 'V4' : ''); return (line ? line + ' ' : '') + v; }
function updatesBlock(st) {
  const u = st.updates || st.update;
  if (!u) return '<p class="muted small">更新状态：daemon 未报告（旧版本或尚未检查）。</p>';
  return `<details class="small"><summary>更新 / 离线重试状态</summary><pre class="mono">${esc(JSON.stringify(u, null, 2))}</pre></details>`;
}
function renderDaemonClock() {
  const el = $('#uptime'); const d = S.status && S.status.daemon;
  if (el && d && d.startedAt) el.textContent = dur(Date.now() - new Date(d.startedAt).getTime());
}
function projectTable(list, live) {
  if (!list.length) return `<div class="empty">${live ? '当前没有正在运行的项目。' : '还没有历史项目。'}</div>`;
  return `<div class="scroll"><table><thead><tr><th>项目</th><th>状态</th><th>阶段</th><th>最终状态</th><th>动作</th><th>时长</th><th>最后活动</th><th>最后错误</th></tr></thead><tbody>
  ${list.map(p => {
    const drc = p.lastDrc ? (p.lastDrc.passed ? pill('DRC ✓', 'ok') : pill('DRC ✗ ' + p.lastDrc.violations, 'bad')) : '';
    const rep = p.report ? pill('报告 ' + p.report.version + ' ' + p.report.verdict, p.report.verdict.startsWith('PASS') ? (p.report.verdict === 'PASS' ? 'ok' : 'warn') : 'bad') : '';
    const wds = (p.workDirs || []).map(w => `<a href="#/workdir/${esc(w.id)}">${esc(w.dir.split('/').pop())}</a>`).join(' ');
    return `<tr><td><b>${esc(p.name || (p.key === 'unattributed' ? '未归属（旧日志无工程上下文）' : p.key))}</b>
      <div class="muted small mono">${esc(p.uuid || '')}</div>${wds ? `<div class="small">目录 ${wds}</div>` : ''}</td>
      <td>${pill(statusText(p.status), p.status)}${p.connectedWindows ? `<div class="small muted">${p.connectedWindows} 窗口${p.host ? ' · ' + esc(hostForm(p.host)) : ''}</div>` : ''}
        ${p.activeClients ? `<div class="small muted">${p.activeClients} 个活动客户端</div>` : ''}${p.runningRuns ? `<div class="small">${p.runningRuns} 个运行中命令</div>` : ''}</td>
      <td class="small">${esc(p.stage || '—')}</td>
      <td>${drc} ${rep}${p.lastSave && !p.lastSave.startsWith('0001') ? `<div class="small muted">保存 ${ago(p.lastSave)}</div>` : ''}</td>
      <td class="num">${p.actions}${p.failures ? `<div class="err small">${p.failures} 失败</div>` : ''}<div class="muted small">${p.writes} 写</div></td>
      <td class="num">${dur(p.activeMs)}</td>
      <td class="nowrap">${ago(p.lastSeen)}<div class="muted small mono">${esc(p.lastAction || '')}</div></td>
      <td class="small">${p.lastError ? `<span class="err">${esc(p.lastError.action)}</span> ${esc(p.lastError.code || '')}<div class="muted">${esc((p.lastError.msg || '').slice(0, 120))}</div><div class="muted">${ago(p.lastError.at)}</div>` : '—'}</td></tr>`;
  }).join('')}</tbody></table></div>`;
}
function workDirMini() {
  const wds = (S.projects && S.projects.workDirs) || [];
  if (!wds.length) return '<div class="empty">没有登记的工作目录（<code>pcbpilot project-config init</code> 会自动登记）。</div>';
  return `<div class="scroll"><table><thead><tr><th>工作目录</th><th>状态</th><th>最新报告</th><th>最新工件</th><th>模板</th></tr></thead><tbody>
  ${wds.map(x => { const w = x.workDir, r = x.report, la = x.lastArtifact, c = x.config || {}; return `<tr>
    <td><a href="#/workdir/${esc(w.id)}"><b>${esc(w.name || w.id)}</b></a><div class="muted small mono">${esc(w.dir.split('/').slice(-2).join('/'))}</div></td>
    <td>${wdPill(x.status)}<div class="muted small">${esc(x.statusReason || '')}</div></td>
    <td>${r ? pill(r.version + ' ' + r.verdict, verdictCls(r.verdict)) + `<div class="muted small">${fmtTime(r.generatedAt)}</div>` : '—'}</td>
    <td class="small">${la ? esc(la.kind) + ' · ' + ago(la.modTime) : '—'}</td><td>${c.template ? pill(c.template, 'info') : '—'}</td></tr>`; }).join('')}
  </tbody></table></div>`;
}
function verdictCls(v) { return v === 'PASS' ? 'ok' : (String(v).startsWith('PASS') ? 'warn' : 'bad'); }
function statusText(s) { return { running: '运行中', idle: '空闲', finished: '已结束' }[s] || s; }
function streamRow(a) {
  const proj = a.projectName ? ` · ${esc(a.projectName)}` : '';
  const err = a.ok ? '' : ` <span class="err">${esc(a.errorCode || '')} ${esc((a.errorMsg || '').slice(0, 80))}</span>`;
  return `<div class="row"><span class="t">${hhmmss(a.ts)}</span><span class="s ${a.ok ? 'ok' : 'bad'}">${a.ok ? '✓' : '✗'}</span>
    <span class="a" title="${esc(a.action)} ${esc(a.clientId || '')}">${esc(a.action)} <span class="muted">${a.durationMs || 0}ms${proj}</span>${err}</span></div>`;
}
function pushStream(a) {
  const el = $('#stream'); if (!el) return;
  const tmp = document.createElement('div'); tmp.innerHTML = streamRow(a);
  const row = tmp.firstElementChild; row.classList.add('fresh');
  const emptyEl = el.querySelector('.empty'); if (emptyEl) emptyEl.remove();
  el.prepend(row);
  while (el.children.length > 200) el.lastElementChild.remove();
}
function runsMini(list) {
  const sessions = ((S.runs && S.runs.sessions) || []).filter(s => s.active);
  if (!list.length && !sessions.length) return '<div class="empty">没有正在运行的命令或会话。</div>';
  return `${list.map(r => `<div class="small">${pill(r.agent, 'info')} <b>${esc(r.title || r.id)}</b> <span class="muted">${ago(r.startedAt)}</span><div class="muted mono">${esc(r.cwd || '')}</div></div>`).join('')}
    ${sessions.map(s => `<div class="small">${pill('会话', 'ok')} ${esc(s.host)}${s.label ? ':' + esc(s.label) : ''} · ${s.commands} 命令 / ${s.actions} 动作 ${s.project ? '· ' + esc(s.project) : ''}<div class="muted mono">${esc(s.lastAction)}</div></div>`).join('')}`;
}

// ── projects & work dirs ──────────────────────────────────────────────────
function viewProjects() {
  const wds = (S.projects && S.projects.workDirs) || [];
  return `<h1>项目工作目录</h1>
  <div class="note">工作目录 = Agent 运行 CLI 的本地文件夹（intent/sim/post/报告、<code>pcbpilot.project.json</code>、<code>resources/</code>）。
  <code>pcbpilot project-config init</code> 会自动登记；也可以在这里填绝对路径登记。EDA 工程历史见“监控”页。</div>
  <div class="card"><div class="row-flex"><input type="text" id="wd-path" placeholder="/绝对路径/到/工作目录" size="60">
  <input type="text" id="wd-name" placeholder="显示名（可选）"><button class="primary" id="wd-add">登记</button><span id="wd-msg" class="small"></span></div></div>
  <div class="card"><div class="scroll"><table><thead><tr><th>名称</th><th>状态</th><th>目录</th><th>流程模板</th><th>EDA 工程</th><th>最新工件</th><th>最新报告</th><th></th></tr></thead><tbody>
  ${wds.map(x => { const w = x.workDir, c = x.config || {}, r = x.report, la = x.lastArtifact; return `<tr>
    <td><a href="#/workdir/${esc(w.id)}"><b>${esc(w.name || w.id)}</b></a></td>
    <td>${wdPill(x.status)}<div class="muted small">${esc(x.statusReason || '')}</div></td><td class="mono small">${esc(w.dir)}</td>
    <td>${c.template ? pill(c.template, 'info') : '<span class="muted">未配置</span>'}</td><td>${esc(c.edaProject || '—')}</td>
    <td class="small">${la ? esc(la.kind) + ' · ' + ago(la.modTime) : '—'}</td>
    <td>${r ? pill(r.version + ' ' + r.verdict, r.verdict === 'PASS' ? 'ok' : (r.verdict.startsWith('PASS') ? 'warn' : 'bad')) : '—'}</td>
    <td><button class="ghost small" data-rm="${esc(w.id)}" title="仅取消登记，不删除文件">移除</button></td></tr>`; }).join('') || '<tr><td colspan=8 class="empty">还没有登记的工作目录。</td></tr>'}
  </tbody></table></div></div>`;
}
function wdPill(st) {
  const m = { running: ['运行中', 'running'], failed: ['失败', 'bad'], finished: ['已完成', 'ok'], 'in-progress': ['进行中', 'warn'], empty: ['空', 'idle'] };
  const [t, c] = m[st] || [st || '—', 'idle'];
  return pill(t, c);
}

async function loadWorkDir(id) {
  if (!id) return;
  try {
    const wd = await api('/api/workdirs/' + encodeURIComponent(id));
    S.wd = wd;
    if (!S.templates) S.templates = await api('/api/templates');
    if (S.page === 'workdir') render();
  } catch (e) { S.wd = { error: e.message }; render(); }
}

function viewWorkDir() {
  const wd = S.wd;
  if (!wd) return '<div class="empty">加载…</div>';
  if (wd.error) return `<div class="card err">${esc(wd.error)}</div>`;
  const w = wd.workDir;
  const tabs = [['timeline', '时间线'], ['sims', '仿真轮次'], ['reports', '报告'], ['library', '资料库'], ['process', '流程配置']];
  const body = { timeline: tabTimeline, sims: tabSims, reports: tabReports, library: tabLibrary, process: tabProcess }[S.wdTab] || tabTimeline;
  return `<h1>${esc(w.name || w.id)} ${wdPill(wd.status)} <span class="muted small mono">${esc(w.dir)}</span></h1>
  ${wd.statusReason ? `<p class="muted small">${esc(wd.statusReason)}</p>` : ''}
  <div class="tabs">${tabs.map(([k, l]) => `<a href="#/workdir/${esc(w.id)}/${k}" class="${S.wdTab === k ? 'on' : ''}">${l}</a>`).join('')}</div>${body(wd)}`;
}

function tabTimeline(wd) {
  const tl = wd.timeline || [];
  const grp = (dom, title) => `<h2>${title}</h2><div class="timeline">${tl.filter(s => s.domain === dom).map(s => `
    <div class="step ${esc(s.status)}"><div class="id">${esc(s.id)} ${pill(stepText(s.status), s.status)}</div><div class="ti">${esc(s.title)}</div>
    ${s.at && !s.at.startsWith('0001') ? `<div class="ev">${fmtTime(s.at)}</div>` : ''}
    ${s.reason ? `<div class="ev">${esc(s.reason)}</div>` : ''}
    ${(s.evidence || []).slice(0, 3).map(e => `<div class="ev mono">${esc(e)}</div>`).join('')}</div>`).join('')}</div>`;
  return `<div class="note">状态来自工件（intent/sim/post/feedback/报告/DRC/板级 dump 的生成器与时间戳）与旧 workflow 记录，只是证据视图，不是门禁：
  “有证据”列出识别到的工件；没有工件的步骤显示“无证据”，不等于没做；“隐含完成”表示自身没有工件、但后续步骤已有证据。${wd.config ? '' : '此目录没有 <code>pcbpilot.project.json</code>，跳过项无法显示。'}</div>
  <div class="grid">${grp('schematic', '原理图 S0–S6.5')}${grp('pcb', 'PCB P0–P10.5')}${grp('report', '报告 P11')}</div>
  <div class="card"><h2>工件 <span class="muted">${(wd.artifacts || []).length} 个</span></h2>${artifactTable(wd)}</div>`;
}
function stepText(s) { return { done: '有证据', implied: '隐含完成', pending: '无证据', skipped: '跳过' }[s] || s; }
function artifactTable(wd) {
  const arts = (wd.artifacts || []).slice().reverse();
  if (!arts.length) return '<div class="empty">没有识别到工件。</div>';
  return `<div class="scroll"><table><thead><tr><th>类型</th><th>步骤</th><th>路径</th><th>时间</th><th>大小</th></tr></thead><tbody>
  ${arts.map(a => `<tr><td>${pill(a.kind, 'info')}</td><td>${esc(a.step)}</td><td class="mono small"><a href="${fileURL(wd, a.path)}" target="_blank" rel="noopener">${esc(a.path)}</a>${(a.aliases || []).length ? `<div class="muted">同内容：${a.aliases.map(esc).join(', ')}</div>` : ''}</td>
    <td class="nowrap">${fmtTime(a.modTime)}</td><td class="num">${(a.bytes / 1024).toFixed(1)} KB</td></tr>`).join('')}</tbody></table></div>`;
}
function fileURL(wd, p, dl) { return `/api/workdirs/${encodeURIComponent(wd.workDir.id)}/file?path=${encodeURIComponent(p)}${dl ? '&download=1' : ''}`; }

function tabSims(wd) {
  const sims = wd.sims || [];
  if (!sims.length) return '<div class="empty">没有仿真输出（sim power / analog / post-layout 的 JSON）。</div>';
  const kinds = { 'sim-power': '电源仿真 sim power', 'sim-analog': '模拟 SPICE sim analog', 'sim-post': '设计后仿真 sim post-layout' };
  return Object.keys(kinds).map(k => {
    const runs = sims.filter(s => s.kind === k).reverse();
    if (!runs.length) return '';
    const keys = [...new Set(runs.flatMap(r => Object.keys(r.metrics || {})))];
    return `<div class="card"><h2>${kinds[k]} <span class="muted">${runs.length} 轮，新→旧；Δ 为与上一轮之差</span></h2><div class="scroll"><table>
      <thead><tr><th>指标</th>${runs.map(r => `<th><a href="${fileURL(wd, r.path)}" target="_blank" rel="noopener" class="mono">${esc(r.path)}</a><div class="muted">${fmtTime(r.modTime)}</div>${r.status ? pill(r.status) : ''}</th>`).join('')}</tr></thead>
      <tbody>${keys.map(key => `<tr><td>${esc(key)}</td>${runs.map(r => { const v = (r.metrics || {})[key]; const d = (r.delta || {})[key];
        return `<td class="num">${v === undefined ? '—' : esc(v)}${d ? ` <span class="delta ${d > 0 ? 'up' : 'down'}">(${d > 0 ? '+' : ''}${esc(d)})</span>` : ''}</td>`; }).join('')}</tr>`).join('')}
      ${runs.some(r => r.labels && Object.keys(r.labels).length) ? `<tr><td class="muted">标签</td>${runs.map(r => `<td class="small">${Object.entries(r.labels || {}).map(([k, v]) => esc(k) + '=' + esc(v)).join('<br>')}</td>`).join('')}</tr>` : ''}
      </tbody></table></div></div>`;
  }).join('');
}

function tabReports(wd) {
  const reps = wd.reports || [];
  if (!reps.length) return '<div class="empty">没有报告包（pcbpilot report design 生成的 vN/manifest.json）。</div>';
  const sel = S.route[3] ? decodeURIComponent(S.route[3]) : null;
  const cur = reps.find(r => r.dir === sel) || null;
  return `<div class="card"><table><thead><tr><th>版本</th><th>结论</th><th>生成时间</th><th>文件</th><th>打开</th></tr></thead><tbody>
  ${reps.map(r => `<tr><td><b>${esc(r.version)}</b> <span class="muted mono small">${esc(r.dir)}</span></td>
    <td>${pill(r.verdict, r.verdict === 'PASS' ? 'ok' : (r.verdict.startsWith('PASS') ? 'warn' : 'bad'))}</td><td>${fmtTime(r.generatedAt)}</td><td class="num">${r.files}</td>
    <td class="row-flex">${r.html ? `<a href="#/workdir/${esc(wd.workDir.id)}/reports/${encodeURIComponent(r.dir)}">预览</a> <a href="${fileURL(wd, r.html)}" target="_blank" rel="noopener">新标签</a>` : ''}
    ${r.markdown ? `<a href="${fileURL(wd, r.markdown)}" target="_blank" rel="noopener">md</a>` : ''} ${r.zip ? `<a href="${fileURL(wd, r.zip, true)}">zip</a>` : ''}</td></tr>`).join('')}
  </tbody></table></div>
  ${cur && cur.html ? `<div class="card"><h2>${esc(cur.version)} 预览 <span class="muted">服务端 CSP sandbox（不透明源、无脚本），只读</span></h2><iframe class="report" src="${fileURL(wd, cur.html)}"></iframe></div>` : ''}`;
}

function tabLibrary(wd) {
  const kinds = (S.templates && S.templates.kinds) || ['other'];
  const k = wd.kb || { docs: 0 };
  const r = S.kbResult;
  return `<div class="grid g2">
  <div class="card"><h2>上传资料 <span class="muted">拖放或选择；只写入本目录 resources/&lt;类型&gt;/</span></h2>
    <div class="row-flex"><label>类型 <select id="kb-kind">${kinds.map(x => `<option>${esc(x)}</option>`).join('')}</select></label>
    <input type="text" id="kb-tags" placeholder="标签，逗号分隔"></div>
    <div class="drop" id="drop">把 PDF / md / txt / html / docx / 图片 / STEP 拖到这里，或 <label class="btn">选择文件<input type="file" id="kb-file" multiple hidden></label>
    <div class="small">单文件 ≤ 200 MB；按 sha256 去重；文件名自动清洗</div></div>
    <div id="kb-up" class="small"></div></div>
  <div class="card"><h2>检索 <span class="muted">BM25，按页引用 kb:&lt;id&gt;#p&lt;页&gt;</span></h2>
    <div class="row-flex"><input type="search" id="kb-q" placeholder="例如 LDO dropout thermal / 爬电距离" size="40"><button class="primary" id="kb-go">搜索</button></div>
    ${r ? `<p class="muted small">${r.docsSearched} 文档 / ${r.chunksSearched} 片段，${r.tookMs} ms</p>
      ${(r.byDoc || []).slice(0, 8).map(d => `<div class="small">${pill(d.score, 'info')} <b>${esc(d.title)}</b> · ${d.hits} 命中 · 页 ${esc((d.pages || []).join(','))}</div>`).join('')}
      <hr>${(r.hits || []).map(h => `<div class="hit"><span class="cite">${esc(h.cite)}</span> <b>${esc(h.title)}</b> <span class="muted small">${esc(h.score)}</span><div class="small">${esc(h.snippet)}</div></div>`).join('') || '<div class="empty">无结果</div>'}` : ''}
  </div></div>
  <div class="card"><h2>文档 <span class="muted">${k.docs} 个 · 已摘要 ${k.summarized || 0}</span></h2><div id="kb-list">${kbList()}</div></div>`;
}
let kbDocs = null;
function kbList() {
  if (!kbDocs) return '<div class="empty">加载…</div>';
  if (!kbDocs.length) return '<div class="empty">资料库为空。</div>';
  return `<div class="scroll"><table><thead><tr><th>ID</th><th>标题 / 文件</th><th>类型</th><th>状态</th><th>页</th><th>摘要</th><th>标签</th></tr></thead><tbody>
  ${kbDocs.map(d => `<tr><td class="mono small">${esc(d.id)}</td><td><a href="${fileURL(S.wd, d.path)}" target="_blank" rel="noopener">${esc(d.title || d.name)}</a><div class="muted small mono">${esc(d.path)}</div>${d.note ? `<div class="small muted">${esc(d.note)}</div>` : ''}</td>
    <td>${esc(d.kind)}</td><td>${pill(d.status, d.status === 'indexed' ? 'ok' : (d.status === 'error' ? 'bad' : 'warn'))}</td><td class="num">${d.pages || ''}</td>
    <td class="small">${d.summary ? esc(d.summary.text.slice(0, 160)) : '<span class="muted">待 Agent 填写</span>'}</td>
    <td class="small">${(d.tags || []).map(t => `<span class="tag">${esc(t)}</span>`).join(' ')} <button class="ghost small" data-tag="${esc(d.id)}">+标签</button></td></tr>`).join('')}</tbody></table></div>`;
}
async function loadKB() {
  if (!S.wd || !S.wd.workDir) return;
  try { kbDocs = (await api(`/api/workdirs/${S.wd.workDir.id}/kb`)).docs || []; } catch (e) { kbDocs = []; }
  const el = $('#kb-list'); if (el) el.innerHTML = kbList(); bindKBTags();
}
async function uploadFiles(files) {
  const fd = new FormData();
  fd.append('kind', $('#kb-kind').value);
  fd.append('tags', $('#kb-tags').value);
  for (const f of files) fd.append('file', f, f.name);
  const out = $('#kb-up'); out.textContent = `上传 ${files.length} 个文件并建索引…`;
  try {
    const r = await api(`/api/workdirs/${S.wd.workDir.id}/kb/upload`, { method: 'POST', body: fd });
    out.innerHTML = (r.added || []).map(a => `${a.duplicate ? '重复（已记别名）' : '已添加'}：${esc(a.source)} → ${esc(a.doc ? a.doc.status : a.error)}`).join('<br>') +
      (r.rejected || []).map(x => `<div class="err">拒绝：${esc(x.name)}（${esc(x.reason)}）</div>`).join('');
    loadKB(); loadWorkDir(S.wd.workDir.id);
  } catch (e) { out.innerHTML = `<span class="err">${esc(e.message)}</span>`; }
}

function tabProcess(wd) {
  const t = S.templates;
  if (!t) return '<div class="empty">加载…</div>';
  const cfg = wd.config;
  if (!cfg) return `<div class="card"><h2>还没有 pcbpilot.project.json</h2><p>选择一个流程模板创建。Agent 在规划前先读此文件，跳过的步骤会写进报告 §11.5。</p>
    <div class="row-flex"><select id="tpl">${t.templates.map(x => `<option value="${esc(x.id)}">${esc(x.title)} — ${esc(x.id)}</option>`).join('')}</select>
    <input type="text" id="tpl-eda" placeholder="EasyEDA 工程名或 uuid（可选）"><button class="primary" id="tpl-init">创建</button></div>
    <ul class="small">${t.templates.map(x => `<li><b>${esc(x.id)}</b>：${esc(x.description)}</li>`).join('')}</ul></div>`;
  const issues = wd.issues || [];
  const tog = (group, id, title, tg, locked) => `<div class="toggle"><input type="checkbox" id="cfg-${group}-${esc(id)}" data-live="1" data-group="${group}" data-id="${esc(id)}" ${tg && tg.enabled ? 'checked' : ''} ${locked ? 'disabled' : ''}>
    <label for="cfg-${group}-${esc(id)}"><b class="mono">${esc(id)}</b> ${esc(title)}</label>
    <input type="text" class="why" id="why-${group}-${esc(id)}" data-live="1" placeholder="跳过理由" value="${esc((tg && tg.reason) || '')}" ${tg && tg.enabled ? 'hidden' : ''}></div>`;
  const k = cfg.constraints || {};
  return `<div class="note">这些开关写入 <code>${esc(wd.workDir.dir)}/pcbpilot.project.json</code>（schemaVersion ${cfg.schemaVersion}，模板 ${esc(cfg.template || '—')}）。
    它只决定流程做什么、报告写什么；不授权任何 EDA 写入，也不放宽检查：S4 后必须 S5、布线后必须 P10、P7 前必须 P6，必需章节 0/1/7/11 不能关。</div>
  ${issues.length ? `<div class="card"><h2>校验</h2><ul class="issues">${issues.map(i => `<li class="${esc(i.severity)}">${esc(i.severity)} · ${esc(i.field)} · ${esc(i.message)}</li>`).join('')}</ul></div>` : ''}
  <div class="grid g3">
    <div class="card"><h2>步骤</h2>${t.steps.map(s => tog('steps', s.id, s.title, cfg.steps[s.id])).join('')}</div>
    <div class="card"><h2>仿真</h2>${t.sims.map(s => tog('sims', s.id, s.title, cfg.sims[s.id])).join('')}
      <h2 class="mt">报告章节</h2>${t.sections.map(s => tog('reportSections', s.id, s.title, cfg.reportSections[s.id], s.required)).join('')}</div>
    <div class="card"><h2>约束</h2>
      <label class="small">EDA 工程<br><input type="text" id="c-eda" data-live="1" value="${esc((cfg.eda || {}).project || '')}" size="30"></label><br>
      <label class="small">标准（逗号分隔）<br><input type="text" id="c-std" data-live="1" value="${esc((k.standards || []).join(', '))}" size="30"></label><br>
      <label class="small">需求文件（相对路径，逗号分隔）<br><input type="text" id="c-req" data-live="1" value="${esc((k.requirements || []).join(', '))}" size="30"></label><br>
      <label class="small">机械文件<br><input type="text" id="c-mech" data-live="1" value="${esc(k.mech || '')}" size="30"></label><br>
      <label class="small">工厂 / 层数<br><input type="text" id="c-fab" data-live="1" value="${esc((k.fab || {}).profile || '')}" size="14"> <input type="text" id="c-layers" data-live="1" value="${esc((k.fab || {}).layers || '')}" size="4"></label><br>
      <label class="small">优选器件<br><input type="text" id="c-pref" data-live="1" value="${esc(((k.parts || {}).preferred || []).join(', '))}" size="30"></label><br>
      <label class="small">禁用器件<br><input type="text" id="c-ban" data-live="1" value="${esc(((k.parts || {}).banned || []).join(', '))}" size="30"></label><br>
      <label class="small">备注<br><textarea id="c-notes" data-live="1" rows="3" cols="30">${esc(k.notes || '')}</textarea></label>
    </div>
  </div>
  <div class="row-flex"><button class="primary" id="cfg-save">校验并保存</button><span id="cfg-msg" class="small"></span>
    <span class="muted small">最后修改 ${fmtTime(cfg.updatedAt)} · ${esc(cfg.updatedBy || '')}</span></div>`;
}
async function saveConfig() {
  const cfg = JSON.parse(JSON.stringify(S.wd.config));
  document.querySelectorAll('input[data-group]').forEach(el => {
    const g = el.dataset.group, id = el.dataset.id;
    const why = document.getElementById(`why-${g}-${id}`);
    cfg[g][id] = el.checked ? { enabled: true } : { enabled: false, reason: (why && why.value.trim()) || '控制台关闭' };
  });
  const list = id => $('#' + id).value.split(',').map(s => s.trim()).filter(Boolean);
  cfg.eda = { project: $('#c-eda').value.trim() };
  cfg.constraints = Object.assign({}, cfg.constraints, {
    standards: list('c-std'), requirements: list('c-req'), mech: $('#c-mech').value.trim(),
    fab: { profile: $('#c-fab').value.trim(), layers: parseInt($('#c-layers').value, 10) || 0 },
    parts: { preferred: list('c-pref'), banned: list('c-ban') }, notes: $('#c-notes').value,
  });
  const msg = $('#cfg-msg');
  try {
    await api(`/api/workdirs/${S.wd.workDir.id}/config`, { method: 'PUT', json: cfg });
    msg.innerHTML = pill('已保存', 'ok');
    loadWorkDir(S.wd.workDir.id);
  } catch (e) {
    const iss = e.data && e.data.issues;
    msg.innerHTML = iss ? iss.filter(i => i.severity === 'error').map(i => `<div class="err">${esc(i.field)}: ${esc(i.message)}</div>`).join('') : `<span class="err">${esc(e.message)}</span>`;
  }
}

// ── activity / agents / decisions ─────────────────────────────────────────
function viewActivity() {
  const f = ($('#act-filter') && $('#act-filter').value.toLowerCase()) || '';
  const rows = S.activity.filter(a => !f || JSON.stringify(a).toLowerCase().includes(f)).slice(0, 500);
  return `<h1>活动流</h1><div class="row-flex"><input type="search" id="act-filter" placeholder="过滤：动作 / 工程 / 错误码 / 客户端"> <span class="muted small">最近 ${S.activity.length} 条（实时）</span></div>
  <div class="card"><div class="scroll"><table><thead><tr><th>时间</th><th></th><th>动作</th><th>耗时</th><th>工程 / 文档</th><th>客户端</th><th>错误</th></tr></thead><tbody>
  ${rows.map(a => `<tr><td class="nowrap mono small">${fmtTime(a.ts)}</td><td>${a.ok ? pill('ok', 'ok') : pill('fail', 'bad')}</td><td class="mono small">${esc(a.action)}</td>
    <td class="num">${a.durationMs}ms</td><td class="small">${esc(a.projectName || '')} <span class="muted">${esc(a.documentType || '')}</span></td>
    <td class="mono small">${esc(a.clientId || '')}</td><td class="small err">${esc(a.errorCode || '')} ${esc((a.errorMsg || '').slice(0, 140))}</td></tr>`).join('') || '<tr><td colspan=7 class="empty">暂无</td></tr>'}
  </tbody></table></div></div>`;
}

function viewAgents() {
  const r = S.runs || {};
  const b = r.bridge || {};
  return `<h1>Agent 运行</h1>
  <div class="note">${pill(b.status || 'planned', 'planned')} ${esc(b.version || 'v0.8')}：${esc(b.note || '')}</div>
  <div class="card"><h2>登记的运行 <span class="muted">POST /api/runs/events；v0.7 由 CLI 的长离线命令（sim / report / intent / pcb auto / kb）上报</span></h2>
  <div class="scroll"><table><thead><tr><th>运行</th><th>Agent</th><th>状态</th><th>开始</th><th>结束</th><th>目录</th><th>详情</th></tr></thead><tbody>
  ${(r.runs || []).map(x => `<tr><td><b>${esc(x.title || x.id)}</b><div class="mono small muted">${esc(x.command || '')}</div></td><td>${pill(x.agent, 'info')}</td>
    <td>${pill(x.status, x.status === 'ok' ? 'ok' : (x.status === 'running' ? 'running' : (x.status === 'failed' ? 'bad' : 'warn')))}</td>
    <td class="nowrap">${fmtTime(x.startedAt)}</td><td class="nowrap">${x.endedAt && !x.endedAt.startsWith('0001') ? fmtTime(x.endedAt) : '—'}</td>
    <td class="mono small">${esc(x.cwd || '')}</td><td class="small">${esc(x.detail || '')}</td></tr>`).join('') || '<tr><td colspan=7 class="empty">暂无登记的运行</td></tr>'}
  </tbody></table></div></div>
  <div class="card"><h2>推断的 CLI 会话 <span class="muted">按主机(+PCBPILOT_CLIENT_LABEL)聚合，间隔 &lt; 5 分钟为同一会话；每条 CLI 命令是一个进程</span></h2>
  <div class="scroll"><table><thead><tr><th>会话</th><th>状态</th><th>工程</th><th>开始</th><th>最后</th><th>命令</th><th>动作</th><th>失败</th><th>最后动作 / 错误</th></tr></thead><tbody>
  ${(r.sessions || []).slice(0, 100).map(s => `<tr><td class="mono small">${esc(s.host)}${s.label ? ':' + esc(s.label) : ''}</td><td>${s.active ? pill('活动', 'ok') : pill('结束', 'idle')}</td>
    <td>${esc(s.project || '—')}</td><td class="nowrap">${fmtTime(s.started)}</td><td class="nowrap">${ago(s.last)}</td><td class="num">${s.commands}</td><td class="num">${s.actions}</td>
    <td class="num">${s.failures || ''}</td><td class="small mono">${esc(s.lastAction)}${s.lastError ? `<div class="err">${esc(s.lastError)}</div>` : ''}</td></tr>`).join('') || '<tr><td colspan=9 class="empty">暂无</td></tr>'}
  </tbody></table></div></div>`;
}

function decisionCard(q) {
  const ctx = q.context ? `<pre>${esc(q.context)}</pre>` : '';
  const meta = [q.agent && ('agent ' + q.agent), q.step && ('步骤 ' + q.step), q.project && ('工程 ' + q.project)].filter(Boolean).map(esc).join(' · ');
  const ans = q.answer ? `<div class="small">${pill(q.status, q.status)} 选择 <b>${esc(q.answer.choice)}</b> ${q.answer.note ? '· ' + esc(q.answer.note) : ''} <span class="muted">by ${esc(q.answer.by)} ${ago(q.answer.at)}</span></div>` : '';
  const opts = q.status === 'pending' ? `<div class="opts">${(q.options || []).map(o => `<button class="${o.id === q.default ? 'primary' : ''}" data-ans="${esc(q.id)}" data-choice="${esc(o.id)}" title="${esc(o.detail || '')}">${esc(o.label)}</button>`).join('')}
    ${q.allowFree ? `<input type="text" id="free-${esc(q.id)}" placeholder="自由回答"><button data-ans="${esc(q.id)}" data-free="1">提交</button>` : ''}
    <input type="text" id="note-${esc(q.id)}" placeholder="备注（可选）"><button class="ghost" data-cancel="${esc(q.id)}">撤销</button></div>
    <div class="muted small">到期 ${fmtTime(q.expiresAt)}${q.default ? ' · 到期默认 ' + esc(q.default) : ''}</div>` : '';
  return `<div class="decision ${esc(q.status)}"><div><b>${esc(q.question)}</b></div><div class="muted small">${meta} · ${ago(q.createdAt)}</div>${ctx}${opts}${ans}</div>`;
}
function viewDecisions() {
  const pend = S.decisions.filter(q => q.status === 'pending');
  const done = S.decisions.filter(q => q.status !== 'pending');
  return `<h1>决策卡</h1><div class="note">Agent 用 <code>pcbpilot ask</code> 提问（Layout 确认、改值计划、换脚、跳过步骤…），在这里回答后命令立即返回你的选择。
  决策只记录你的取舍，不代替回读证据，也不能批准 Skill 禁止的操作。历史写入 <code>~/.pcbpilot/console/decisions.jsonl</code>。</div>
  <h2>待回答 ${pill(pend.length, pend.length ? 'warn' : 'idle')}</h2>${pend.map(decisionCard).join('') || '<div class="empty">没有待回答的问题。</div>'}
  <h2>已处理</h2>${done.map(decisionCard).join('') || '<div class="empty">暂无。</div>'}`;
}

// ── event binding after each render ───────────────────────────────────────
function bind() {
  document.querySelectorAll('[data-ans]').forEach(b => b.onclick = async () => {
    const id = b.dataset.ans;
    const choice = b.dataset.free ? $('#free-' + id).value.trim() : b.dataset.choice;
    const noteEl = $('#note-' + id);
    b.disabled = true;
    try { await api(`/api/ask/${id}/answer`, { method: 'POST', json: { choice, note: noteEl ? noteEl.value : '' } }); } catch (e) { alert(e.message); }
    refreshDecisions();
  });
  document.querySelectorAll('[data-cancel]').forEach(b => b.onclick = async () => {
    try { await api(`/api/ask/${b.dataset.cancel}/cancel`, { method: 'POST' }); } catch (e) {}
    refreshDecisions();
  });
  const add = $('#wd-add');
  if (add) add.onclick = async () => {
    try { await api('/api/workdirs', { method: 'POST', json: { dir: $('#wd-path').value.trim(), name: $('#wd-name').value.trim() } }); $('#wd-path').value = ''; refreshProjects(); }
    catch (e) { $('#wd-msg').innerHTML = `<span class="err">${esc(e.message)}</span>`; }
  };
  document.querySelectorAll('[data-rm]').forEach(b => b.onclick = async () => {
    if (!confirm('只取消登记，不删除任何文件。继续？')) return;
    try { await api('/api/workdirs/' + b.dataset.rm, { method: 'DELETE' }); refreshProjects(); } catch (e) { alert(e.message); }
  });
  const af = $('#act-filter');
  if (af) af.oninput = () => debounce('af', 200, render);
  // work dir tabs
  if (S.page === 'workdir' && S.wd && S.wd.workDir) {
    if (S.wdTab === 'library') {
      if (kbDocs === null || kbDocs.__wd !== S.wd.workDir.id) { kbDocs = null; loadKB().then(() => { if (kbDocs) kbDocs.__wd = S.wd.workDir.id; }); }
      const drop = $('#drop');
      if (drop) {
        drop.ondragover = e => { e.preventDefault(); drop.classList.add('over'); };
        drop.ondragleave = () => drop.classList.remove('over');
        drop.ondrop = e => { e.preventDefault(); drop.classList.remove('over'); if (e.dataTransfer.files.length) uploadFiles(e.dataTransfer.files); };
      }
      const fi = $('#kb-file'); if (fi) fi.onchange = () => fi.files.length && uploadFiles(fi.files);
      const go = $('#kb-go'), q = $('#kb-q');
      const search = async () => { S.kbQuery = q.value; try { S.kbResult = await api(`/api/workdirs/${S.wd.workDir.id}/kb/search?q=${encodeURIComponent(q.value)}&k=20`); } catch (e) { S.kbResult = null; } render(); };
      if (go) go.onclick = search;
      if (q) { q.value = S.kbQuery; q.onkeydown = e => { if (e.key === 'Enter') search(); }; }
      bindKBTags();
    }
    if (S.wdTab === 'process') {
      const init = $('#tpl-init');
      if (init) init.onclick = async () => {
        try { await api(`/api/workdirs/${S.wd.workDir.id}/config/init`, { method: 'POST', json: { template: $('#tpl').value, edaProject: $('#tpl-eda').value.trim() } }); loadWorkDir(S.wd.workDir.id); }
        catch (e) { alert(e.message); }
      };
      document.querySelectorAll('input[data-group]').forEach(el => el.onchange = () => {
        const why = document.getElementById(`why-${el.dataset.group}-${el.dataset.id}`); if (why) why.hidden = el.checked;
      });
      const save = $('#cfg-save'); if (save) save.onclick = saveConfig;
    }
  }
}
function bindKBTags() {
  document.querySelectorAll('[data-tag]').forEach(b => b.onclick = async () => {
    const t = prompt('添加标签（逗号分隔；前缀 - 表示移除）');
    if (!t) return;
    const parts = t.split(',').map(s => s.trim()).filter(Boolean);
    try {
      await api(`/api/workdirs/${S.wd.workDir.id}/kb/${b.dataset.tag}/tags`, { method: 'POST', json: { add: parts.filter(p => !p.startsWith('-')), remove: parts.filter(p => p.startsWith('-')).map(p => p.slice(1)) } });
      loadKB();
    } catch (e) { alert(e.message); }
  });
}

// ── boot ──────────────────────────────────────────────────────────────────
(async () => {
  await initAuth();
  route();
  connect();
})();
