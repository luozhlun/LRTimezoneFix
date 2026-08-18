const api = () => window.go.main.GUIApp;

const state = {
  selection: null,
  report: null,
  selected: new Set(),
  busy: false,
  cancelRequested: false,
  filter: 'all',
  search: '',
  thumbnailCache: new Map(),
};

let thumbnailObserver;

const el = (id) => document.getElementById(id);
const escapeHTML = (value = '') => String(value).replace(/[&<>'"]/g, (char) => ({
  '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;'
}[char]));

function errorText(error) {
  if (!error) return '发生未知错误';
  return error.message || String(error);
}

function setBusy(busy) {
  state.busy = busy;
  ['folderButton', 'filesButton', 'scanButton', 'repairButton'].forEach((id) => {
    const node = el(id);
    if (node) node.disabled = busy || (id === 'scanButton' && !state.selection) || (id === 'repairButton' && state.selected.size === 0);
  });
}

async function initialise() {
  bindEvents();
  if (window.runtime?.EventsOn) {
    window.runtime.EventsOn('lrtimezonefix:progress', updateProgress);
  }
  try {
    const info = await api().GetAppInfo();
    el('versionBadge').textContent = `v${info.version}`;
    const status = el('exifStatus');
    if (info.exifToolReady) {
      status.className = 'status-pill ready';
      status.innerHTML = '<span class="status-dot"></span>ExifTool 已就绪';
      status.title = info.exifToolPath;
    } else {
      status.className = 'status-pill error';
      status.innerHTML = '<span class="status-dot"></span>未找到 ExifTool';
      status.title = info.exifToolError;
      showToast(info.exifToolError, true, 7000);
    }
  } catch (error) {
    showToast(errorText(error), true);
  }
}

function bindEvents() {
  el('folderButton').addEventListener('click', () => choose('folder'));
  el('filesButton').addEventListener('click', () => choose('files'));
  el('scanButton').addEventListener('click', () => scanSelection());
  el('cancelScanButton').addEventListener('click', cancelScan);
  el('repairButton').addEventListener('click', repairSelected);
  el('searchInput').addEventListener('input', (event) => { state.search = event.target.value.trim().toLowerCase(); renderRows(); });
  el('stateFilter').addEventListener('change', (event) => { state.filter = event.target.value; renderRows(); });
  el('selectAllCandidates').addEventListener('change', toggleVisibleCandidates);
  el('closeDrawer').addEventListener('click', closeDrawer);
  el('drawerBackdrop').addEventListener('click', closeDrawer);
  document.addEventListener('keydown', (event) => { if (event.key === 'Escape') closeDrawer(); });
}

async function choose(mode) {
  if (state.busy) return;
  try {
    const selection = mode === 'folder' ? await api().ChooseFolder() : await api().ChooseFiles();
    if (!selection?.mode) return;
    state.selection = selection;
    state.report = null;
    state.selected.clear();
    state.thumbnailCache.clear();
    el('selectionPanel').classList.remove('empty');
    el('selectionTitle').textContent = selection.mode === 'folder' ? '递归扫描文件夹' : selection.label;
    el('selectionPath').textContent = selection.mode === 'folder' ? selection.root : selection.files.join('  ·  ');
    el('scanButton').disabled = false;
    el('resultsSection').classList.add('hidden');
    el('actionBar').classList.add('hidden');
    el('repairNotice').classList.add('hidden');
    closeDrawer();
  } catch (error) {
    showToast(errorText(error), true);
  }
}

async function scanSelection(options = {}) {
  if (!state.selection || state.busy) return;
  state.cancelRequested = false;
  setBusy(true);
  showProgress('scan', 0, 1, '正在准备扫描……');
  if (!options.preserveNotice) el('repairNotice').classList.add('hidden');
  try {
    state.report = await api().Scan(state.selection);
    state.selected = new Set(state.report.files.filter((file) => file.repairable).map((file) => file.index));
    renderReport();
    showToast(state.report.summary.candidates > 0 ? `发现 ${state.report.summary.candidates} 张需要修复的照片` : '扫描完成，没有发现需要修复的照片');
  } catch (error) {
    if (state.cancelRequested) {
      showToast('扫描已终止；没有修改任何文件');
    } else {
      showToast(errorText(error), true, 6500);
    }
  } finally {
    hideProgressSoon();
    setBusy(false);
    state.cancelRequested = false;
  }
}

async function cancelScan() {
  if (!state.busy || state.cancelRequested) return;
  state.cancelRequested = true;
  el('cancelScanButton').disabled = true;
  el('progressMessage').textContent = '正在安全终止当前扫描……';
  try {
    const accepted = await api().CancelScan();
    if (!accepted) state.cancelRequested = false;
  } catch (error) {
    state.cancelRequested = false;
    showToast(errorText(error), true);
  }
}

function updateProgress(progress) {
  showProgress(progress.phase, progress.done, progress.total, progress.message);
}

function showProgress(phase, done, total, message) {
  const indeterminate = !total || total <= 0;
  const safeTotal = Math.max(total || 1, 1);
  const percent = Math.max(0, Math.min(100, Math.round((done / safeTotal) * 100)));
  el('progressPanel').classList.remove('hidden');
  el('progressTitle').textContent = phase === 'repair' ? '正在安全修复' : '正在分析元数据';
  el('progressMessage').textContent = message || '正在处理……';
  el('progressBar').classList.toggle('indeterminate', indeterminate);
  el('progressBar').style.width = indeterminate ? '32%' : `${percent}%`;
  el('progressPercent').textContent = indeterminate ? '…' : `${percent}%`;
  const cancelButton = el('cancelScanButton');
  cancelButton.classList.toggle('hidden', phase !== 'scan');
  cancelButton.disabled = state.cancelRequested;
}

function hideProgressSoon() {
  window.setTimeout(() => {
    el('progressPanel').classList.add('hidden');
    el('cancelScanButton').classList.add('hidden');
  }, 550);
}

function renderReport() {
  const summary = state.report.summary;
  el('totalCount').textContent = summary.total;
  el('candidateCount').textContent = summary.candidates;
  el('consistentCount').textContent = summary.consistent;
  el('attentionCount').textContent = summary.ambiguous + summary.unreadable;
  el('resultsSection').classList.remove('hidden');
  el('actionBar').classList.toggle('hidden', summary.candidates === 0);
  state.filter = summary.candidates > 0 ? 'candidate' : 'all';
  el('stateFilter').value = state.filter;
  el('searchInput').value = '';
  state.search = '';
  renderRows();
  updateSelectedCount();
}

function filteredFiles() {
  if (!state.report) return [];
  return state.report.files.filter((file) => {
    const matchesSearch = !state.search || `${file.displayName} ${file.path}`.toLowerCase().includes(state.search);
    let matchesState = true;
    if (state.filter === 'candidate') matchesState = file.repairable;
    if (state.filter === 'consistent') matchesState = file.state === 'consistent';
    if (state.filter === 'attention') matchesState = file.state === 'ambiguous' || file.state === 'unreadable';
    return matchesSearch && matchesState;
  });
}

function renderGPSWarningBadge(file) {
  return file.gpsTimezoneReference?.needsManualReview
    ? '<span class="timezone-warning-chip" title="临近时区规则变更或当地时间存在歧义，建议打开详情人工核对">时区变更临近</span>'
    : '';
}

function renderGPSReference(file) {
  const gps = file.gpsTimezoneReference || {status: 'missing_gps', note: '照片未提供可用的 GPS 坐标。'};
  if (gps.status !== 'available') {
    return `<section class="detail-block"><h3>GPS 时区参考</h3>
      <div class="gps-reference-box unavailable"><strong>${gps.status === 'missing_gps' ? '未提供 GPS' : '无法推算'}</strong><span>${escapeHTML(gps.note || '没有足够的 GPS 或日期信息。')}</span></div>
    </section>`;
  }
  return `<section class="detail-block"><h3>GPS 时区参考</h3>
    <div class="gps-reference-box${gps.needsManualReview ? ' warning' : ''}">
      <div class="metadata-row"><span>坐标</span><span>${escapeHTML(gps.coordinates)}</span></div>
      <div class="metadata-row"><span>地理时区</span><span>${escapeHTML(gps.timezone)}</span></div>
      <div class="metadata-row"><span>参考当地时间</span><span>${escapeHTML(gps.referenceTime)}</span></div>
      <div class="metadata-row"><span>日期依据</span><span>${escapeHTML(gps.dateSource)}</span></div>
      <div class="metadata-row"><span>当时 UTC 偏移</span><span>${escapeHTML(gps.offset)}</span></div>
      <div class="metadata-row"><span>夏令时</span><span>${escapeHTML(gps.dstLabel)}</span></div>
      ${gps.warning ? `<div class="timezone-warning"><strong>建议人工核对</strong><span>${escapeHTML(gps.warning)}</span></div>` : ''}
      <p>${escapeHTML(gps.note || 'GPS 推算仅供参考，不参与自动修复判断。')} 边界数据来自 timezone-boundary-builder / OpenStreetMap 贡献者（ODbL）。</p>
    </div>
  </section>`;
}

function renderRows() {
  const files = filteredFiles();
  el('visibleCount').textContent = `${files.length} 张`;
  el('emptyResults').classList.toggle('hidden', files.length !== 0);
  el('resultsBody').innerHTML = files.map((file) => {
    const checked = state.selected.has(file.index) ? 'checked' : '';
    const checkbox = file.repairable ? `<input class="row-check" type="checkbox" data-index="${file.index}" ${checked} aria-label="选择 ${escapeHTML(file.displayName)}">` : '';
    const time = file.dateTimeOriginal || '—';
    const target = file.repairable ? `${file.targetLocal}${file.targetOffset}` : '—';
    const oldOffset = file.offsetTimeOriginal || '—';
    const offset = file.repairable ? `<span class="mono">${escapeHTML(oldOffset)}</span><span class="offset-arrow">→</span><span class="mono">${escapeHTML(file.targetOffset)}</span>` : `<span class="mono">${escapeHTML(oldOffset)}</span>`;
    return `<tr data-index="${file.index}">
      <td class="check-col">${checkbox}</td>
      <td class="thumb-col"><div class="photo-thumb"><span>JPG</span><img class="thumb-image" data-index="${file.index}" alt="" decoding="async"></div></td>
      <td class="file-cell"><div class="file-name-row"><div class="file-name" title="${escapeHTML(file.displayName)}">${escapeHTML(file.displayName)}</div>${renderGPSWarningBadge(file)}</div><div class="file-path">${escapeHTML(file.reason || file.path)}</div></td>
      <td class="reveal-col"><button class="reveal-button" type="button" data-index="${file.index}" title="在资源管理器中显示" aria-label="在资源管理器中显示 ${escapeHTML(file.displayName)}"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3.5 6.5h6l2 2h9v9.5a2 2 0 0 1-2 2h-15a2 2 0 0 1-2-2V8.5a2 2 0 0 1 2-2Z"/><path d="M2 10h20"/></svg></button></td>
      <td class="mono">${escapeHTML(time)}</td>
      <td>${offset}</td>
      <td class="mono">${escapeHTML(target)}</td>
      <td><span class="state-chip ${escapeHTML(file.state)}">${escapeHTML(file.stateLabel)}</span></td>
    </tr>`;
  }).join('');

  el('resultsBody').querySelectorAll('tr').forEach((row) => row.addEventListener('click', () => openDrawer(Number(row.dataset.index))));
  el('resultsBody').querySelectorAll('.row-check').forEach((box) => box.addEventListener('click', (event) => {
    event.stopPropagation();
    const index = Number(event.target.dataset.index);
    event.target.checked ? state.selected.add(index) : state.selected.delete(index);
    updateSelectedCount();
  }));
  el('resultsBody').querySelectorAll('.reveal-button').forEach((button) => button.addEventListener('click', (event) => {
    event.stopPropagation();
    revealFile(Number(button.dataset.index));
  }));
  observeThumbnails();
  updateSelectAllState(files);
}

function observeThumbnails() {
  thumbnailObserver?.disconnect();
  const images = [...el('resultsBody').querySelectorAll('.thumb-image')];
  if (!('IntersectionObserver' in window)) {
    images.slice(0, 30).forEach(loadThumbnail);
    return;
  }
  thumbnailObserver = new IntersectionObserver((entries) => {
    entries.filter((entry) => entry.isIntersecting).forEach((entry) => {
      thumbnailObserver.unobserve(entry.target);
      loadThumbnail(entry.target);
    });
  }, {root: el('tableWrap'), rootMargin: '160px 0px'});
  images.forEach((image) => thumbnailObserver.observe(image));
}

async function loadThumbnail(image) {
  if (!state.report || image.dataset.loading) return;
  const sessionId = state.report.sessionId;
  const index = Number(image.dataset.index);
  const key = `${sessionId}:${index}`;
  image.dataset.loading = 'true';
  try {
    let data = state.thumbnailCache.get(key);
    if (data === undefined) {
      data = await api().GetThumbnail(sessionId, index);
      if (state.thumbnailCache.size >= 300) state.thumbnailCache.delete(state.thumbnailCache.keys().next().value);
      state.thumbnailCache.set(key, data || '');
    }
    if (data && state.report?.sessionId === sessionId && image.isConnected) {
      image.addEventListener('load', () => image.closest('.photo-thumb')?.classList.add('loaded'), {once: true});
      image.src = data;
    }
  } catch (_) {
    // Missing or malformed embedded thumbnails keep the lightweight JPG placeholder.
  }
}

async function revealFile(index) {
  if (!state.report) return;
  try {
    await api().RevealFile(state.report.sessionId, index);
  } catch (error) {
    showToast(errorText(error), true);
  }
}

function updateSelectAllState(files = filteredFiles()) {
  const repairable = files.filter((file) => file.repairable);
  const selected = repairable.filter((file) => state.selected.has(file.index)).length;
  const box = el('selectAllCandidates');
  box.disabled = repairable.length === 0;
  box.checked = repairable.length > 0 && selected === repairable.length;
  box.indeterminate = selected > 0 && selected < repairable.length;
}

function toggleVisibleCandidates(event) {
  filteredFiles().filter((file) => file.repairable).forEach((file) => {
    event.target.checked ? state.selected.add(file.index) : state.selected.delete(file.index);
  });
  renderRows();
  updateSelectedCount();
}

function updateSelectedCount() {
  const count = state.selected.size;
  el('selectedCount').textContent = `已选择 ${count} 张`;
  el('repairButton').disabled = state.busy || count === 0;
  updateSelectAllState();
}

function openDrawer(index) {
  if (!state.report) return;
  const file = state.report.files.find((item) => item.index === index);
  if (!file) return;
  el('detailName').textContent = file.displayName;
  const target = file.repairable ? `${file.targetLocal}${file.targetOffset}` : '不自动修改';
  el('detailContent').innerHTML = `
    <section class="detail-block"><h3>完整路径</h3><div class="detail-path">${escapeHTML(file.path)}</div></section>
    <section class="detail-block"><h3>关键字段</h3>
      <div class="metadata-row"><span>DateTimeOriginal</span><span>${escapeHTML(file.dateTimeOriginal || '—')} ${escapeHTML(file.offsetTimeOriginal || '')}</span></div>
      <div class="metadata-row"><span>CreateDate</span><span>${escapeHTML(file.createDate || '—')} ${escapeHTML(file.offsetTimeDigitized || '')}</span></div>
      <div class="metadata-row"><span>墙上时间变化</span><span>${escapeHTML(file.repairable ? file.shift : '—')}</span></div>
    </section>
    ${renderGPSReference(file)}
    ${file.repairable ? `<section class="detail-block"><h3>计划修复</h3><div class="target-box"><strong>${escapeHTML(target)}</strong><span>两组时间与关联 EXIF/XMP/IPTC 将统一；UTC 时刻保持不变。</span></div></section>` : ''}
    <section class="detail-block"><h3>判断说明</h3><div class="reason-box">${escapeHTML(file.reason || '字段时间一致，无需处理。')}</div></section>`;
  el('detailDrawer').classList.add('open');
  el('detailDrawer').setAttribute('aria-hidden', 'false');
}

function closeDrawer() {
  el('detailDrawer').classList.remove('open');
  el('detailDrawer').setAttribute('aria-hidden', 'true');
}

async function repairSelected() {
  if (!state.report || state.busy || state.selected.size === 0) return;
  setBusy(true);
  showProgress('repair', 0, state.selected.size, '正在等待确认……');
  try {
    const result = await api().Repair({sessionId: state.report.sessionId, indices: [...state.selected]});
    if (result.cancelled) {
      showToast('已取消，没有修改照片');
      return;
    }
    const notice = el('repairNotice');
    notice.innerHTML = `<strong>修复完成：</strong>${result.succeeded} 张成功，${result.failed} 张失败并尝试恢复。备份目录名称为 <code>${escapeHTML(result.backupFolderName)}</code>。`;
    notice.classList.remove('hidden');
    if (result.failed > 0) {
      const failed = result.results.filter((item) => !item.success).map((item) => state.report.files.find((file) => file.index === item.index)?.displayName).filter(Boolean);
      showToast(`有 ${result.failed} 张未完成：${failed.join('、')}`, true, 8000);
    } else {
      showToast(`${result.succeeded} 张照片已安全修复，正在复查……`);
    }
    state.selected.clear();
	setBusy(false);
    await scanSelection({preserveNotice: true});
  } catch (error) {
    showToast(errorText(error), true, 8000);
  } finally {
    hideProgressSoon();
    setBusy(false);
    updateSelectedCount();
  }
}

let toastTimer;
function showToast(message, isError = false, duration = 4000) {
  const toast = el('toast');
  window.clearTimeout(toastTimer);
  toast.textContent = message;
  toast.className = `toast show${isError ? ' error' : ''}`;
  toastTimer = window.setTimeout(() => { toast.className = 'toast'; }, duration);
}

window.addEventListener('DOMContentLoaded', initialise);
