<script setup>
import { computed, nextTick, onMounted, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import { FRONTEND_VERSION, filterFiles, paginate, selectionSummary, debounce } from './results.mjs';
import Thumbnail from './Thumbnail.vue';

const api = () => window.go.main.GUIApp;
const info = ref(null);
const selection = ref(null);
const report = shallowRef(null);
const selected = ref(new Set());
const busy = ref('');
const cancelled = ref(false);
const filter = ref('all');
const format = ref('all');
const searchInput = ref('');
const search = ref('');
const page = ref(1);
const progress = ref({ done: 0, total: 0, message: '' });
const notice = ref('');
const failureDetails = ref([]);
const error = ref('');
const detail = shallowRef(null);
const dialog = ref(null);
let unsubscribe;
const updateSearch = debounce(value => { search.value = value; });
watch(searchInput, updateSearch);
watch([filter, format, search], () => { page.value = 1; });
const matches = computed(() => filterFiles(report.value?.files || [], filter.value, search.value)
  .filter(file => format.value === 'all' || (format.value === 'xmp' ? file.fileType === 'XMP' : file.fileType !== 'XMP')));
const pagination = computed(() => paginate(matches.value, page.value));
const pageCandidates = computed(() => pagination.value.items.filter(file => file.repairable));
const selectionCount = computed(() => selectionSummary(selected.value, pagination.value.items));
const allChecked = computed(() => pageCandidates.value.length > 0 && pageCandidates.value.every(file => selected.value.has(file.index)));
const someChecked = computed(() => !allChecked.value && pageCandidates.value.some(file => selected.value.has(file.index)));
const summary = computed(() => report.value?.summary || {});
const hasXMP = computed(() => report.value?.files.some(file => file.fileType === 'XMP'));
const percent = computed(() => progress.value.total ? Math.min(100, Math.round(progress.value.done / progress.value.total * 100)) : 0);
const cards = computed(() => [
  { id: 'all', label: '扫描文件', count: summary.value.total, note: '全部结果' },
  { id: 'candidate', label: '需要修复', count: summary.value.candidates, note: '已默认勾选' },
  { id: 'consistent', label: '时间一致', count: summary.value.consistent, note: '无需修改' },
  { id: 'attention', label: '需人工检查', count: (summary.value.ambiguous || 0) + (summary.value.unreadable || 0), note: '查看原因' },
]);
const gps = computed(() => detail.value?.gpsTimezoneReference);
const displayTime = value => (value || '—').replace(/^(\d{4}):(\d{2}):(\d{2})/, '$1-$2-$3');
const message = err => err?.message || String(err);

async function refreshInfo() {
  try { info.value = await api().GetAppInfo(); }
  catch (err) { error.value = message(err); }
}
onMounted(async () => {
  unsubscribe = window.runtime?.EventsOn('lrtimezonefix:progress', value => { progress.value = value; });
  await refreshInfo();
});
onBeforeUnmount(() => { updateSearch.cancel(); unsubscribe?.(); });

async function choose(method) {
  busy.value = 'choose'; error.value = '';
  try {
    const value = await api()[method]();
    if (!value?.mode) return;
    selection.value = value;
    report.value = null; selected.value = new Set(); notice.value = ''; failureDetails.value = [];
  } catch (err) { error.value = message(err); }
  finally { busy.value = ''; }
}
async function scan(preserveNotice = false) {
  busy.value = 'scan'; cancelled.value = false; error.value = '';
  report.value = null; selected.value = new Set();
  progress.value = { done: 0, total: 0, message: '正在查找 JPG、JPEG 和 XMP…' };
  if (!preserveNotice) { notice.value = ''; failureDetails.value = []; }
  try {
    report.value = await api().Scan(selection.value);
    selected.value = new Set(report.value.files.filter(file => file.repairable).map(file => file.index));
    filter.value = 'all'; page.value = 1;
  } catch (err) { error.value = message(err); }
  finally { busy.value = ''; }
}
async function cancelScan() {
  cancelled.value = true;
  try { await api().CancelScan(); }
  catch (err) { error.value = message(err); cancelled.value = false; }
}
function toggle(index, checked) {
  if (checked) selected.value.add(index); else selected.value.delete(index);
}
function selectPage(checked) { pageCandidates.value.forEach(file => toggle(file.index, checked)); }
async function openDetail(file) {
  detail.value = file;
  await nextTick();
  dialog.value.showModal();
}
function closeDetail() { dialog.value.close(); detail.value = null; }
async function reveal(file) {
  try { await api().RevealFile(report.value.sessionId, file.index); }
  catch (err) { error.value = message(err); }
}
async function repair() {
  busy.value = 'repair'; error.value = '';
  progress.value = { done: 0, total: selected.value.size, message: '等待确认修复…' };
  try {
    const result = await api().Repair({ sessionId: report.value.sessionId, indices: [...selected.value] });
    if (result.cancelled) { notice.value = '已取消修复，未修改文件。'; return; }
    notice.value = `修复完成：${result.succeeded} 个成功，${result.failed} 个失败。备份目录：${result.backupFolderName}。`;
    failureDetails.value = (result.results || []).filter(item => !item.success).map(item =>
      `${report.value.files.find(file => file.index === item.index)?.displayName}：${item.error}`);
    if (hasXMP.value && result.succeeded) notice.value += ' XMP 修复后，请在 Lightroom 中对对应照片执行“从文件读取元数据”，再检查一次导出结果。';
    await scan(true);
  } catch (err) { error.value = message(err); }
  finally { busy.value = ''; }
}
</script>

<template>
  <div class="app-shell">
    <header class="topbar">
      <div class="brand"><img src="/appicon.svg" alt=""><div><strong>LRTimezoneFix</strong><span>让拍摄时间回到正确时区</span></div></div>
      <div class="topbar-meta"><span class="version">v{{ info?.version || FRONTEND_VERSION }}</span><span class="tool-status" :class="{ ready: info?.exifToolReady }"><i></i>{{ !info ? '正在检查 ExifTool' : info.exifToolReady ? 'ExifTool 已就绪' : 'ExifTool 未就绪' }}</span></div>
    </header>
    <div class="layout">
      <aside class="sidebar">
        <div class="eyebrow">工作空间</div><h1>时区修复</h1><p class="muted">检查导出照片，或从 XMP 源头修正。</p>
        <ol class="steps"><li :class="{ current: !selection }"><b>1</b><span>选择文件<small>JPG · JPEG · XMP</small></span></li><li :class="{ current: selection && !report }"><b>2</b><span>扫描与检查<small>只读分析，不修改文件</small></span></li><li :class="{ current: report }"><b>3</b><span>确认修复<small>先备份，再写入与验证</small></span></li></ol>
        <div class="source-actions"><button id="folderButton" class="button secondary" :disabled="!!busy" @click="choose('ChooseFolder')"><span aria-hidden="true">▱</span>选择文件夹</button><button id="filesButton" class="button secondary" :disabled="!!busy" @click="choose('ChooseFiles')"><span aria-hidden="true">＋</span>选择文件</button></div>
        <div class="source-selection"><small>当前扫描范围</small><strong>{{ selection ? (selection.mode === 'folder' ? '文件夹及全部子目录' : `${selection.files?.length || 0} 个所选文件`) : '尚未选择文件' }}</strong><p :title="selection?.root">{{ selection?.label || selection?.root || '选择文件夹或多个文件后开始扫描。' }}</p></div>
        <button id="scanButton" class="button primary" :disabled="!!busy || !selection || !info?.exifToolReady" @click="scan()">{{ report ? '重新扫描' : '开始扫描' }}<span>→</span></button>
        <div class="sidebar-note"><span class="offline-dot"></span>本地处理 · 离线时区参考<small>原文件备份保存在各自所在目录。</small></div>
      </aside>
      <main class="workspace">
        <section v-if="info && !info.exifToolReady" class="notice warning" role="alert"><div><strong>请先安装 ExifTool 并加入 PATH</strong><p>{{ info.exifToolError }}</p></div><button class="button secondary" :disabled="!!busy" @click="refreshInfo">重新检测</button></section>
        <section v-if="error" class="notice error" role="alert"><span>{{ error }}</span><button aria-label="关闭提示" @click="error = ''">×</button></section>
        <section v-if="notice" id="repairNotice" class="notice success" role="status"><div>{{ notice }}<details v-if="failureDetails.length"><summary>查看失败详情</summary><p v-for="failure in failureDetails" :key="failure">{{ failure }}</p></details></div></section>
        <section v-if="busy === 'scan' || busy === 'repair'" class="progress-panel" role="status"><div><strong>{{ busy === 'repair' ? '正在修复' : progress.phase === 'analyze' ? '正在分析' : '正在扫描' }}</strong><span>{{ progress.message }}</span></div><progress :value="progress.phase === 'analyze' ? 100 : progress.total ? percent : undefined" max="100"></progress><span>{{ progress.phase === 'analyze' ? '读取完成' : progress.total ? `${percent}%` : '准备中' }}</span><button v-if="busy === 'scan'" id="cancelScanButton" class="button secondary" :disabled="cancelled" @click="cancelScan">{{ cancelled ? '正在终止…' : '终止扫描' }}</button></section>

        <template v-if="report">
          <div class="section-heading"><h2>扫描结果</h2><span class="subtle">{{ summary.total }} 个文件 · 扫描只读</span></div>
          <div class="summary-grid"><button v-for="card in cards" :key="card.id" class="summary-card" :class="[card.id, { active: filter === card.id }]" :aria-pressed="filter === card.id" @click="filter = card.id"><span>{{ card.label }}</span><strong>{{ card.count }}</strong><small>{{ card.note }} <span>↗</span></small></button></div>
          <section class="results-card" :aria-busy="!!busy">
            <div class="results-toolbar"><h3>文件明细 <span>{{ matches.length }}</span></h3><div class="filters"><input id="searchInput" v-model="searchInput" type="search" placeholder="搜索文件名或路径" aria-label="搜索文件名或路径"><select id="formatFilter" v-model="format" aria-label="文件类型"><option value="all">全部类型</option><option value="jpg">JPG / JPEG</option><option value="xmp">XMP 侧车</option></select><select id="stateFilter" v-model="filter" aria-label="筛选状态"><option value="all">全部状态</option><option value="candidate">需要修复</option><option value="consistent">时间一致</option><option value="attention">需人工检查</option></select></div></div>
            <div class="select-row"><label><input id="selectAllCandidates" type="checkbox" :checked="allChecked" :indeterminate="someChecked" :disabled="!!busy || !pageCandidates.length" @change="selectPage($event.target.checked)">选择本页可修复文件</label><span v-if="selectionCount.hidden" id="selectionHint">另有 {{ selectionCount.hidden }} 个已选文件不在本页</span><button id="clearSelectionButton" :disabled="!!busy || !selected.size" @click="selected.clear()">清除全部选择</button></div>
            <div class="table-wrap"><table><thead><tr><th></th><th>文件</th><th>当前拍摄时间</th><th>时区修正</th><th>状态</th><th></th></tr></thead><tbody id="resultsBody"><tr v-for="file in pagination.items" :key="file.index" :class="{ selected: selected.has(file.index) }"><td><input class="row-check" type="checkbox" :aria-label="`选择 ${file.displayName}`" :checked="selected.has(file.index)" :disabled="!!busy || !file.repairable" @change="toggle(file.index, $event.target.checked)"></td><td><div class="file-cell"><Thumbnail :key="`${report.sessionId}:${file.index}`" :session="report.sessionId" :file="file"/><div><button class="file-name" :title="file.path" @click="openDetail(file)">{{ file.displayName }}</button><small>{{ file.fileType === 'XMP' ? 'XMP 侧车文件' : 'JPEG 照片' }}</small></div></div></td><td class="date-cell">{{ displayTime(file.dateTimeOriginal) }}<small v-if="file.gpsTimezoneReference?.needsManualReview" class="gps-warning">⚠ 时区切换附近，请核对</small></td><td><div class="offsets"><span>{{ file.offsetTimeOriginal || '—' }}</span><template v-if="file.repairable"><span class="arrow">→</span><b>{{ file.targetOffset }}</b></template></div></td><td><span class="state-badge" :class="file.state">{{ file.repairable ? '需要修复' : file.state === 'consistent' ? '时间一致' : file.state === 'unreadable' ? '读取失败' : '需人工检查' }}</span></td><td><button class="reveal-button" title="在资源管理器中显示" :aria-label="`定位 ${file.displayName}`" @click="reveal(file)">↗</button></td></tr></tbody></table><div v-if="!matches.length" class="empty-results"><strong>没有符合条件的文件</strong><p>清空搜索词或切换筛选条件。</p></div></div>
            <nav class="pagination" aria-label="文件分页"><small>每页最多 100 个文件</small><button id="previousPageButton" :disabled="pagination.page <= 1" aria-label="上一页" @click="page--">←</button><span id="pageStatus">第 {{ pagination.page }} / {{ pagination.pageCount }} 页</span><button id="nextPageButton" :disabled="pagination.page >= pagination.pageCount" aria-label="下一页" @click="page++">→</button></nav>
          </section>
        </template>
        <section v-else-if="!busy" class="welcome"><div class="welcome-icon"><img src="/appicon.svg" alt=""></div><span class="eyebrow">JPG + XMP · 一次清晰的修正</span><h2>留住照片，校准时间。</h2><p>Lightroom 调整拍摄时间后，旧时区可能仍留在元数据中。<br>从左侧选择文件，我们会帮你找出差异，给出修复建议。</p><div class="format-cards"><article><span class="format-label">JPG</span><h3>修复已导出的照片</h3><p>同步拍摄时间、时区与关联字段，保留 JPEG 图像数据。</p></article><article><span class="format-label purple">XMP</span><h3>从侧车文件修正</h3><p>无需提供 RAW。修复后同步 Lightroom 目录，让后续导出使用正确时间。</p></article></div><div class="welcome-foot">01 选择文件 <span>→</span> 02 只读检查 <span>→</span> 03 备份并修复</div></section>
      </main>
    </div>
    <footer v-if="report" class="action-bar"><div><strong id="selectedCount">已选择 {{ selected.size }} 个文件</strong><span>跨页保留选择 · 修复前备份 · 写后验证</span></div><button id="repairButton" class="button primary" :disabled="!!busy || !selected.size" @click="repair">修复所选文件 <span>→</span></button></footer>
    <dialog id="detailDrawer" ref="dialog" :aria-hidden="!detail" aria-labelledby="detailName" @cancel="detail = null" @click="($event.target === dialog) && closeDetail()">
      <template v-if="detail"><div class="drawer-head"><div><span class="eyebrow">{{ detail.fileType === 'XMP' ? 'XMP 侧车' : 'JPEG 照片' }} · 元数据详情</span><h2 id="detailName">{{ detail.displayName }}</h2></div><button class="icon-button" aria-label="关闭详情" autofocus @click="closeDetail">×</button></div><div class="drawer-content"><p class="file-path">{{ detail.path }}</p><div class="detail-reason">{{ detail.reason || '拍摄与创建时间一致，无需修复。' }}</div><h3>时间对照</h3><dl><dt>{{ detail.fileType === 'XMP' ? 'XMP exif:DateTimeOriginal' : 'DateTimeOriginal' }}</dt><dd>{{ displayTime(detail.dateTimeOriginal) }}</dd><dt>{{ detail.fileType === 'XMP' ? 'XMP xmp:CreateDate' : 'CreateDate' }}</dt><dd>{{ displayTime(detail.createDate) }}</dd><template v-if="detail.fileType === 'XMP'"><dt>XMP photoshop:DateCreated</dt><dd>{{ displayTime(detail.xmpDateCreated) }}</dd></template><dt>原拍摄 / 创建时区</dt><dd>{{ detail.offsetTimeOriginal || '—' }} / {{ detail.offsetTimeDigitized || '—' }}</dd></dl><div v-if="detail.repairable" class="target-preview"><small>修复后三个 XMP 时间字段{{ detail.fileType === 'XMP' ? '' : '及 EXIF / IPTC' }}同步为</small><strong>{{ displayTime(detail.targetLocal) }}{{ detail.targetOffset }}</strong><span>墙上时间平移 {{ detail.shift }} · 按原创建时间推断偏移</span></div><p v-if="detail.fileType === 'XMP'" class="detail-tip">XMP 源头修正，无需 RAW 文件。只修改 DateTimeOriginal、CreateDate、DateCreated。修复后请在 Lightroom 中“从文件读取元数据”，避免目录中的旧值覆盖修正。</p><h3>GPS 地理时区参考</h3><template v-if="gps"><dl v-if="gps.timezone"><dt>坐标 / IANA 时区</dt><dd>{{ gps.coordinates }} / {{ gps.timezone }}</dd><dt>当地时间 / UTC 偏移</dt><dd>{{ gps.referenceTime }} / {{ gps.offset }}</dd><dt>夏令时 / 时间来源</dt><dd>{{ gps.dstLabel }} / {{ gps.dateSource }}</dd></dl><p v-if="gps.warning" class="gps-warning">{{ gps.warning }}</p><p class="muted">{{ gps.note }}</p></template><p class="muted">GPS 仅供人工参考，不参与修复推断。</p><button class="button secondary" @click="reveal(detail)">在资源管理器中显示 ↗</button></div></template>
    </dialog>
  </div>
</template>
