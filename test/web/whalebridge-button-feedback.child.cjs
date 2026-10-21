'use strict';
const { app, BrowserWindow, nativeTheme } = require('electron');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
app.disableHardwareAcceleration();
app.setPath('userData', process.argv[2]);
const assets = path.resolve(__dirname, '../../internal/whalebridge/assets');
const provider = { id: 'qa', name: 'QA supplier', models: ['qa-model'], modelCount: 1 };
let heldPath, releaseRequest, requestCount = 0, agentValue = 'model-a';
const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, 'http://localhost');
  const json = value => { res.writeHead(200, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(value)); };
  if (!url.pathname.startsWith('/api/')) {
    const name = url.pathname === '/' ? 'index.html' : url.pathname.slice(1);
    if (!fs.readdirSync(assets).includes(name)) { res.writeHead(404); res.end(); return; }
    res.setHeader('Content-Type', { '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.html': 'text/html' }[path.extname(name)] || 'application/octet-stream');
    res.end(fs.readFileSync(path.join(assets, name))); return;
  }
  let body = ''; for await (const chunk of req) body += chunk;
  if (url.pathname === heldPath) { requestCount++; await new Promise(resolve => { releaseRequest = resolve; }); }
  if (url.pathname === '/api/agents') return json({ agents: [{ id: 'dsh', name: '鲸屿', fields: [] }, { id: 'fixture', name: 'Fixture Agent', connected: true, managed: true, detected: true, fields: [{ key: 'model', label: '模型', value: agentValue, options: [{ value: 'model-a', label: 'Model A' }, { value: 'model-b', label: 'Model B' }] }] }] });
  if (url.pathname.startsWith('/api/agent-models/')) return json({ id: 'fixture', models: [] });
  if (url.pathname === '/api/agent-profiles') return json({ profiles: [], managed: 1 });
  if (url.pathname === '/api/agent/set') { agentValue = JSON.parse(body).value; return json({ ok: true }); }
  if (url.pathname === '/api/state') return json({ version: 'button-test', providers: [provider], models: [{ id: 'qa/qa-model' }], groups: [], presets: [] });
  if (url.pathname === '/api/provider/live') return json({ providers: [] });
  if (url.pathname === '/api/subscriptions/alerts') return json({ sequence: 0, alerts: [] });
  if (url.pathname === '/api/subscriptions') return json([]);
  if (url.pathname === '/api/provider/presets') return json([]);
  if (url.pathname === '/api/provider') return json(provider);
  if (url.pathname === '/api/provider/test') return json({ results: [{ ok: true, protocol: 'chat', model: 'qa-model', ms: 3 }] });
  res.writeHead(404); res.end('unknown fixture route');
});

async function run() {
  await app.whenReady();
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const win = new BrowserWindow({ width: 1120, height: 780, frame: false, show: false, webPreferences: { offscreen: true, contextIsolation: true, nodeIntegration: false, backgroundThrottling: false } });
  const js = source => win.webContents.executeJavaScript(source);
  const wait = async source => {
    for (let i = 0; i < 200; i++) { if (await js(`Boolean(${source})`)) return; await new Promise(resolve => setTimeout(resolve, 10)); }
    throw new Error('Unsettled: ' + source);
  };
  try {
    await win.loadURL(`http://127.0.0.1:${server.address().port}/`);
    await wait("document.querySelector('#content').getAttribute('aria-busy')==='false'");
    win.webContents.debugger.attach('1.3');
    let cases = 0;
    for (const scheme of ['light', 'dark']) {
      nativeTheme.themeSource = scheme;
      for (const width of [1120, 380]) {
        await win.webContents.debugger.sendCommand('Emulation.setDeviceMetricsOverride', { width, height: 780, deviceScaleFactor: 1, mobile: false });
        await win.webContents.debugger.sendCommand('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-reduced-motion', value: 'no-preference' }] });
        await js("go('providers')");
        await wait("document.querySelector('[data-action=edit-provider]')");
        await js("document.querySelector('[data-action=edit-provider]').click()");
        await wait("editor?.type==='provider'&&document.querySelector('[data-action=test-provider]')");
        await js('Promise.all(document.getAnimations().filter(a => a.effect.getTiming().iterations !== Infinity).map(a => a.finished))');
        const before = await js(`(() => {
          const b = document.querySelector('[data-action=test-provider]'), neighbor = document.querySelector('[data-action=detect-provider]');
          window.testButton = b;
          const rect = n => { const r = n.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height }; };
          return { button: rect(b), neighbor: rect(neighbor) };
        })()`);
        heldPath = '/api/provider/test'; releaseRequest = undefined;
        await js('testButton.click()');
        for (let i = 0; !releaseRequest && i < 200; i++) await new Promise(resolve => setTimeout(resolve, 10));
        assert.ok(releaseRequest, 'the real provider test reaches the held fixture request');
        const busy = await js(`(() => {
          const rect = n => { const r = n.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height }; };
          return { button: rect(testButton), neighbor: rect(document.querySelector('[data-action=detect-provider]')), text: testButton.textContent, busy: testButton.getAttribute('aria-busy') };
        })()`);
        assert.deepEqual(busy.button, before.button, `${scheme}/${width}: the pending provider test keeps its control dimensions`);
        assert.deepEqual(busy.neighbor, before.neighbor, `${scheme}/${width}: the adjacent protocol action stays in place`);
        assert.equal(busy.text, '测试中');
        assert.equal(busy.busy, 'true');
        await js('testButton.click()');
        assert.equal(requestCount, cases * 2 + 1, 'disabled feedback blocks repeated requests');
        heldPath = undefined; releaseRequest();
        await wait('!testButton.disabled');
        assert.equal(await js('testButton.textContent'), '检查连接');
        await js("document.querySelector('#editor').close()");
        agentValue = 'model-a';
        await js("go('agents')");
        await js(`(() => { const select = document.querySelector('#agent-target'); select.value = 'fixture'; select.dispatchEvent(new Event('change')); })()`);
        await wait("document.querySelector('[data-agent-field] button')&&!document.querySelector('#agent-target').disabled");
        await js(`(() => { const select = document.querySelector('[data-agent-field] select'); select.value = 'model-b'; select.dispatchEvent(new Event('change')); window.applyButton = document.querySelector('[data-agent-field] button'); })()`);
        const agentBefore = await js(`(() => { const r = applyButton.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height }; })()`);
        heldPath = '/api/agent/set'; releaseRequest = undefined;
        await js('applyButton.click()');
        for (let i = 0; !releaseRequest && i < 200; i++) await new Promise(resolve => setTimeout(resolve, 10));
        assert.ok(releaseRequest, 'the Agent apply control reaches its held request');
        await wait("applyButton.querySelector('.button-busy-label').hasAttribute('data-overflow')");
        const agentBusy = await js(`(() => {
          const r = applyButton.getBoundingClientRect(), track = applyButton.querySelector('.button-busy-label');
          const viewport = track.parentElement, animation = track.getAnimations()[0], spinner = applyButton.querySelector('.button-spinner');
          window.applyTrack = track; window.applyAnimation = animation;
          animation.pause(); const duration = Number(animation.effect.getTiming().duration);
          animation.currentTime = 0; const start = track.getBoundingClientRect().left - viewport.getBoundingClientRect().left;
          const center = () => { const s = spinner.getBoundingClientRect(); return s.x + s.width / 2; };
          const spinnerBefore = center(); animation.currentTime = duration * .5;
          const end = track.getBoundingClientRect().right - viewport.getBoundingClientRect().right;
          animation.currentTime = duration * .3; animation.play(); window.applyTime = animation.currentTime;
          return { rect: { x: r.x, y: r.y, width: r.width, height: r.height }, start, end, spinnerMoved: Math.abs(center() - spinnerBefore), text: track.textContent, aria: applyButton.getAttribute('aria-label'), title: applyButton.title };
        })()`);
        assert.deepEqual(agentBusy.rect, agentBefore);
        assert.ok(Math.abs(agentBusy.start) < 1 && Math.abs(agentBusy.end) < 1, 'the first and last characters are reachable');
        assert.ok(agentBusy.spinnerMoved < .1, 'only the label moves');
        assert.equal(agentBusy.text, '应用中'); assert.equal(agentBusy.aria, '应用中'); assert.equal(agentBusy.title, '应用中');
        await new Promise(resolve => setTimeout(resolve, 120));
        assert.equal(await js('applyAnimation.currentTime > applyTime'), true, 'the real animation clock advances');
        await win.webContents.debugger.sendCommand('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-reduced-motion', value: 'reduce' }] });
        assert.equal(await js('getComputedStyle(applyTrack).animationName'), 'none');
        assert.equal(await js('applyButton.getAttribute("aria-label")'), '应用中');
        heldPath = undefined; releaseRequest();
        await wait("document.querySelector('[data-agent-field] select').value==='model-b'&&!document.querySelector('#agents-page').hasAttribute('aria-busy')");
        await win.webContents.debugger.sendCommand('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-reduced-motion', value: 'no-preference' }] });
        await js(`(() => {
          const host = document.createElement('div'); host.id = 'feedback-fixture'; host.style.cssText = 'position:fixed;left:8px;bottom:8px;display:flex;gap:8px;background:var(--dsw-alias-bg-base);padding:8px;z-index:99';
          host.innerHTML = '<button id="short-action" class="text-button">上移</button><button id="wide-action">重新同步模型</button><button id="icon-action" class="icon-button" aria-label="刷新" title="原提示"><svg class="icon"></svg></button><label class="file-button"><span id="file-action">选择凭据文件</span><input type="file"></label><label class="check-field"><input type="checkbox"><span id="mirror-action">使用国内镜像</span></label>';
          document.body.append(host); window.fixtureSaved = new Map(); window.fixtureBusy = new Map();
          for (const id of ['short-action','wide-action','icon-action','file-action','mirror-action']) {
            const b = document.getElementById(id), r = b.getBoundingClientRect(); fixtureSaved.set(id, { html: b.innerHTML, width: r.width, height: r.height });
            fixtureBusy.set(id, startButtonBusy(b, id === 'file-action' ? '读取中' : '保存中'));
          }
        })()`);
        await wait("document.querySelector('#short-action .button-busy-label').hasAttribute('data-overflow')");
        const fixtures = await js(`[...fixtureSaved].map(([id, saved]) => {
          const b = document.getElementById(id), r = b.getBoundingClientRect(), track = b.querySelector('.button-busy-label');
          const animation = track?.getAnimations()[0]; let edges;
          if(animation){animation.pause(); animation.currentTime = 0; const start = track.getBoundingClientRect().left - track.parentElement.getBoundingClientRect().left; animation.currentTime = Number(animation.effect.getTiming().duration) * .5; edges = { start, end: track.getBoundingClientRect().right - track.parentElement.getBoundingClientRect().right }; animation.play();}
          return { id, before: saved, width: r.width, height: r.height, label: track?.textContent, viewport: track?.parentElement.clientWidth, edges, animation: track && getComputedStyle(track).animationName, spinner: b.querySelectorAll('.button-spinner').length };
        })`);
        for (const row of fixtures) {
          assert.ok(Math.abs(row.width - row.before.width) < .1, row.id + ' width'); assert.ok(Math.abs(row.height - row.before.height) < .1, row.id + ' height'); assert.equal(row.spinner, 1);
        }
        const short = fixtures.find(row => row.id === 'short-action');
        assert.ok(short.viewport >= 12, 'the smallest sorting button keeps readable character space beside the spinner');
        assert.ok(Math.abs(short.edges.start) < 1 && Math.abs(short.edges.end) < 1, 'sorting status reaches both ends');
        assert.equal(fixtures.find(row => row.id === 'wide-action').animation, 'none', 'a fitting caption stays still');
        assert.equal(await js(`(() => { const b = document.getElementById('wide-action'), spinner = b.querySelector('.button-spinner'); const center = () => { const r = spinner.getBoundingClientRect(); return r.x + r.width / 2; }; const before = center(); fixtureBusy.get('wide-action').update('正在读取来源目录'); return Math.abs(center() - before) < .1; })()`), true, 'different label lengths keep the spinner in its position');
        assert.equal(fixtures.find(row => row.id === 'icon-action').label, undefined, 'icon controls remain icon only');
        const continuity = await js(`(() => {
          const b = document.getElementById('short-action'), track = b.querySelector('.button-busy-label'), animation = track.getAnimations()[0];
          const duration = Number(animation.effect.getTiming().duration); animation.currentTime = duration * .3;
          fixtureBusy.get('short-action').update('移除中'); fixtureBusy.get('short-action').update('移除中');
          return { sameNode: track === b.querySelector('.button-busy-label'), sameAnimation: animation === track.getAnimations()[0], progress: animation.currentTime / duration };
        })()`);
        assert.equal(continuity.sameNode && continuity.sameAnimation, true); assert.ok(continuity.progress > .29);
        assert.equal(await js(`(() => {
          const b = document.getElementById('short-action'), old = fixtureBusy.get('short-action'), next = startButtonBusy(b, '删除中'); old.finish(); old.update('旧请求');
          const owns = b.getAttribute('aria-busy') === 'true' && b.textContent === '删除中'; next.finish();
          return owns && b.innerHTML === fixtureSaved.get('short-action').html;
        })()`), true, 'a stale owner cannot change or clear later feedback');
        assert.equal(await js(`(() => {
          for (const [id,busy] of fixtureBusy) busy.finish();
          return [...fixtureSaved].every(([id,saved]) => { const b = document.getElementById(id); return b.innerHTML === saved.html && !b.hasAttribute('data-button-busy') && !b.style.getPropertyValue('--button-busy-width'); });
        })()`), true);
        assert.equal(await js(`(() => {
          const b = document.getElementById('short-action'), busy = startButtonBusy(b, '读取中'); b.textContent = '隐藏密钥';
          const released = b.getBoundingClientRect().width > fixtureSaved.get('short-action').width;
          busy.finish();
          return released && b.textContent === '隐藏密钥' && !b.hasAttribute('data-button-busy');
        })()`), true, 'successful consumers can replace the next action');
        assert.equal(await js(`(() => {
          const save = document.getElementById('save'), old = startButtonBusy(save, '保存中'); editor = { type:'sources' }; showEditor();
          const clean = !save.hasAttribute('data-button-busy') && !save.style.getPropertyValue('--button-busy-width');
          const next = startButtonBusy(save, '登录中'); old.finish(); const owns = save.textContent === '登录中'; next.finish(); document.getElementById('editor').close();
          return clean && owns && save.textContent === '保存';
        })()`), true, 'reopening the shared editor resets feedback before another owner starts');
        assert.equal(await js(`(() => { const b = document.getElementById('icon-action'); return b.title === '原提示' && b.getAttribute('aria-label') === '刷新' && !b.disabled; })()`), true, 'feedback restores the original tooltip, accessible name and enabled state');
        await js("document.querySelector('#feedback-fixture').remove()");
        cases++;
      }
    }
    win.webContents.debugger.detach();
    console.log('BUTTON_FEEDBACK_RESULT:' + JSON.stringify({ cases, geometry: true, scrolling: true, ownership: true, restore: true }));
  } finally {
    releaseRequest?.();
    if (win.webContents.debugger.isAttached()) win.webContents.debugger.detach();
    win.destroy(); server.closeAllConnections(); await new Promise(resolve => server.close(resolve));
  }
}
run().then(() => app.exit(0)).catch(error => { console.error(error.stack); app.exit(1); });
