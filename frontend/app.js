/* ============================================================
   微信多开助手 · 前端逻辑（简洁版）
   两个模式：新开窗口 / 复登账号；后端接口 window.go.main.App.*
   ============================================================ */

const $ = (id) => document.getElementById(id);

const el = {
  seg: document.querySelector('.seg'),
  helpBtn: $('helpBtn'),
  helpPop: $('helpPop'),
  helpWrap: document.querySelector('.help-wrap'),
  segNew: $('segNew'),
  segLogin: $('segLogin'),
  panelStats: $('panelStats'),
  panelLogin: $('panelLogin'),
  accountList: $('accountList'),
  listTitle: $('listTitle'),
  heroStats: $('heroStats'),
  searchInput: $('searchInput'),
  searchClear: $('searchClear'),
  btnLaunchAll: $('btnLaunchAll'),
  cta: $('cta'),
  btnLaunch: $('btnLaunch'),
  launchText: $('launchText'),
  btnSave: $('btnSave'),
  btnDelete: $('btnDelete'),
  btnInstall: $('btnInstall'),
  btnCache: $('btnCache'),
  installPath: $('installPath'),
  cachePath: $('cachePath'),
  installVer: $('installVer'),
  cacheVer: $('cacheVer'),
  accountsPath: $('accountsPath'),
  dirNote: $('dirNote'),
  dirTag: $('dirTag'),
  fold: $('fold'),
  foldBtn: $('foldBtn'),
  foldBody: $('foldBody'),

  toasts: $('toasts'),
};

const ICON_CHECK = `<svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 10.5l3.6 3.6L16 5.6"/></svg>`;
const ICON_PLAY = `<svg viewBox="0 0 20 20" width="18" height="18" fill="currentColor" aria-hidden="true"><path d="M6.5 4.3l9 5.7-9 5.7z"/></svg>`;

let state = null;
let mode = 'new';          // new | login
let picked = new Set();    // 复登模式勾选的账号名（多选）
let query = '';            // 账号搜索词
let dirOpen = false;
let busy = false;

/* ------------------------------------------------------------ 后端 */

function api() {
  return window.go && window.go.main && window.go.main.App ? window.go.main.App : null;
}

async function waitForBridge(timeoutMs = 6000) {
  const started = Date.now();
  while (!api()) {
    if (Date.now() - started > timeoutMs) throw new Error('后端未就绪');
    await new Promise((r) => setTimeout(r, 60));
  }
}

async function invoke(method, ...args) {
  const backend = api();
  if (!backend) throw new Error('后端未就绪');
  return backend[method](...args);
}

/* ------------------------------------------------------------ 小工具 */

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

function stripSlash(p) {
  return String(p || '').replace(/[\\/]+$/, '');
}

/* 路径太长保留头尾，完整值放 title */
function midTrunc(p, head = 12, tail = 18) {
  if (p.length <= head + tail + 1) return p;
  return p.slice(0, head) + '…' + p.slice(-tail);
}

/* 只留末尾 n 段：账号目录永远在 <安装盘>:\…\data\allUsersConfig 这种深路径下，
   按字符截会截出「…ata\allUsersConfig」这种半截单词，不如按目录段截。 */
function pathTail(p, n = 2) {
  const parts = String(p || '').split(/[\\/]+/).filter(Boolean);
  if (!parts.length) return String(p || '');
  if (parts.length <= n + 1) return p;
  return '…\\' + parts.slice(-n).join('\\');
}

function readyAccounts() {
  return (state && state.accounts ? state.accounts : []).filter((a) => a.ready);
}

/* ------------------------------------------------------------ 渲染 */

function render() {
  if (!state) return;

  // 模式
  el.seg.dataset.mode = mode;
  const isNew = mode === 'new';
  el.segNew.classList.toggle('is-on', isNew);
  el.segLogin.classList.toggle('is-on', !isNew);
  el.segNew.setAttribute('aria-selected', String(isNew));
  el.segLogin.setAttribute('aria-selected', String(!isNew));
  el.panelLogin.hidden = isNew;
  el.cta.classList.toggle('is-new', isNew);   // 新开窗口：主按钮和「保存当前账号」并排、加高

  // 账号列表（复登模式）
  renderAccounts();

  // 主按钮
  const pickedList = pickedAccounts();
  el.btnLaunch.disabled = busy || !state.ready || (!isNew && pickedList.length === 0);
  el.launchText.textContent = isNew
    ? '打开登录窗口'
    : (pickedList.length > 1 ? `登录 ${pickedList.length} 个账号` : '登录选中账号');
  if (!busy) el.btnLaunch.innerHTML = ICON_PLAY + `<span id="launchText">${escapeHtml(el.launchText.textContent)}</span>`;
  el.launchText = $('launchText');

  // 次要按钮：新开窗口只留「保存当前账号」，复登账号只留「删除账号」
  el.btnSave.hidden = !isNew;
  el.btnDelete.hidden = isNew;
  el.btnSave.disabled = busy || !state.cacheOk;
  el.btnDelete.disabled = busy || isNew || pickedList.length === 0;

  // 新开窗口的统计行：一个账号都没存过就整块收起来，
  // 不然按钮下面空留一行「还没有保存过账号」，还带着一条分隔线
  if (el.heroStats) {
    const accounts = state.accounts || [];
    const n = accounts.length;
    el.panelStats.hidden = !isNew || n === 0;
    el.heroStats.innerHTML = n
      ? `已保存 <b>${n}</b> 个账号<span class="sep"></span>最近保存 <b>${escapeHtml((accounts[0] && accounts[0].savedAt) || '—')}</b>`
      : '';
  }

  // 目录设置
  el.installPath.textContent = state.installDir ? midTrunc(stripSlash(state.installDir)) : '未设置';
  el.installPath.classList.toggle('is-empty', !state.installDir);
  el.installPath.classList.toggle('is-bad', !!state.installDir && !state.installOk);
  el.installPath.title = state.installDir || '';
  el.installVer.textContent = state.installOk ? `微信 ${state.weixinVer}` : state.installDir ? '无效' : '';
  el.installVer.classList.toggle('bad', !!state.installDir && !state.installOk);

  el.cachePath.textContent = state.cacheDir ? midTrunc(stripSlash(state.cacheDir)) : '未设置';
  el.cachePath.classList.toggle('is-empty', !state.cacheDir);
  el.cachePath.classList.toggle('is-bad', !!state.cacheDir && !state.cacheOk);
  el.cachePath.title = state.cacheDir || '';
  el.cacheVer.textContent = state.cacheOk ? '已识别' : state.cacheDir ? '无效' : '';
  el.cacheVer.classList.toggle('bad', !!state.cacheDir && !state.cacheOk);

  el.dirTag.textContent = state.ready ? '已配置' : '待配置';
  el.dirTag.classList.toggle('ok', !!state.ready);

  // 账号目录：每个账号一个文件夹，正式数据在这儿，跟上面两个待填路径分开显示
  if (el.accountsPath) {
    el.accountsPath.textContent = state.configDir ? pathTail(state.configDir, 2) : '—';
    el.accountsPath.title = state.configDir || '';
  }

  el.btnInstall.disabled = busy;
  el.btnCache.disabled = busy;

  el.foldBtn.setAttribute('aria-expanded', String(dirOpen));
  el.foldBody.hidden = !dirOpen;

  if (!state.ready) {
    el.dirNote.textContent = state.installErr || state.cacheErr
      || '需要分别指向 Weixin.exe 所在目录和包含 xwechat_files 的目录。';
    el.dirNote.classList.add('bad');
  } else {
    el.dirNote.textContent = '需要分别指向 Weixin.exe 所在目录和包含 xwechat_files 的目录。';
    el.dirNote.classList.remove('bad');
  }

  // 多开守护的状态不再占界面（原来在底栏），只写进 data\debug.log；
  // 真失败时后端在启动阶段就会弹窗，用户不需要盯着这一行。
  fitWindow();
}

/* 让窗口高度贴着内容走：目录展开、账号增减都会重新量一次。
   注意不能用 main.scrollHeight —— .main 是 flex:1，内容少时它会等于视口高度，
   量出来永远是当前窗口高度。这里按子元素累加，才是真实内容高度。 */
let fitTimer = null;
let lastFit = 0;

// 算出窗口需要多高；exclude 是要扣掉的高度（比如即将收起的目录内容）
//
// 不能用「子元素 offsetHeight 加起来 + gap 数」那种算法：
// 隐藏的 .panel 高度是 0 却照样占一个 gap，负 margin 和外边距它也一律看不见，
// 算出来总会差一点，窗口就比内容高出一截（底下空一条）。
// 这里直接量布局位置：从 main 的顶，量到最后一块真实内容的底（连它的下外边距）。
// 中间有多少 gap、谁有负 margin，全都不用管；.grow 根本不参与计算。
function measureNeeded(exclude = 0) {
  const hdr = document.querySelector('.hdr');
  const ftr = document.querySelector('.ftr');   // 底栏已经去掉了，这里留个兜底
  const main = document.getElementById('main');
  if (!hdr || !main) return 0;

  const real = [...main.children].filter((k) => !k.classList.contains('grow'));
  if (!real.length) return 0;
  const last = real[real.length - 1];
  const mb = parseFloat(getComputedStyle(last).marginBottom) || 0;
  // 底部内边距从样式里读，别写死：改 CSS 时这里会跟着走，不然窗口会高出一截
  const padBottom = parseFloat(getComputedStyle(main).paddingBottom) || 0;

  // last.offsetTop 已经含 main 的 padding-top，再加上底部内边距就齐了
  const mainNeed = (last.offsetTop + last.offsetHeight + mb - main.offsetTop) + padBottom;

  return Math.ceil(hdr.offsetHeight + mainNeed - exclude + (ftr ? ftr.offsetHeight : 0));
}

function fitWindow() {
  clearTimeout(fitTimer);
  fitTimer = setTimeout(() => {
    const need = measureNeeded();
    if (!need || Math.abs(need - lastFit) < 4) return;
    lastFit = need;
    invoke('FitHeight', need).catch(() => {});
  }, 60);
}

/* 立刻量、立刻改高度：切模式这种「内容整块换掉」的操作不能等防抖，
   否则这一百来毫秒里窗口还是旧高度，底部内容被裁、看着就像跳了一下。 */
async function fitNow() {
  clearTimeout(fitTimer);
  const need = measureNeeded();
  if (!need || Math.abs(need - lastFit) < 2) return;
  lastFit = need;
  try {
    await invoke('FitHeight', need);
  } catch (e) {
    /* 后端不可用就算了，后面的 fitWindow 会兜底 */
  }
}

// 调试用：把数字写进 data\debug.log（Go 那边有 LogJS）
function logjs(msg) {
  try {
    if (api()) api().LogJS(String(msg)).catch(() => {});
  } catch (e) { /* ignore */ }
}

// 当前勾选、且仍然存在的账号
function pickedAccounts() {
  if (!state) return [];
  return (state.accounts || []).filter((a) => picked.has(a.name));
}

function pickedNames() {
  return pickedAccounts().map((a) => a.name);
}

function renderAccounts() {
  const accounts = state.accounts || [];
  const ready = accounts.filter((a) => a.ready);

  // 勾选状态跟着真实账号列表走：账号被删了就从集合里剔掉
  for (const name of [...picked]) {
    if (!accounts.some((a) => a.name === name)) picked.delete(name);
  }

  el.btnLaunchAll.hidden = ready.length < 2;
  const q = query.trim().toLowerCase();
  const visible = q ? accounts.filter((a) => a.name.toLowerCase().includes(q)) : accounts;
  el.listTitle.textContent = q
    ? `搜索到 ${visible.length} 个`
    : (accounts.length ? `可选账号 · ${accounts.length} 个` : '可选账号');

  if (!accounts.length) {
    el.accountList.innerHTML = `<div class="empty">还没有保存过账号。<br>切到「新开窗口」扫码登录，再点「保存当前账号」。</div>`;
    return;
  }
  if (!visible.length) {
    el.accountList.innerHTML = `<div class="empty">没有匹配「${escapeHtml(query.trim())}」的账号</div>`;
    return;
  }

  el.accountList.innerHTML = visible.map((a) => {
    const on = picked.has(a.name);
    return `<button class="acct" role="checkbox" aria-checked="${on}" data-name="${escapeHtml(a.name)}">
        <span class="acct-dot ${a.ready ? '' : 'bad'}"></span>
        <span class="acct-name">${escapeHtml(a.name)}</span>
        <span class="acct-meta">${escapeHtml(a.ready ? (a.savedAt || '已保存') : '配置不完整')}</span>
        <span class="acct-check ${on ? 'is-on' : ''}">${on ? ICON_CHECK : ''}</span>
      </button>`;
  }).join('');

  // 账号多的时候，把第一个勾选/首个账号滚进可视区。
  // 注意只能滚 .list 自己 —— 用 scrollIntoView 会连带外层容器一起滚，
  // 整页就会跟着往上窜。
  const active = el.accountList.querySelector('.acct[aria-checked="true"]') || el.accountList.querySelector('.acct');
  if (active && el.accountList.scrollHeight > el.accountList.clientHeight + 1) {
    const box = el.accountList.getBoundingClientRect();
    const row = active.getBoundingClientRect();
    if (row.top < box.top) el.accountList.scrollTop -= box.top - row.top;
    else if (row.bottom > box.bottom) el.accountList.scrollTop += row.bottom - box.bottom;
  }
}

/* ------------------------------------------------------------ 模式切换 */

el.segNew.addEventListener('click', () => switchMode('new'));
el.segLogin.addEventListener('click', () => switchMode('login'));

function switchMode(next) {
  if (mode === next) return;
  mode = next;
  if (next === 'new') {
    query = '';
    el.searchInput.value = '';
    el.searchClear.hidden = true;
  }
  // 先换面板，再把窗口高度立刻跟上去：中途多出来的部分是被裁掉的，
  // 不会有「先滚一下、再长高」的抖动。
  try {
    render();
    fitNow();
  } catch (e) {
    logjs('switchMode 出错: ' + (e && e.message));
    return;
  }
  if (next === 'login') {
    const first = el.accountList.querySelector('.acct');
    if (first) first.focus({ preventScroll: true });
  }
}

// 单击切换勾选（多选），再点一次取消
el.accountList.addEventListener('click', (e) => {
  const btn = e.target.closest('.acct');
  if (!btn) return;
  const name = btn.dataset.name;
  if (picked.has(name)) picked.delete(name);
  else picked.add(name);
  render();
  const again = el.accountList.querySelector(`.acct[data-name="${CSS.escape(name)}"]`);
  if (again) again.focus({ preventScroll: true });
});

// 键盘：上下移动焦点，空格切换勾选（按钮原生行为），回车启动勾选的账号
el.accountList.addEventListener('keydown', (e) => {
  if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp' && e.key !== 'Enter') return;
  const rows = [...el.accountList.querySelectorAll('.acct')];
  if (!rows.length) return;
  if (e.key === 'Enter') {
    e.preventDefault();
    el.btnLaunch.click();
    return;
  }
  e.preventDefault();
  const cur = rows.indexOf(document.activeElement);
  const next = e.key === 'ArrowDown'
    ? Math.min(rows.length - 1, cur + 1)
    : Math.max(0, cur - 1);
  rows[next].focus({ preventScroll: false });
});

/* ------------------------------------------------------------ 使用流程气泡 */

let helpTimer = null;

// 气泡水平位置：右边缘对齐窗口内容区（16px 边距），左右都不许越出窗口。
// 以前在 CSS 里写死 right:-68px（按“使用流程 + — + ✕”三个按钮算的），后来标题栏删掉一个按钮，
// 锚点右移 34px，气泡就整块顶出右边缘、被窗口裁掉半截。
const HELP_MARGIN = 16;
function placeHelp() {
  if (el.helpPop.hidden) return;
  const wrapRight = el.helpWrap.getBoundingClientRect().right;
  const width = el.helpPop.offsetWidth;
  let right = wrapRight - (window.innerWidth - HELP_MARGIN);
  const minRight = wrapRight - (window.innerWidth - 8);
  const maxRight = wrapRight - 8 - width;
  if (right < minRight) right = minRight;
  if (right > maxRight) right = maxRight;
  el.helpPop.style.right = Math.round(right) + 'px';
}
window.addEventListener('resize', placeHelp);

// 气泡是绝对定位的，撑不大窗口；窗口矮的时候它就会被窗口底边裁掉。
// WebView 里的东西不可能画到窗口外面去，所以打开时先把窗口临时长高到能装下整个气泡，
// 关掉再缩回去。
async function showHelp() {
  clearTimeout(helpTimer);
  el.helpPop.hidden = false;
  el.helpBtn.setAttribute('aria-expanded', 'true');
  placeHelp();

  // 用 offsetTop/offsetHeight 算底部，别用 getBoundingClientRect()：
  // 气泡有 140ms 的入场动画（translateY + scale），动画期间 rect 会被 transform 带偏，
  // 量出来的高度每次都不一样（日志里就出现过 298/303 来回跳）。
  const wrapTop = el.helpWrap.getBoundingClientRect().top;
  const bottom = wrapTop + el.helpPop.offsetTop + el.helpPop.offsetHeight;
  const need = Math.ceil(bottom) + 8;   // 留 8px 给阴影
  if (need > window.innerHeight) {
    lastFit = need;
    try {
      await invoke('FitHeight', need);
    } catch (e) {
      /* 后端不可用就让它裁着，不影响主流程 */
    }
  }
}

function hideHelp(delay = 120) {
  clearTimeout(helpTimer);
  helpTimer = setTimeout(() => {
    const wasOpen = !el.helpPop.hidden;
    el.helpPop.hidden = true;
    el.helpBtn.setAttribute('aria-expanded', 'false');
    // 把为了气泡临时长出来的高度还回去
    if (wasOpen) fitNow();
  }, delay);
}

// 鼠标悬停要停够 1 秒才弹，不然鼠标扫过标题栏就被拦一下；键盘 focus / 点击仍然是立刻
const HELP_HOVER_DELAY = 1000;
let helpOpenTimer = null;

el.helpBtn.addEventListener('mouseenter', () => {
  clearTimeout(helpOpenTimer);
  helpOpenTimer = setTimeout(showHelp, HELP_HOVER_DELAY);
});
el.helpBtn.addEventListener('mouseleave', () => {
  clearTimeout(helpOpenTimer);
  hideHelp();
});
el.helpBtn.addEventListener('focus', showHelp);
el.helpBtn.addEventListener('blur', () => hideHelp());
el.helpPop.addEventListener('mouseenter', () => {
  clearTimeout(helpOpenTimer);
  showHelp();
});
el.helpPop.addEventListener('mouseleave', () => hideHelp());
// 触屏/点击也能开关
el.helpBtn.addEventListener('click', (e) => {
  e.stopPropagation();
  if (el.helpPop.hidden) showHelp();
  else hideHelp(0);
});
document.addEventListener('click', (e) => {
  if (!el.helpWrap.contains(e.target)) hideHelp(0);
});
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && !el.helpPop.hidden) hideHelp(0);
});
/* ------------------------------------------------------------ 账号搜索 */

el.searchInput.addEventListener('input', () => {
  query = el.searchInput.value;
  el.searchClear.hidden = !query;
  render();
});

el.searchClear.addEventListener('click', () => {
  el.searchInput.value = '';
  query = '';
  el.searchClear.hidden = true;
  render();
  el.searchInput.focus();
});

// Ctrl+F / 直接敲 / 都能跳到搜索框
document.addEventListener('keydown', (e) => {
  const inInput = e.target && (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA');
  if ((e.ctrlKey && e.key.toLowerCase() === 'f') || (e.key === '/' && !inInput)) {
    if (el.panelLogin.hidden) return;
    e.preventDefault();
    el.searchInput.focus();
    el.searchInput.select();
  }
});
/* ------------------------------------------------------------ 折叠目录设置 */

el.foldBtn.addEventListener('click', async () => {
  if (!dirOpen) {
    dirOpen = true;
    render();
    fitNow();          // 展开也是整块换高度，立刻跟上去，别等防抖
    return;
  }
  // 收起：先把目录内容设成「隐形但还占位」，让窗口缩到位，再真正隐藏。
  // 否则会出现「窗口还是高的、内容已经没了」的空档，看起来就是页面往下跳一下。
  const bodyH = el.foldBody.offsetHeight;
  el.foldBody.style.visibility = 'hidden';
  try {
    const need = measureNeeded(bodyH);
    if (need) {
      lastFit = need;
      await invoke('FitHeight', need);
    }
  } catch (e) {
    /* 后端不可用就直接收起，下面 render 会再兜一次 */
  } finally {
    el.foldBody.style.visibility = '';
  }
  dirOpen = false;
  render();
});

/* ------------------------------------------------------------ Toast */

function toast(msg, kind = 'ok', ms = 2600) {
  const node = document.createElement('div');
  node.className = `toast ${kind === 'ok' ? '' : kind}`;
  node.innerHTML = `<span class="t-dot"></span><span>${escapeHtml(msg)}</span>`;
  el.toasts.appendChild(node);
  setTimeout(() => {
    node.classList.add('hide');
    setTimeout(() => node.remove(), 220);
  }, ms);
}

/* ------------------------------------------------------------ 弹窗（原生窗口） */

// 弹窗开在屏幕正中的独立窗口里（Go 侧 syswin.ShowDialog 自绘、输入框用系统 EDIT），
// 不再挤在 360px 的主界面里 —— 那样一定会被窗口边界裁掉，也没法居中。
async function askDialog(opts) {
  const req = {
    kind: opts.kind || 'confirm',
    title: opts.title || '',
    desc: opts.desc || '',
    label: opts.label || '',
    hint: opts.hint || '',
    initial: opts.initial || '',
    okText: opts.okText || '确定',
    cancelText: opts.cancelText || '取消',
    danger: !!opts.danger,
  };
  try {
    const r = await invoke('PromptDialog', req);
    return r && typeof r === 'object' ? r : { ok: false, text: '' };
  } catch (e) {
    logjs('PromptDialog 调用失败: ' + (e && e.message ? e.message : e));
    return { ok: false, text: '' };
  }
}

/* 纯提示框（错误信息常带完整路径，交给原生窗口显示，能换行能滚动） */
async function alertModal(title, desc) {
  return askDialog({ kind: 'alert', title, desc, okText: '知道了' });
}

/* ------------------------------------------------------------ 操作 */

function applyResult(res) {
  if (res && res.state) state = res.state;
  render();
  if (!res) return;
  if (res.code === 'cancel') return;
  if (res.ok) {
    toast(res.msg || '完成', 'ok');
  } else {
    // 失败信息可能很长（带完整路径），弹窗显示，能换行、能滚动、能选中复制
    alertModal('操作失败', res.msg || '未知错误');
  }
}

async function withBusy(fn) {
  busy = true;
  render();
  try {
    await fn();
  } catch (err) {
    alertModal('出错了', String(err && err.message ? err.message : err));
  } finally {
    busy = false;
    render();
  }
}

function launchLabel() {
  if (mode === 'new') return '打开登录窗口';
  const n = pickedNames().length;
  return n > 1 ? `登录 ${n} 个账号` : '登录选中账号';
}

function setLaunchButton(text, spinning) {
  el.btnLaunch.disabled = true;
  el.btnLaunch.innerHTML = spinning
    ? `<span class="spinner"></span><span id="launchText">${escapeHtml(text)}</span>`
    : ICON_PLAY + `<span id="launchText">${escapeHtml(text)}</span>`;
  el.launchText = $('launchText');
}

el.btnLaunch.addEventListener('click', () => {
  if (busy || !state) return;
  const names = pickedNames();
  if (mode === 'login' && names.length === 0) {
    toast('先勾选要登录的账号', 'warn');
    return;
  }
  withBusy(async () => {
    setLaunchButton('启动中…', true);
    const res = await invoke('Launch', mode === 'login' ? 'picked' : 'new', names);
    setLaunchButton(launchLabel(), false);
    applyResult(res);
  });
});

el.btnLaunchAll.addEventListener('click', () => {
  if (busy) return;
  withBusy(async () => {
    setLaunchButton('启动中…', true);
    const res = await invoke('Launch', 'all', []);
    setLaunchButton(launchLabel(), false);
    applyResult(res);
  });
});

el.btnSave.addEventListener('click', () => {
  if (busy || !state) return;
  withBusy(async () => {
    const suggested = suggestName();
    const r1 = await askDialog({
      kind: 'input',
      title: '保存当前账号',
      desc: '保存当前登录的微信，下次免扫码直接进。',
      label: '账号名称',
      hint: '留空自动命名：' + suggested,
      okText: '保存',
    });
    if (!r1.ok) return;
    const name = String(r1.text || '').trim() || suggested;
    let res = await invoke('SaveAccount', name, false);
    if (res && res.code === 'exists') {
      const r2 = await askDialog({
        kind: 'confirm',
        title: '覆盖已有账号？',
        desc: `「${name}」已经存在，覆盖后旧的登录会话会丢失。`,
        danger: true,
        okText: '覆盖',
      });
      if (!r2.ok) return;
      res = await invoke('SaveAccount', name, true);
    }
    // 保存完就留在「新开窗口」页：不跳转、也不替你勾账号，
    // 免得看着像“已经选中要登录的号”。想登录自己切到复登页勾。
    applyResult(res);
  });
});

el.btnDelete.addEventListener('click', () => {
  const names = pickedNames();
  if (busy || names.length === 0) return;
  const one = names.length === 1;
  withBusy(async () => {
    const r3 = await askDialog({
      kind: 'confirm',
      title: one ? '删除账号' : `删除 ${names.length} 个账号`,
      desc: one
        ? `确定删除「${names[0]}」的本地登录会话吗？下次使用需要重新扫码。`
        : `确定删除这 ${names.length} 个账号的本地登录会话吗？\n${names.join('、')}`,
      danger: true,
      okText: '删除',
    });
    if (!r3.ok) return;
    let last = null;
    let failed = 0;
    for (const name of names) {
      const res = await invoke('DeleteAccount', name);
      if (res && res.ok) picked.delete(name);
      else failed++;
      last = res;
    }
    if (failed > 0 && last) {
      last.msg = `有 ${failed} 个账号删除失败`;
    }
    applyResult(last);
  });
});

el.btnInstall.addEventListener('click', () => {
  if (busy) return;
  withBusy(async () => applyResult(await invoke('ChooseInstall')));
});

el.btnCache.addEventListener('click', () => {
  if (busy) return;
  withBusy(async () => applyResult(await invoke('ChooseCache')));
});

$('btnAccounts').addEventListener('click', () => {
  withBusy(async () => applyResult(await invoke('OpenAccountsDir')));
});

// 关闭按钮 = 收进托盘，不是退出。真正退出只走托盘右键菜单的「退出程序」。
$('btnClose').addEventListener('click', async () => {
  try { await invoke('HideToTray'); } catch (e) { /* ignore */ }
});

document.addEventListener('keydown', (e) => {
  // 弹窗现在是原生窗口，Esc 由它自己处理；这里只留个空壳，防止以后需要页面级快捷键
});

function suggestName() {
  const d = new Date();
  const pad = (n) => String(n).padStart(2, '0');
  return `账号${pad(d.getMonth() + 1)}${pad(d.getDate())}-${pad(d.getHours())}${pad(d.getMinutes())}`;
}

/* ------------------------------------------------------------ 启动进度 */

function bindEvents() {
  const rt = window.runtime;
  if (!rt || typeof rt.EventsOn !== 'function') return;
  rt.EventsOn('launch:progress', (p) => {
    if (!p) return;
    if (p.done) {
      setLaunchButton(mode === 'new' ? '打开登录窗口' : '登录选中账号', false);
      return;
    }
    if (p.total > 1) {
      setLaunchButton(`启动中 ${p.index}/${p.total}${p.name ? ' · ' + p.name : ''}`, true);
    }
  });
}

/* ------------------------------------------------------------ 启动 */

async function boot() {
  try {
    await waitForBridge();
    bindEvents();
    state = await invoke('GetState');

    // 存过账号就默认停在「复登账号」，一个账号都没有才停「新开窗口」。
    // 不看 ready：会话文件坏了的账号也得让人看见、好去删掉。
    // 进来不预勾任何账号 —— 列表干净着给你看，勾哪个登录哪个。
    const all = state.accounts || [];
    mode = all.length ? 'login' : 'new';
    // 目录没配好就把目录设置摊开
    dirOpen = !state.ready;
    render();
    fitNow();   // 首帧就把高度定下来，别让窗口先冒个 520 再缩

    if (!state.ready) {
      toast('先设置微信安装目录和缓存目录', 'warn', 4000);
    } else if (state.autoDetected && state.autoDetected.length) {
      const parts = [];
      if (state.autoDetected.includes('install')) parts.push('安装目录');
      if (state.autoDetected.includes('cache')) parts.push('缓存目录');
      toast(`已自动识别微信${parts.join('和')}`, 'ok', 3400);
    }
  } catch (err) {
    // 没有底栏可以显示了，初始化失败就直接弹窗 + 写日志
    const msg = '初始化失败：' + (err && err.message ? err.message : err);
    alertModal('初始化失败', msg);
    logjs(msg);
  }
}

boot();

