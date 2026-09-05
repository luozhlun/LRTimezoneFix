const api = () => window.go.main.GUIApp;

const FRONTEND_VERSION = '1.6.0';
const PAGE_SIZE = 100;
const SEARCH_DEBOUNCE_MS = 180;
const THUMBNAIL_CONCURRENCY = 2;
const THUMBNAIL_CACHE_LIMIT = 300;

const state = {
  selection: null,
  report: null,
  selected: new Set(),
  busy: false,
  cancelRequested: false,
  filter: 'all',
  search: '',
  page: 1,
  thumbnailCache: new Map(),
  thumbnailGeneration: 0,
  thumbnailQueue: [],
  thumbnailActive: 0,
  operationId: 0,
  progressPhase: '',
  progressRenderToken: 0,
  progressHideToken: 0,
  progressHideTimer: 0,
  exifChecking: false,
  drawerPreviousFocus: null,
};

let thumbnailObserver;
let searchDebouncer;

const el = (id) => document.getElementById(id);
const escapeHTML = (value = '') => String(value).replace(/[&<>'"]/g, (char) => ({
  '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;'
}[char]));

function errorText(error) {
  if (!error) return '发生未知错误';
  return error.message || String(error);
}

function fileMatchesFilter(file, filter = 'all', search = '') {
  const query = String(search || '').trim().toLowerCase();
  const nameAndPath = `${file?.displayName || ''} ${file?.path || ''}`.toLowerCase();
  if (query && !nameAndPath.includes(query)) return false;
  if (filter === 'candidate') return Boolean(file?.repairable);
  if (filter === 'consistent') return file?.state === 'consistent';
  if (filter === 'attention') return file?.state === 'ambiguous' || file?.state === 'unreadable';
  return true;
}

function filterFiles(files = [], filter = 'all', search = '') {
  return files.filter((file) => fileMatchesFilter(file, filter, search));
}

function paginate(items = [], page = 1, pageSize = PAGE_SIZE) {
  const safePageSize = Math.max(1, Number(pageSize) || PAGE_SIZE);
  const pageCount = Math.max(1, Math.ceil(items.length / safePageSize));
  const safePage = Math.min(pageCount, Math.max(1, Number(page) || 1));
  const start = (safePage - 1) * safePageSize;
  return {
    page: safePage,
    pageCount,
    items: items.slice(start, start + safePageSize),
  };
}

function selectionSummary(selected, pageFiles = []) {
  const selectedSet = selected instanceof Set ? selected : new Set(selected || []);
  const pageIndices = new Set(pageFiles.map((file) => file.index));
  let onPage = 0;
  selectedSet.forEach((index) => {
    if (pageIndices.has(index)) onPage += 1;
  });
  return {
    total: selectedSet.size,
    onPage,
    hidden: Math.max(0, selectedSet.size - onPage),
  };
}

function isThumbnailTaskCurrent(task, current) {
  return Boolean(
    task && current &&
    task.generation === current.generation &&
    task.sessionId === current.sessionId &&
    task.index === current.index &&
    current.connected
  );
}

function cacheThumbnail(cache, key, data) {
  if (!cache.has(key) && cache.size >= THUMBNAIL_CACHE_LIMIT) {
    cache.delete(cache.keys().next().value);
  }
  cache.set(key, data || '');
}

function cacheThumbnailForSession(cache, key, data, taskSessionId, currentSessionId) {
  if (!taskSessionId || taskSessionId !== currentSessionId) return false;
  cacheThumbnail(cache, key, data);
  return true;
}

function debounce(callback, delay = SEARCH_DEBOUNCE_MS) {
  let timer = 0;
  const debounced = (...args) => {
    window.clearTimeout(timer);
    timer = window.setTimeout(() => callback(...args), delay);
  };
  debounced.cancel = () => {
    window.clearTimeout(timer);
    timer = 0;
  };
  return debounced;
}

function setBusy(busy) {
  state.busy = Boolean(busy);
  ['folderButton', 'filesButton', 'scanButton', 'repairButton'].forEach((id) => {
    const node = el(id);
    if (!node) return;
    node.disabled = state.busy ||
      (id === 'scanButton' && !state.selection) ||
      (id === 'repairButton' && state.selected.size === 0);
  });

  const selectionBox = el('selectAllCandidates');
  if (selectionBox) selectionBox.disabled = state.busy || selectionBox.dataset.hasCandidates !== 'true';
  const clearSelectionButton = el('clearSelectionButton');
  if (clearSelectionButton) clearSelectionButton.disabled = state.busy || state.selected.size === 0;
  document.querySelectorAll('#resultsBody .row-check').forEach((node) => { node.disabled = state.busy; });
  el('resultsSection')?.setAttribute('aria-busy', String(state.busy));
}

function updateWorkflow() {
  const stage = state.report ? 3 : state.selection ? 2 : 1;
  document.querySelectorAll('.workflow-step').forEach((step, index) => {
    const number = index + 1;
    step.classList.toggle('active', number === stage);
    step.classList.toggle('completed', number < stage);
  });
}

async function initialise() {
  bindEvents();
  updateWorkflow();
  if (window.runtime?.EventsOn) {
    window.runtime.EventsOn('lrtimezonefix:progress', updateProgress);
  }
  await refreshAppInfo(true);
}

function setExifToolStatus(info, announce = false) {
  const status = el('exifStatus');
  const notice = el('exifToolNotice');
  const message = el('exifToolMessage');
  if (!status || !notice || !message) return;

  if (info?.exifToolReady) {
    status.className = 'status-pill ready';
    status.innerHTML = '<span class="status-dot"></span>ExifTool 已就绪';
    status.title = info.exifToolPath || '';
    notice.classList.add('hidden');
    return;
  }

  const reason = info?.exifToolError || '未找到 ExifTool，请确认它已安装并位于应用可访问的位置。';
  status.className = 'status-pill error';
  status.innerHTML = '<span class="status-dot"></span>未找到 ExifTool';
  status.title = reason;
  message.textContent = reason;
  notice.classList.remove('hidden');
  if (announce) showToast(reason, true, 7000);
}

async function refreshAppInfo(announce = false) {
  if (state.exifChecking) return;
  state.exifChecking = true;
  const retryButton = el('retryExifButton');
  if (retryButton) {
    retryButton.disabled = true;
    retryButton.textContent = '检查中…';
  }
  try {
    const info = await api().GetAppInfo();
    el('versionBadge').textContent = `v${info?.version || FRONTEND_VERSION}`;
    setExifToolStatus(info, announce);
  } catch (error) {
    const message = errorText(error);
    setExifToolStatus({exifToolReady: false, exifToolError: message}, announce);
    if (!announce) showToast(message, true, 6500);
  } finally {
    state.exifChecking = false;
    if (retryButton) {
      retryButton.disabled = false;
      retryButton.textContent = '重试检查';
    }
  }
}

function bindEvents() {
  el('folderButton').addEventListener('click', () => choose('folder'));
  el('filesButton').addEventListener('click', () => choose('files'));
  el('scanButton').addEventListener('click', () => scanSelection());
  el('cancelScanButton').addEventListener('click', cancelScan);
  el('repairButton').addEventListener('click', repairSelected);
  el('retryExifButton').addEventListener('click', () => refreshAppInfo(true));

  searchDebouncer = debounce((value) => {
    state.search = String(value || '').trim().toLowerCase();
    state.page = 1;
    renderRows();
  });
  el('searchInput').addEventListener('input', (event) => searchDebouncer(event.target.value));
  el('stateFilter').addEventListener('change', (event) => setFilter(event.target.value));
  el('selectAllCandidates').addEventListener('change', toggleVisibleCandidates);
  el('clearSelectionButton').addEventListener('click', clearSelection);
  el('previousPageButton').addEventListener('click', () => changePage(-1));
  el('nextPageButton').addEventListener('click', () => changePage(1));
  document.querySelectorAll('.quick-filter').forEach((button) => {
    button.addEventListener('click', () => setFilter(button.dataset.filter || 'all'));
  });
  el('closeDrawer').addEventListener('click', closeDrawer);
  el('drawerBackdrop').addEventListener('click', closeDrawer);
  document.addEventListener('keydown', handleDocumentKeydown);
}

function setFilter(filter) {
  state.filter = filter || 'all';
  state.page = 1;
  const select = el('stateFilter');
  if (select) select.value = state.filter;
  document.querySelectorAll('.quick-filter').forEach((button) => {
    const active = button.dataset.filter === state.filter;
    button.classList.toggle('active', active);
    button.setAttribute('aria-pressed', String(active));
  });
  renderRows();
}

async function choose(mode) {
  if (state.busy) return;
  try {
    const selection = mode === 'folder' ? await api().ChooseFolder() : await api().ChooseFiles();
    if (!selection?.mode) return;
    state.selection = selection;
    state.report = null;
    state.selected.clear();
    state.filter = 'all';
    state.search = '';
    state.page = 1;
    searchDebouncer?.cancel();
    invalidateThumbnailWork(true);

    const files = Array.isArray(selection.files) ? selection.files : [];
    const selectionSummaryText = files.length > 3
      ? `${files.slice(0, 3).join('  ·  ')}  等 ${files.length} 个文件`
      : files.join('  ·  ');
    el('selectionPanel').classList.remove('empty');
    el('selectionTitle').textContent = selection.mode === 'folder' ? '递归扫描文件夹' : (selection.label || `已选择 ${files.length} 张照片`);
    el('selectionPath').textContent = selection.mode === 'folder' ? selection.root : selectionSummaryText;
    el('resultsSection').classList.add('hidden');
    el('actionBar').classList.add('hidden');
    el('repairNotice').classList.add('hidden');
    el('searchInput').value = '';
    el('stateFilter').value = 'all';
    updateWorkflow();
    updateSelectedCount();
    closeDrawer();
  } catch (error) {
    handleOperationError(error);
  }
}

async function scanSelection(options = {}) {
  if (!state.selection || (state.busy && !options.allowBusy)) return;
  const operationId = ++state.operationId;
  state.cancelRequested = false;
  setBusy(true);
  showProgress('scan', 0, 1, '正在准备扫描……');
  if (!options.preserveNotice) el('repairNotice').classList.add('hidden');
  try {
    const report = await api().Scan(state.selection);
    if (!report?.files || !report.summary) throw new Error('扫描结果格式无效');
    state.report = report;
    state.selected = new Set(report.files.filter((file) => file.repairable).map((file) => file.index));
    state.page = 1;
    renderReport();
    showToast(report.summary.candidates > 0 ? `发现 ${report.summary.candidates} 张需要修复的照片` : '扫描完成，没有发现需要修复的照片');
  } catch (error) {
    if (state.cancelRequested) {
      showToast('扫描已终止；没有修改任何文件');
    } else {
      handleOperationError(error, 6500);
    }
  } finally {
    setBusy(false);
    hideProgressSoon(operationId);
    state.cancelRequested = false;
  }
}

async function cancelScan() {
  if (!state.busy || state.cancelRequested || state.progressPhase !== 'scan') return;
  state.cancelRequested = true;
  el('cancelScanButton').disabled = true;
  el('progressMessage').textContent = '正在安全终止当前扫描……';
  try {
    const accepted = await api().CancelScan();
    if (!accepted) {
      state.cancelRequested = false;
      el('cancelScanButton').disabled = false;
    }
  } catch (error) {
    state.cancelRequested = false;
    el('cancelScanButton').disabled = false;
    handleOperationError(error);
  }
}

function updateProgress(progress) {
  if (!progress || !state.busy) return;
  showProgress(progress.phase, progress.done, progress.total, progress.message);
}

function showProgress(phase, done, total, message) {
  window.clearTimeout(state.progressHideTimer);
  state.progressHideTimer = 0;
  state.progressPhase = phase || 'scan';
  state.progressRenderToken += 1;
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

function hideProgressSoon(operationId = state.operationId) {
  if (operationId !== state.operationId) return;
  window.clearTimeout(state.progressHideTimer);
  const hideToken = ++state.progressHideToken;
  const renderToken = state.progressRenderToken;
  state.progressHideTimer = window.setTimeout(() => {
    if (
      hideToken !== state.progressHideToken ||
      renderToken !== state.progressRenderToken ||
      state.busy ||
      operationId !== state.operationId
    ) return;
    el('progressPanel').classList.add('hidden');
    el('cancelScanButton').classList.add('hidden');
    state.progressPhase = '';
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
  state.page = 1;
  searchDebouncer?.cancel();
  el('stateFilter').value = state.filter;
  el('searchInput').value = '';
  state.search = '';
  document.querySelectorAll('.quick-filter').forEach((button) => {
    const active = button.dataset.filter === state.filter;
    button.classList.toggle('active', active);
    button.setAttribute('aria-pressed', String(active));
  });
  updateWorkflow();
  renderRows();
  updateSelectedCount();
}

function filteredFiles() {
  return state.report ? filterFiles(state.report.files, state.filter, state.search) : [];
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

function currentPageFiles() {
  return paginate(filteredFiles(), state.page, PAGE_SIZE).items;
}

function renderRows() {
  invalidateThumbnailWork(false);
  const matches = filteredFiles();
  const pageInfo = paginate(matches, state.page, PAGE_SIZE);
  state.page = pageInfo.page;
  const files = pageInfo.items;
  const visibleCount = el('visibleCount');
  visibleCount.textContent = matches.length ? `匹配 ${matches.length} 张 · 本页 ${files.length} 张` : '匹配 0 张';

  const emptyResults = el('emptyResults');
  emptyResults.classList.toggle('hidden', matches.length !== 0);
  el('resultsBody').innerHTML = files.map((file) => {
    const checked = state.selected.has(file.index) ? 'checked' : '';
    const disabled = state.busy ? ' disabled' : '';
    const checkbox = file.repairable
      ? `<input class="row-check" type="checkbox" data-index="${file.index}" ${checked}${disabled} aria-label="选择 ${escapeHTML(file.displayName)}">`
      : '';
    const time = file.dateTimeOriginal || '—';
    const target = file.repairable ? `${file.targetLocal || ''}${file.targetOffset || ''}` : '—';
    const oldOffset = file.offsetTimeOriginal || '—';
    const offset = file.repairable
      ? `<span class="mono">${escapeHTML(oldOffset)}</span><span class="offset-arrow">→</span><span class="mono">${escapeHTML(file.targetOffset || '—')}</span>`
      : `<span class="mono">${escapeHTML(oldOffset)}</span>`;
    const displayName = file.displayName || file.path || `文件 ${file.index}`;
    return `<tr data-index="${file.index}" tabindex="0" role="button" aria-label="查看 ${escapeHTML(displayName)} 的详情">
      <td class="check-col">${checkbox}</td>
      <td class="thumb-col"><div class="photo-thumb"><span>JPG</span><img class="thumb-image" data-index="${file.index}" alt="" decoding="async"></div></td>
      <td class="file-cell"><div class="file-name-row"><div class="file-name" title="${escapeHTML(displayName)}">${escapeHTML(displayName)}</div>${renderGPSWarningBadge(file)}</div><div class="file-path">${escapeHTML(file.reason || file.path || '')}</div></td>
      <td class="reveal-col"><button class="reveal-button" type="button" data-index="${file.index}" title="在资源管理器中显示" aria-label="在资源管理器中显示 ${escapeHTML(displayName)}"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3.5 6.5h6l2 2h9v9.5a2 2 0 0 1-2 2h-15a2 2 0 0 1-2-2V8.5a2 2 0 0 1 2-2Z"/><path d="M2 10h20"/></svg></button></td>
      <td class="mono">${escapeHTML(time)}</td>
      <td>${offset}</td>
      <td class="mono">${escapeHTML(target)}</td>
      <td><span class="state-chip ${escapeHTML(file.state || '')}">${escapeHTML(file.stateLabel || '')}</span></td>
    </tr>`;
  }).join('');

  const rows = el('resultsBody').querySelectorAll('tr');
  rows.forEach((row) => {
    const index = Number(row.dataset.index);
    row.addEventListener('click', (event) => {
      if (event.target.closest('button, input, a, select, textarea')) return;
      openDrawer(index);
    });
    row.addEventListener('keydown', (event) => {
      if (event.key !== 'Enter' && event.key !== ' ') return;
      if (event.target !== row) return;
      event.preventDefault();
      openDrawer(index);
    });
  });

  el('resultsBody').querySelectorAll('.row-check').forEach((box) => {
    box.addEventListener('click', (event) => event.stopPropagation());
    box.addEventListener('change', (event) => {
      event.stopPropagation();
      if (state.busy) return;
      const index = Number(event.target.dataset.index);
      event.target.checked ? state.selected.add(index) : state.selected.delete(index);
      updateSelectedCount();
    });
  });
  el('resultsBody').querySelectorAll('.reveal-button').forEach((button) => button.addEventListener('click', (event) => {
    event.stopPropagation();
    revealFile(Number(button.dataset.index));
  }));

  updatePagination(matches, pageInfo);
  updateSelectionDisplay(files);
  observeThumbnails();
}

function updatePagination(matches, pageInfo) {
  const pagination = el('pagination');
  const hasPages = pageInfo.pageCount > 1;
  pagination.classList.toggle('hidden', !hasPages);
  el('pageStatus').textContent = `第 ${pageInfo.page} / ${pageInfo.pageCount} 页`;
  el('previousPageButton').disabled = pageInfo.page <= 1;
  el('nextPageButton').disabled = pageInfo.page >= pageInfo.pageCount;
  pagination.setAttribute('aria-label', `${matches.length} 张照片，第 ${pageInfo.page} / ${pageInfo.pageCount} 页`);
}

function changePage(delta) {
  const pageInfo = paginate(filteredFiles(), state.page + delta, PAGE_SIZE);
  if (pageInfo.page === state.page) return;
  state.page = pageInfo.page;
  renderRows();
  el('tableWrap').scrollTop = 0;
}

function prepareThumbnailRender() {
  state.thumbnailGeneration += 1;
  state.thumbnailQueue.length = 0;
  thumbnailObserver?.disconnect();
  thumbnailObserver = null;
}

function invalidateThumbnailWork(clearCache = false) {
  prepareThumbnailRender();
  if (clearCache) state.thumbnailCache.clear();
}

function observeThumbnails() {
  const images = [...el('resultsBody').querySelectorAll('.thumb-image')];
  if (!('IntersectionObserver' in window)) {
    images.forEach(enqueueThumbnail);
    return;
  }
  thumbnailObserver = new IntersectionObserver((entries) => {
    entries.filter((entry) => entry.isIntersecting).forEach((entry) => {
      thumbnailObserver.unobserve(entry.target);
      enqueueThumbnail(entry.target);
    });
  }, {root: el('tableWrap'), rootMargin: '160px 0px'});
  images.forEach((image) => thumbnailObserver.observe(image));
}

function enqueueThumbnail(image) {
  if (!state.report || !image || image.dataset.loading === 'true' || image.dataset.queued === 'true') return;
  const task = {
    image,
    sessionId: state.report.sessionId,
    index: Number(image.dataset.index),
    generation: state.thumbnailGeneration,
  };
  image.dataset.queued = 'true';
  state.thumbnailQueue.push(task);
  pumpThumbnailQueue();
}

function pumpThumbnailQueue() {
  while (state.thumbnailActive < THUMBNAIL_CONCURRENCY && state.thumbnailQueue.length > 0) {
    const task = state.thumbnailQueue.shift();
    if (task.image?.dataset) delete task.image.dataset.queued;
    state.thumbnailActive += 1;
    loadThumbnailTask(task)
      .catch(() => {})
      .finally(() => {
        state.thumbnailActive = Math.max(0, state.thumbnailActive - 1);
        pumpThumbnailQueue();
      });
  }
}

async function loadThumbnailTask(task) {
  const current = () => ({
    generation: state.thumbnailGeneration,
    sessionId: state.report?.sessionId,
    index: task.index,
    connected: Boolean(task.image?.isConnected),
  });
  if (!isThumbnailTaskCurrent(task, current())) return;

  const image = task.image;
  image.dataset.loading = 'true';
  const key = `${task.sessionId}:${task.index}`;
  try {
    let data;
    if (state.thumbnailCache.has(key)) {
      data = state.thumbnailCache.get(key);
    } else {
      data = await api().GetThumbnail(task.sessionId, task.index);
      cacheThumbnailForSession(state.thumbnailCache, key, data, task.sessionId, state.report?.sessionId);
    }
    if (!isThumbnailTaskCurrent(task, current()) || !data) return;
    image.addEventListener('load', () => image.closest('.photo-thumb')?.classList.add('loaded'), {once: true});
    image.src = data;
  } finally {
    delete image.dataset.loading;
  }
}

async function revealFile(index) {
  if (!state.report) return;
  try {
    await api().RevealFile(state.report.sessionId, index);
  } catch (error) {
    handleOperationError(error);
  }
}

function updateSelectAllState(files = currentPageFiles()) {
  const repairable = files.filter((file) => file.repairable);
  const selected = repairable.filter((file) => state.selected.has(file.index)).length;
  const box = el('selectAllCandidates');
  box.dataset.hasCandidates = repairable.length > 0 ? 'true' : 'false';
  box.disabled = state.busy || repairable.length === 0;
  box.checked = repairable.length > 0 && selected === repairable.length;
  box.indeterminate = selected > 0 && selected < repairable.length;
}

function updateSelectionHint(files = currentPageFiles()) {
  const summary = selectionSummary(state.selected, files);
  const hint = el('selectionHint');
  hint.classList.toggle('hidden', summary.total === 0);
  hint.classList.toggle('cross-page', summary.hidden > 0);
  if (summary.total > 0) {
    hint.textContent = summary.hidden > 0
      ? `已选 ${summary.total} 张 · 本页 ${summary.onPage} 张，另有 ${summary.hidden} 张在其他页或筛选之外`
      : `已选 ${summary.total} 张 · 当前页已显示全部选择`;
  }
  const clearButton = el('clearSelectionButton');
  clearButton.disabled = state.busy || summary.total === 0;
}

function updateSelectionDisplay(files = currentPageFiles()) {
  updateSelectAllState(files);
  updateSelectionHint(files);
  const count = state.selected.size;
  el('selectedCount').textContent = `已选择 ${count} 张`;
  el('repairButton').disabled = state.busy || count === 0;
}

function toggleVisibleCandidates(event) {
  if (state.busy) return;
  const pageFiles = currentPageFiles().filter((file) => file.repairable);
  pageFiles.forEach((file) => {
    event.target.checked ? state.selected.add(file.index) : state.selected.delete(file.index);
  });
  el('resultsBody').querySelectorAll('.row-check').forEach((box) => {
    box.checked = state.selected.has(Number(box.dataset.index));
  });
  updateSelectionDisplay(currentPageFiles());
}

function clearSelection() {
  if (state.busy) return;
  state.selected.clear();
  updateSelectionDisplay(currentPageFiles());
  el('selectAllCandidates').checked = false;
  el('selectAllCandidates').indeterminate = false;
  el('resultsBody').querySelectorAll('.row-check').forEach((box) => { box.checked = false; });
}

function updateSelectedCount() {
  updateSelectionDisplay(currentPageFiles());
}

function openDrawer(index) {
  if (!state.report) return;
  const file = state.report.files.find((item) => item.index === index);
  if (!file) return;
  if (!el('detailDrawer').classList.contains('open')) state.drawerPreviousFocus = document.activeElement;
  el('detailName').textContent = file.displayName || file.path || '照片';
  const target = file.repairable ? `${file.targetLocal || ''}${file.targetOffset || ''}` : '不自动修改';
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
  window.requestAnimationFrame?.(() => el('closeDrawer').focus({preventScroll: true}));
}

function closeDrawer() {
  const drawer = el('detailDrawer');
  if (!drawer.classList.contains('open')) return;
  drawer.classList.remove('open');
  drawer.setAttribute('aria-hidden', 'true');
  const previous = state.drawerPreviousFocus;
  state.drawerPreviousFocus = null;
  if (previous?.isConnected && !previous.disabled) {
    window.requestAnimationFrame?.(() => previous.focus({preventScroll: true}));
  }
}

function handleDocumentKeydown(event) {
  const drawer = el('detailDrawer');
  if (!drawer?.classList.contains('open')) return;
  if (event.key === 'Escape') {
    event.preventDefault();
    closeDrawer();
    return;
  }
  if (event.key !== 'Tab') return;
  const panel = drawer.querySelector('.drawer-panel');
  const focusable = [...panel.querySelectorAll('button, input, select, textarea, a[href], [tabindex]:not([tabindex="-1"])')]
    .filter((node) => !node.disabled && node.offsetParent !== null);
  if (focusable.length === 0) {
    event.preventDefault();
    panel.focus();
    return;
  }
  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault();
    first.focus();
  }
}

async function repairSelected() {
  if (!state.report || state.busy || state.selected.size === 0) return;
  const operationId = ++state.operationId;
  const indices = [...state.selected];
  setBusy(true);
  showProgress('repair', 0, indices.length, '正在等待安全修复……');
  try {
    const result = await api().Repair({sessionId: state.report.sessionId, indices});
    if (result.cancelled) {
      showToast('已取消，没有修改照片');
      return;
    }
    const notice = el('repairNotice');
    notice.innerHTML = `<strong>修复完成：</strong>${result.succeeded} 张成功，${result.failed} 张失败并尝试恢复。备份目录名称为 <code>${escapeHTML(result.backupFolderName)}</code>。`;
    notice.classList.remove('hidden');
    if (result.failed > 0) {
      const failed = (result.results || []).filter((item) => !item.success).map((item) => state.report.files.find((file) => file.index === item.index)?.displayName).filter(Boolean);
      showToast(`有 ${result.failed} 张未完成：${failed.join('、')}`, true, 8000);
    } else {
      showToast(`${result.succeeded} 张照片已安全修复，正在复查……`);
    }
    state.selected.clear();
    await scanSelection({preserveNotice: true, allowBusy: true});
  } catch (error) {
    handleOperationError(error, 8000);
  } finally {
    setBusy(false);
    hideProgressSoon(operationId);
    updateSelectedCount();
  }
}

function handleOperationError(error, duration = 4000) {
  const message = errorText(error);
  if (/exiftool/i.test(message)) setExifToolStatus({exifToolReady: false, exifToolError: message}, false);
  showToast(message, true, duration);
}

let toastTimer;
function showToast(message, isError = false, duration = 4000) {
  const toast = el('toast');
  window.clearTimeout(toastTimer);
  toast.textContent = message;
  toast.className = `toast show${isError ? ' error' : ''}`;
  toastTimer = window.setTimeout(() => { toast.className = 'toast'; }, duration);
}

if (typeof module !== 'undefined' && module.exports) {
  module.exports = {
    FRONTEND_VERSION,
    PAGE_SIZE,
    THUMBNAIL_CACHE_LIMIT,
    cacheThumbnail,
    cacheThumbnailForSession,
    debounce,
    fileMatchesFilter,
    filterFiles,
    isThumbnailTaskCurrent,
    paginate,
    selectionSummary,
  };
}

if (typeof window !== 'undefined' && window.addEventListener) {
  window.addEventListener('DOMContentLoaded', initialise);
}
