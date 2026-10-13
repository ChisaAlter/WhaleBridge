import { createHash } from 'node:crypto';
import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const source = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const pin = JSON.parse(readFileSync(join(source, 'upstream.json'), 'utf8'));
let out = join(source, '.tmp/whalebridge-package');
let version = pin.version;
for (let i = 2; i < process.argv.length; i += 2) {
  const flag = process.argv[i], value = process.argv[i + 1];
  if (!value || !['--out', '--version'].includes(flag)) {
    throw new Error('Usage: node scripts/build.mjs [--out directory] [--version x.y.z]');
  }
  if (flag === '--out') out = resolve(value);
  else version = value;
}
if (!/^\d+\.\d+\.\d+$/.test(version)) throw new Error('Invalid component version');

mkdirSync(out, { recursive: true });
const asset = 'WhaleBridge-win32-x64.exe';
execFileSync(process.env.WHALEBRIDGE_GO || 'go', [
  'build', '-mod=readonly', '-trimpath', '-ldflags', `-s -w -X main.version=${version}`,
  '-o', join(out, asset), '.',
], { cwd: source, stdio: 'inherit', env: { ...process.env, GOOS: 'windows', GOARCH: 'amd64', CGO_ENABLED: '0' } });
const binary = readFileSync(join(out, asset));
const license = readFileSync(join(source, 'LICENSE'), 'utf8');
const manifest = {
  id: 'whalebridge', name: '鲸桥', version,
  description: '默认接入鲸屿，也可为其他 Agent 配置模型，统一管理供应商、路由与用量。',
  upstream: pin.commit, providerFeaturesUpstream: pin.providerFeaturesCommit,
  providerDiscoveryUpstream: pin.providerDiscoveryCommit, searchFeaturesUpstream: pin.searchFeaturesCommit,
  agentFeaturesUpstream: pin.agentFeaturesCommit, license,
  platforms: { 'win32-x64': { asset, size: binary.length, sha256: createHash('sha256').update(binary).digest('hex') } },
};
writeFileSync(join(out, 'WhaleBridge-component.json'), `${JSON.stringify(manifest, null, 2)}\n`);
copyFileSync(join(source, 'LICENSE'), join(out, 'LICENSE'));
console.log(`鲸桥 ${version}: ${binary.length} bytes → ${out}`);
