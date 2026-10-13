// Exercise the independently built component through its actual startup protocol.
import assert from 'node:assert/strict';
import { spawn, execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { dirname, join, relative, resolve, isAbsolute } from 'node:path';
import { fileURLToPath } from 'node:url';

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const payload = resolve(process.env.WHALEBRIDGE_QA_PACKAGE || join(repo, '.tmp/whalebridge-package'));
const manifest = JSON.parse(readFileSync(join(payload, 'WhaleBridge-component.json')));
const spec = manifest.platforms['win32-x64'];
const binary = join(payload, spec.asset);
const bytes = readFileSync(binary);
assert.equal(bytes.length, spec.size);
assert.equal(createHash('sha256').update(bytes).digest('hex'), spec.sha256);
const profile = mkdtempSync(join(tmpdir(), 'whalebridge-component-'));
const home = join(profile, 'dsh'), data = join(profile, 'data');
const patch = join(home, 'profiles/web/cordis.patch.yml');
mkdirSync(dirname(patch), { recursive: true });
writeFileSync(patch, '- id: llm-pi-ai\n  config:\n    providers:\n      existing:\n        api: openai-completions\n        models: []\n- id: agent-default-model\n  config:\n    provider: existing\n    model: keep\n');
const socket = createServer();
await new Promise(resolve => socket.listen(0, '127.0.0.1', resolve));
const port = socket.address().port;
await new Promise(resolve => socket.close(resolve));
const env = { ...process.env, HOME: profile, USERPROFILE: profile,
  APPDATA: join(profile, 'roaming'), LOCALAPPDATA: join(profile, 'local'),
  XDG_CONFIG_HOME: join(profile, 'config'), XDG_DATA_HOME: join(profile, 'share'), XDG_STATE_HOME: join(profile, 'state'),
  LAUNCHER_COMPONENT_DATA_DIR: data, WHALEBRIDGE_DSH_HOME: home, MAGPIE_ADDR: `127.0.0.1:${port}`,
};
const vars = readFileSync(join(repo, 'internal/agentenv/agentenv.go'), 'utf8').split('var Vars = []string{')[1].split('\n}')[0];
for (const name of vars.match(/"[A-Z_]+"/g)) delete env[name.slice(1, -1)];
for (const name of ['ELECTRON_RUN_AS_NODE', 'MAGPIE_WEB_KEY', 'MAGPIE_WEB_RUNKEY']) delete env[name];
const child = spawn(binary, ['whalebridge'], { env, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] });
let output = '', failure;
child.stdout.on('data', bytes => { output += bytes; });
child.stderr.on('data', bytes => { output += bytes; });
child.once('error', error => { failure = error; });
try {
  const stateFile = join(data, 'state.json'), deadline = Date.now() + 30_000;
  while (!existsSync(stateFile)) {
    if (failure || child.exitCode !== null || Date.now() > deadline) throw failure || new Error(`Component not ready: ${output}`);
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  const state = JSON.parse(readFileSync(stateFile));
  assert.equal(state.id, 'whalebridge'); assert.equal(state.pid, child.pid);
  assert.equal(state.version, manifest.version); assert.equal(state.ready, true);
  const url = new URL(state.url);
  const access = await fetch(state.url, { redirect: 'manual' });
  assert.equal(access.status, 303);
  const cookie = access.headers.get('set-cookie').split(';')[0];
  assert.equal(cookie, `magpie_web_${url.port}=${url.searchParams.get('k')}`);
  const headers = { Cookie: cookie };
  const page = await fetch(url.origin, { headers });
  assert.equal(page.status, 200); assert.match(await page.text(), /agents\.js/);
  const response = await fetch(`${url.origin}/api/agents`, { headers });
  assert.equal(response.status, 200);
  const agents = await response.json();
  assert.equal(agents.defaultAgent, 'dsh'); assert.ok(agents.agents.some(agent => agent.id === 'codex'));
  const saved = readFileSync(patch, 'utf8');
  assert.match(saved, /whalebridge/); assert.match(saved, /provider: existing/);
  console.log(`WhaleBridge ${manifest.version}: native startup, embedded page and default WhaleIsle passed`);
} finally {
  if (child.pid && child.exitCode === null) {
    const closed = new Promise(resolve => child.once('close', resolve));
    if (process.platform === 'win32') execFileSync('taskkill', ['/PID', String(child.pid), '/T', '/F'], { windowsHide: true, stdio: 'ignore' });
    else child.kill();
    await closed;
  }
  const owned = relative(resolve(tmpdir()), resolve(profile));
  assert.ok(owned && !owned.startsWith('..') && !isAbsolute(owned));
  rmSync(profile, { recursive: true, force: true });
}
