'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { killProcessTree } = require('../helpers/process.cjs');
const electron = process.env.ELECTRON_PATH || path.resolve(__dirname, '../../node_modules/electron/dist/electron.exe');

test('WhaleBridge busy labels remain readable inside fixed control geometry', { skip: !fs.existsSync(electron) }, async t => {
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'whalebridge-button-feedback-'));
  t.after(() => fs.rmSync(profile, { recursive: true, force: true }));
  const result = await new Promise((resolve, reject) => {
    const env = { ...process.env }; delete env.ELECTRON_RUN_AS_NODE;
    const child = spawn(electron, [path.join(__dirname, 'whalebridge-button-feedback.child.cjs'), profile], { env, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] });
    let out = '', err = '';
    child.stdout.on('data', chunk => { out += chunk; }); child.stderr.on('data', chunk => { err += chunk; });
    const timer = setTimeout(() => killProcessTree(child), 30000);
    child.once('error', reject);
    child.once('close', code => {
      clearTimeout(timer);
      const row = out.split(/\r?\n/).find(line => line.startsWith('BUTTON_FEEDBACK_RESULT:'));
      if (code !== 0 || !row) reject(new Error(`button feedback renderer ${code}\n${err}\n${out}`));
      else resolve(JSON.parse(row.slice('BUTTON_FEEDBACK_RESULT:'.length)));
    });
  });
  assert.equal(result.cases, 4);
  assert.equal(result.geometry, true);
  assert.equal(result.scrolling, true);
  assert.equal(result.ownership, true);
  assert.equal(result.restore, true);
});
