const FRONTEND_VERSION = '2.0.0';
const PAGE_SIZE=100;
const SEARCH_DEBOUNCE_MS=180;
const THUMBNAIL_CACHE_LIMIT=300;
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

function cacheThumbnail(cache, key, data) {
  if (!cache.has(key) && cache.size >= THUMBNAIL_CACHE_LIMIT) {
    cache.delete(cache.keys().next().value);
  }
  cache.set(key, data || '');
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

export { FRONTEND_VERSION, PAGE_SIZE, THUMBNAIL_CACHE_LIMIT, cacheThumbnail, debounce, fileMatchesFilter, filterFiles, paginate, selectionSummary };
