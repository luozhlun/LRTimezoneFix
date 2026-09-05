// Optional browser integration check: PLAYWRIGHT_MODULE may point to an existing
// Playwright installation. Requires Microsoft Edge; does not touch real photos.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const { pathToFileURL } = require('node:url');
const path = require('node:path');

(async () => {
  const browser = await chromium.launch({ channel: 'msedge', headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1240, height: 820 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.addInitScript(() => {
      const files = Array.from({ length: 10000 }, (_, index) => ({
        index, displayName: `Photo_${String(index).padStart(5, '0')}.jpg`,
        path: `D:\\Photos\\Photo_${index}.jpg`, state: 'candidate',
        stateLabel: '需要修复', repairable: true, reason: '时区残留',
        dateTimeOriginal: '2025:10:01 13:39:15', createDate: '2025:10:01 12:39:15',
        offsetTimeOriginal: '+08:00', offsetTimeDigitized: '+08:00',
        targetLocal: '2025:10:01 13:39:15', targetOffset: '+09:00', shift: '+01:00'
      }));
      window.runtime = { EventsOn() {} };
      window.go = { main: { GUIApp: {
        GetAppInfo: async () => ({ version: '1.6.0', exifToolReady: true }),
        ChooseFolder: async () => ({ mode: 'folder', root: 'D:\\Photos' }),
        Scan: async () => ({ sessionId: 'smoke', files,
          summary: { total: files.length, candidates: files.length, consistent: 0, ambiguous: 0, unreadable: 0 } }),
        GetThumbnail: async () => '', Repair: async () => ({ cancelled: true })
      } } };
    });
    await page.goto(pathToFileURL(path.resolve(__dirname, '../frontend/dist/index.html')).href);
    await page.getByRole('button', { name: '选择文件夹', exact: true }).click();
    await page.locator('#scanButton').click();
    await page.waitForFunction(() => document.querySelectorAll('#resultsBody tr').length === 100);
    assert.equal(await page.locator('#resultsBody tr').count(), 100);
    assert.match(await page.locator('#selectedCount').innerText(), /10000/);
    await page.locator('#nextPageButton').click();
    assert.match(await page.locator('#resultsBody').innerText(), /Photo_00100/);
    await page.locator('#selectAllCandidates').uncheck();
    assert.match(await page.locator('#selectedCount').innerText(), /9900/);
    await page.locator('#previousPageButton').click();
    assert.equal(await page.locator('.row-check:checked').count(), 100);
    await page.locator('#searchInput').fill('Photo_09999');
    await page.waitForFunction(() => document.querySelectorAll('#resultsBody tr').length === 1);
    assert.match(await page.locator('#resultsBody').innerText(), /Photo_09999/);
    await page.locator('#resultsBody tr .file-name').click();
    assert.equal(await page.locator('#detailDrawer').getAttribute('aria-hidden'), 'false');
    await page.keyboard.press('Escape');
    assert.equal(await page.locator('#detailDrawer').getAttribute('aria-hidden'), 'true');
    await page.locator('#repairButton').click();
    await page.waitForFunction(() => !document.querySelector('#repairButton').disabled);
    assert.match(await page.locator('#selectedCount').innerText(), /9900/);
    await page.locator('#clearSelectionButton').click();
    assert.equal(await page.locator('#repairButton').isDisabled(), true);
    assert.deepEqual(errors, []);
    console.log('PASS: 10,000 photos, 100-row pagination, cross-page selection, search, drawer, repair cancellation; no browser errors.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
