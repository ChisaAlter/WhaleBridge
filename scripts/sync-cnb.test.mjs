import test from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, writeFileSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { basename, join } from 'node:path'
import { cnbClient, mirrorRelease, syncSource, validateMirrorAssets } from './sync-cnb.mjs'

test('component mirror validates native bytes against the component manifest', async t => {
  const directory = mkdtempSync(join(tmpdir(), 'cnb-component-'))
  t.after(() => rmSync(directory, { recursive: true, force: true }))
  const binary = Buffer.from('component')
  const manifest = { id: 'whalebridge', version: '1.0.0', license: 'MIT License', platforms: { 'win32-x64': {
    asset: 'WhaleBridge-win32-x64.exe', size: binary.length, sha256: createHash('sha256').update(binary).digest('hex'),
  } } }
  const bytes = Buffer.from(JSON.stringify(manifest))
  writeFileSync(join(directory, 'WhaleBridge-component.json'), bytes)
  writeFileSync(join(directory, 'WhaleBridge-win32-x64.exe'), binary)
  const release = { tag_name: 'whalebridge-v1.0.0', assets: [
    { name: 'WhaleBridge-component.json', size: bytes.length }, { name: 'WhaleBridge-win32-x64.exe', size: binary.length },
  ] }
  assert.equal((await validateMirrorAssets(release, directory)).length, 2)
  for (const changes of [{ tag_name: 'v1.0.0' }, { draft: true }, { prerelease: true },
    { assets: [...release.assets, { name: 'LICENSE', size: 11 }] }, { assets: [release.assets[0], release.assets[0]] }]) {
    await assert.rejects(validateMirrorAssets({ ...release, ...changes }, directory), /stable|exactly/)
  }
  writeFileSync(join(directory, 'WhaleBridge-win32-x64.exe'), Buffer.from('tampered!'))
  await assert.rejects(validateMirrorAssets(release, directory), /manifest mismatch/)
})

const repo = 'ayasealter/WhaleIsle'
const sha = 'a'.repeat(40)
const digest = (bytes, algo = 'sha512', encoding = 'hex') => createHash(algo).update(bytes).digest(encoding)
function fixture(t, { existing = null, uploaded = [], corruptRemote = false, failUpload = false, tagSha = sha, latest = null } = {}) {
  const directory = mkdtempSync(join(tmpdir(), 'cnb-mirror-test-'))
  t.after(() => rmSync(directory, { recursive: true, force: true }))
  const name = 'WhaleBridge-win32-x64.exe'
  const binary = Buffer.from('component')
  const manifest = { id: 'whalebridge', version: '1.2.3', license: 'MIT License', platforms: { 'win32-x64': {
    asset: name, size: binary.length, sha256: digest(binary, 'sha256'),
  } } }
  const bytes = new Map([[name, binary], ['WhaleBridge-component.json', Buffer.from(JSON.stringify(manifest, null, 2) + '\n')]])
  for (const [n, b] of bytes) writeFileSync(join(directory, n), b)
  const release = { tag_name: 'whalebridge-v1.2.3', name: 'WhaleBridge', body: 'Release notes', prerelease: false, draft: false,
    assets: [...bytes].map(([n, b]) => ({ name: n, size: b.length, digest: 'sha256:' + digest(b, 'sha256') })) }
  const asset = name => ({ id: name, name, browser_download_url: 'https://cnb.cool/' + repo + '/-/releases/download/' + release.tag_name + '/' + name })
  const calls = []
  let target = existing ? { draft: false, ...existing, assets: uploaded.map(asset) } : null
  const request = async (path, options = {}) => {
    calls.push([path, options])
    if (path.includes('/git/tags/')) return { name: release.tag_name, commit: { sha: tagSha } }
    if (path.endsWith('/releases/latest')) return latest
    if (path.includes('/releases/tags/')) return target
    if (path.endsWith('/asset-upload-url')) return { upload_url: 'https://storage.example/' + options.body.asset_name, verify_url: 'https://api.cnb.cool/' + repo + '/-/releases/9/asset-upload-confirmation/' + options.body.asset_name }
    if (path.includes('/asset-upload-confirmation/')) {
      target.assets.push(asset(basename(new URL('https://api.cnb.cool' + path).pathname)))
      return null
    }
    if (options.method === 'POST') {
      target = { id: '9', draft: false, prerelease: options.body.prerelease, assets: [] }
      return { ...target, assets: null }
    }
    if (options.method === 'PATCH') { Object.assign(target, options.body); return null }
    if (path.endsWith('/releases/9')) return target
    throw new Error('Unexpected request ' + path)
  }
  const fetchImpl = async (url, options) => {
    assert.equal(options.headers, undefined, 'storage upload and attachment verification must not send credentials')
    const name = basename(new URL(url).pathname)
    if (options.method === 'PUT') {
      calls.push(['upload', name])
      if (failUpload) throw new Error('upload interrupted')
      assert.deepEqual(Buffer.from(await options.body.arrayBuffer()), bytes.get(name))
      return new Response(null, { status: 200 })
    }
    calls.push(['download', name])
    return new Response(corruptRemote ? Buffer.from('wrong') : bytes.get(name))
  }
  return { options: { release, directory, sha, request, fetchImpl, repo, output: () => {} }, calls, bytes }
}

test('stages original bytes and promotes only after every anonymous checksum passes', async t => {
  const f = fixture(t)
  const result = await mirrorRelease(f.options)
  assert.equal(result.assets, 2)
  const create = f.calls.find(([, o]) => o.method === 'POST' && !o.file)
  assert.equal(create[1].body.prerelease, true)
  assert.equal(create[1].body.target_commitish, sha)
  assert.equal(f.calls.filter(([p]) => p === 'download').length, 2)
  assert.equal(f.calls.at(-1)[1].body.prerelease, false)
})

test('partial mirrors resume missing files; published mirrors are verified without rewriting', async t => {
  const f = fixture(t, { existing: { id: 9, prerelease: true }, uploaded: ['WhaleBridge-component.json'] })
  await mirrorRelease(f.options)
  assert.equal(f.calls.filter(([p]) => p === 'upload').length, 1)
  assert.ok(!f.calls.some(([p, o]) => p.endsWith('/releases') && o.method === 'POST'))
  const published = fixture(t, { existing: { id: 9, prerelease: false } })
  await assert.rejects(mirrorRelease(published.options), /incomplete/)
  assert.ok(!published.calls.some(([, o]) => o.method))
  const complete = fixture(t, { existing: { id: 9, prerelease: false }, uploaded: [...published.bytes.keys()] })
  await mirrorRelease(complete.options)
  assert.ok(!complete.calls.some(([, o]) => o.method))
})

test('component releases never replace the shared mirror desktop latest', async t => {
  const f = fixture(t, { latest: { tag_name: 'v2.0.0' } })
  await mirrorRelease(f.options)
  assert.equal(f.calls.at(-1)[1].body.make_latest, 'false')
  assert.ok(!f.calls.some(([path]) => path.endsWith('/releases/latest')))
})

test('interrupted uploads or corrupt anonymous bytes never promote the staged release', async t => {
  for (const changes of [{ failUpload: true }, { corruptRemote: true }]) {
    const f = fixture(t, changes)
    await assert.rejects(mirrorRelease(f.options), /upload failed|mismatch/)
    assert.ok(!f.calls.some(([, o]) => o.method === 'PATCH'))
  }
})

test('source tag, GitHub digest and component manifest are checked before CNB writes', async t => {
  const badTag = fixture(t, { tagSha: 'b'.repeat(40) })
  await assert.rejects(mirrorRelease(badTag.options), /source/)
  const badDigest = fixture(t)
  writeFileSync(join(badDigest.options.directory, 'WhaleBridge-win32-x64.exe'), 'tampering')
  await assert.rejects(mirrorRelease(badDigest.options), /digest/)
  const badManifest = fixture(t)
  const manifest = JSON.parse(badManifest.bytes.get('WhaleBridge-component.json'))
  manifest.platforms['win32-x64'].sha256 = '0'.repeat(64)
  const manifestBytes = Buffer.from(JSON.stringify(manifest))
  writeFileSync(join(badManifest.options.directory, 'WhaleBridge-component.json'), manifestBytes)
  Object.assign(badManifest.options.release.assets.find(a => a.name === 'WhaleBridge-component.json'), { size: manifestBytes.length, digest: 'sha256:' + digest(manifestBytes, 'sha256') })
  await assert.rejects(mirrorRelease(badManifest.options), /manifest/)
  for (const f of [badTag, badDigest, badManifest]) assert.ok(!f.calls.some(([, o]) => o.method))
})

test('CNB API uses bearer JSON, supports empty confirmations and never logs signed URLs or tokens', async () => {
  const seen = []
  const client = cnbClient('secret-value', async (url, options) => {
    seen.push([url, options])
    return new Response(options.method === 'PATCH' ? null : '{}', { status: 200 })
  })
  await client('/a/b/-/releases')
  await client('/a/b/-/releases', { method: 'POST', body: { prerelease: true } })
  assert.equal(await client('/a/b/-/releases/1', { method: 'PATCH', body: { prerelease: false } }), null)
  assert.equal(seen[0][0].origin, 'https://api.cnb.cool')
  assert.equal(seen[0][1].headers.Authorization, 'Bearer secret-value')
  assert.equal(seen[0][1].headers.Accept, 'application/vnd.cnb.api+json')
  assert.deepEqual(JSON.parse(seen[1][1].body), { prerelease: true })
  const failed = cnbClient('secret-value', async () => { throw new Error('signed URL secret-value') })
  await assert.rejects(failed('/a/b/-/releases'), error => !error.message.includes('secret-value'))
  assert.throws(() => cnbClient(''), /CNB_TOKEN/)
})

test('source mirror pushes only the selected component tag and preserves shared main and old tags on conflict', async t => {
  const directory = mkdtempSync(join(tmpdir(), 'cnb-git-test-'))
  t.after(() => rmSync(directory, { recursive: true, force: true }))
  const source = join(directory, 'source')
  const host = join(directory, 'host')
  const target = join(directory, 'target.git')
  const exec = args => execFileSync('git', args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim()
  exec(['init', '--initial-branch=main', source])
  exec(['init', '--bare', target])
  exec(['init', '--initial-branch=main', host])
  exec(['-C', host, 'config', 'user.email', 'mirror@example.test'])
  exec(['-C', host, 'config', 'user.name', 'Mirror test'])
  exec(['-C', host, 'commit', '--allow-empty', '-m', 'WhaleIsle main'])
  const hostHead = exec(['-C', host, 'rev-parse', 'HEAD'])
  exec(['-C', host, 'tag', 'whalebridge-v1.1.0'])
  exec(['-C', host, 'push', target, 'main', 'refs/tags/whalebridge-v1.1.0'])
  exec(['-C', source, 'config', 'user.email', 'mirror@example.test'])
  exec(['-C', source, 'config', 'user.name', 'Mirror test'])
  exec(['-C', source, 'commit', '--allow-empty', '-m', 'WhaleBridge source'])
  const head = exec(['-C', source, 'rev-parse', 'HEAD'])
  exec(['-C', source, 'tag', '-a', 'whalebridge-v1.2.3', '-m', 'Component release'])
  exec(['-C', source, 'tag', 'whalebridge-v9.9.9'])
  exec(['-C', source, 'tag', 'whalebridge-v1.1.0'])
  let selectedTag = 'whalebridge-v1.2.3'
  const git = (args, options) => {
    assert.ok(!args.includes('--force'))
    assert.ok(!args.includes('--tags'))
    assert.ok(!args.some(a => a.startsWith('+') || a.includes('refs/heads/') || a.includes('refs/remotes/')))
    assert.equal(args.at(-1), 'refs/tags/' + selectedTag + ':refs/tags/' + selectedTag)
    const askpass = args.find(a => a.startsWith('core.askPass=')).slice('core.askPass='.length)
    assert.ok(!readFileSync(askpass, 'utf8').includes('secret-value'))
    execFileSync('git', ['-C', source, ...args.map(a => a.startsWith('https://cnb.cool/') ? target : a)], options)
  }
  await syncSource({ token: 'secret-value', repo, tag: selectedTag, git })
  assert.equal(exec(['--git-dir=' + target, 'rev-parse', 'whalebridge-v1.2.3^{commit}']), head)
  assert.equal(exec(['--git-dir=' + target, 'rev-parse', 'main']), hostHead)
  assert.equal(exec(['--git-dir=' + target, 'rev-parse', 'whalebridge-v1.1.0']), hostHead)
  assert.throws(() => exec(['--git-dir=' + target, 'rev-parse', '--verify', 'refs/tags/whalebridge-v9.9.9']))
  selectedTag = 'whalebridge-v1.1.0'
  await assert.rejects(syncSource({ token: 'secret-value', repo, tag: selectedTag, git }), /push failed/)
  assert.equal(exec(['--git-dir=' + target, 'rev-parse', 'main']), hostHead)
  assert.equal(exec(['--git-dir=' + target, 'rev-parse', 'whalebridge-v1.1.0']), hostHead)
  assert.equal(exec(['--git-dir=' + target, 'rev-parse', 'whalebridge-v1.2.3^{commit}']), head)
  await assert.rejects(syncSource({ token: 'secret-value', repo, tag: 'v1.2.3', git }), /component tag/)
})

test('workflow covers component releases and manual backfill without main source pushes or rebuilding', () => {
  const workflow = readFileSync(new URL('../.github/workflows/cnb-mirror.yml', import.meta.url), 'utf8').replace(/\r\n/g, '\n')
  assert.doesNotMatch(workflow, /^  push:/m)
  assert.match(workflow, /^  workflow_run:\n    workflows: \[WhaleBridge\]\n    types: \[completed\]\n    branches: \[main\]/m)
  assert.match(workflow, /^  release:\n    types: \[published\]/m)
  assert.match(workflow, /^  workflow_dispatch:\n    inputs:\n      tag:/m)
  assert.match(workflow, /^  cancel-in-progress: false$/m)
  assert.match(workflow, /^          fetch-depth: 0$/m)
  assert.match(workflow, /^          CNB_TOKEN: \$\{\{ secrets.CNB_TOKEN \}\}$/m)
  assert.match(workflow, /^          CNB_REPO: \$\{\{ vars.CNB_REPO \}\}$/m)
  assert.match(workflow, /github\.event\.workflow_run\.conclusion == 'success'/)
  assert.match(workflow, /github\.event\.workflow_run\.head_repository\.full_name == github\.repository/)
  assert.match(workflow, /github\.event\.workflow_run\.head_sha \}\}:upstream\.json/)
  assert.ok(!/npm (test|run (dist|setup:harness))/.test(workflow))
})
