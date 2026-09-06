// Optional browser integration check: PLAYWRIGHT_MODULE may point to an existing
// Playwright installation. Requires Microsoft Edge; does not touch real photos.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');

(async () => {
  const root = path.resolve(__dirname, '../frontend/dist');
  const server = http.createServer((req, res) => {
    const file = path.join(root, req.url === '/' ? 'index.html' : req.url.split('?')[0]);
    res.setHeader('Content-Type', ({ '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.html': 'text/html' })[path.extname(file)] || 'application/octet-stream');
    fs.createReadStream(file).on('error', () => { res.statusCode = 404; res.end(); }).pipe(res);
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ channel: 'msedge', headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1240, height: 820 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.addInitScript(() => {
      const files = Array.from({ length: 10000 }, (_, index) => ({
        index, displayName: `Photo_${String(index).padStart(5, '0')}.${index % 10 === 0 ? 'xmp' : 'jpg'}`,
        fileType: index % 10 === 0 ? 'XMP' : 'JPG',
        path: `D:\\Photos\\Photo_${index}.jpg`, state: 'candidate',
        stateLabel: '需要修复', repairable: true, reason: '时区残留',
        dateTimeOriginal: '2025:10:01 13:39:15', createDate: '2025:10:01 12:39:15',
        offsetTimeOriginal: '+08:00', offsetTimeDigitized: '+08:00',
        targetLocal: '2025:10:01 13:39:15', targetOffset: '+09:00', shift: '+01:00'
      }));
      window.runtime = { EventsOn() {} };
      window.go = { main: { GUIApp: {
        GetAppInfo: async () => ({ version: '2.1.0', exifToolReady: true }),
        ChooseFolder: async () => ({ mode: 'folder', root: 'D:\\Photos' }),
        Scan: async () => ({ sessionId: 'smoke', files,
          summary: { total: files.length, candidates: files.length, consistent: 0, ambiguous: 0, unreadable: 0 } }),
        GetThumbnail: async (_, index) => { if (index % 10 === 0) throw new Error('XMP must not request thumbnails'); return ''; },
        Repair: async () => window.repairOutcome || ({ cancelled: true })
      } } };
    });
    await page.goto(`http://127.0.0.1:${server.address().port}/`);
    if (process.env.UI_SCREENSHOT_DIR) await page.screenshot({ path: path.join(process.env.UI_SCREENSHOT_DIR, 'welcome.png') });
    await page.getByRole('button', { name: '选择文件夹', exact: true }).click();
    await page.locator('#scanButton').click();
    await page.waitForFunction(() => document.querySelectorAll('#resultsBody tr').length === 100);
    assert.equal(await page.locator('#resultsBody tr').count(), 100);
    assert.match(await page.locator('#selectedCount').innerText(), /10000/);
    if (process.env.UI_SCREENSHOT_DIR) await page.screenshot({ path: path.join(process.env.UI_SCREENSHOT_DIR, 'results.png') });
    await page.locator('#formatFilter').selectOption('xmp');
    assert.equal(await page.locator('#resultsBody tr').count(), 100);
    assert.match(await page.locator('#pageStatus').innerText(), /10 页/);
    await page.locator('#formatFilter').selectOption('all');
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
    await page.evaluate(() => { window.repairOutcome = { succeeded: 1, failed: 1, backupFolderName: 'ExifTool_Backup_20260906_120000', results: [{ index: 0, success: true }, { index: 1, success: false, error: 'write failed; restored' }] }; });
    await page.locator('#repairButton').click();
    await page.waitForFunction(() => !document.querySelector('#repairButton').disabled);
    assert.match(await page.locator('#repairNotice').innerText(), /从文件读取元数据/);
    await page.locator('#repairNotice summary').click();
    assert.match(await page.locator('#repairNotice').innerText(), /write failed; restored/);
    await page.locator('#clearSelectionButton').click();
    assert.equal(await page.locator('#repairButton').isDisabled(), true);
    await page.emulateMedia({ colorScheme: 'dark' });
    await page.setViewportSize({ width: 980, height: 680 });
    await page.locator('#searchInput').fill('');
    await page.waitForFunction(() => document.querySelectorAll('#resultsBody tr').length === 100);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    if (process.env.UI_SCREENSHOT_DIR) await page.screenshot({ path: path.join(process.env.UI_SCREENSHOT_DIR, 'dark.png') });
    assert.deepEqual(errors, []);
    console.log('PASS: 10,000 photos, 100-row pagination, cross-page selection, search, drawer, repair cancellation; no browser errors.');
  } finally { await browser.close(); server.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
