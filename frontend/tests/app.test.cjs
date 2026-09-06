const assert = require('node:assert/strict');

global.window = {
  clearTimeout,
  setTimeout,
};

async function main() {
  const {
    FRONTEND_VERSION,
    PAGE_SIZE,
    THUMBNAIL_CACHE_LIMIT,
    cacheThumbnail,
    debounce,
    filterFiles,
    paginate,
    selectionSummary,
  } = await import('../src/results.mjs');

  function makeFiles(count) {
    return Array.from({length: count}, (_, index) => ({
      index,
      displayName: `IMG_${String(index).padStart(5, '0')}.JPG`,
      path: `D:/photos/${index % 2 ? 'travel' : 'family'}/IMG_${index}.JPG`,
      repairable: index % 3 === 0,
      state: index % 5 === 0 ? 'ambiguous' : index % 3 === 0 ? 'candidate' : 'consistent',
    }));
  }

  assert.equal(FRONTEND_VERSION, '2.1.0');
  assert.equal(PAGE_SIZE, 100);

  const files = makeFiles(10000);
  const firstPage = paginate(files, 1);
  const lastPage = paginate(files, 100);
  const clampedPage = paginate(files, 101);
  assert.equal(firstPage.items.length, 100, '每页最多渲染 100 条');
  assert.equal(lastPage.items.length, 100, '10000 条应有完整的第 100 页');
  assert.equal(lastPage.items[0].index, 9900);
  assert.equal(clampedPage.page, 100, '越界页码应钳制到最后一页');

  const candidates = filterFiles(files, 'candidate');
  assert.ok(candidates.length > 0);
  assert.ok(candidates.every((file) => file.repairable));
  const nameMatches = filterFiles(files, 'all', 'img_00001');
  assert.deepEqual(nameMatches.map((file) => file.index), [1]);
  const attentionMatches = filterFiles(files, 'attention');
  assert.ok(attentionMatches.every((file) => file.state === 'ambiguous' || file.state === 'unreadable'));

  const crossPage = selectionSummary(new Set([1, 101, 202]), [{index: 1}, {index: 2}]);
  assert.deepEqual(crossPage, {total: 3, onPage: 1, hidden: 2}, '跨页选择应显示隐藏选择数量');
  const visibleSelection = selectionSummary([1, 2], [{index: 1}, {index: 2}]);
  assert.deepEqual(visibleSelection, {total: 2, onPage: 2, hidden: 0});

  const thumbnailCache = new Map();
  for (let index = 0; index < THUMBNAIL_CACHE_LIMIT; index += 1) {
    cacheThumbnail(thumbnailCache, `session-a:${index}`, `data-${index}`);
  }
  assert.equal(thumbnailCache.size, 300, '缩略图缓存上限应为 300 条');
  cacheThumbnail(thumbnailCache, 'session-new:301', 'new-data');
  assert.equal(thumbnailCache.size, 300);
  assert.equal(thumbnailCache.has('session-a:0'), false, '超出上限时应淘汰最早的缓存项');

  let calls = [];
  const debounced = debounce((value) => calls.push(value), 20);
  debounced('a');
  debounced('ab');
  debounced('abc');
  await new Promise((resolve) => setTimeout(resolve, 45));
  assert.deepEqual(calls, ['abc'], '连续搜索输入应只触发最后一次筛选');
  debounced.cancel();

  console.log('frontend unit tests passed');
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
