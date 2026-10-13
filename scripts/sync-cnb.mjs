#!/usr/bin/env node
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { createReadStream, openAsBlob } from 'node:fs'
import { mkdtemp, readFile, readdir, rm, stat, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const safeRepo = /^[\w.-]+(?:\/[\w.-]+)+$/
const stableTag = /^whalebridge-v\d+\.\d+\.\d+$/
const sourceRepo = 'ChisaAlter/WhaleBridge'
const defaultMirrorRepo = 'ayasealter/WhaleIsle'
const manifestName = 'WhaleBridge-component.json'
const binaryName = 'WhaleBridge-win32-x64.exe'

function validateManifest(manifest) {
  const spec = manifest?.platforms?.['win32-x64']
  if (manifest?.id !== 'whalebridge' || !/^\d+\.\d+\.\d+$/.test(manifest.version) || spec?.asset !== binaryName ||
      !/^[a-f0-9]{64}$/.test(spec.sha256) || !Number.isSafeInteger(spec.size) || spec.size <= 0 ||
      spec.size > 200 * 1024 * 1024 || typeof manifest.license !== 'string' || !manifest.license.includes('MIT License')) {
    throw new Error('WhaleBridge release manifest mismatch')
  }
  return manifest
}
async function fileHash(file, algorithm = 'sha512') {
  const hash = createHash(algorithm)
  for await (const chunk of createReadStream(file)) hash.update(chunk)
  return hash.digest('hex')
}

export function cnbClient(token, fetchImpl = fetch) {
  if (!token) throw new Error('CNB_TOKEN is required')
  return async (path, { method = 'GET', body } = {}) => {
    const url = new URL('https://api.cnb.cool' + path)
    const options = { method, redirect: 'error', signal: AbortSignal.timeout(30000),
      headers: { Accept: 'application/vnd.cnb.api+json', Authorization: 'Bearer ' + token } }
    if (body) {
      options.headers['Content-Type'] = 'application/json'
      options.body = JSON.stringify(body)
    }
    let response
    try { response = await fetchImpl(url, options) }
    catch { throw new Error('CNB ' + method + ': request failed') }
    if (response.status === 404) return null
    if (!response.ok) throw new Error('CNB ' + method + ': HTTP ' + response.status)
    const text = await response.text()
    return text ? JSON.parse(text) : null
  }
}

async function verifyDownload(asset, expected, fetchImpl) {
  const url = asset?.browser_download_url
  if (!url || new URL(url).protocol !== 'https:' || new URL(url).hostname !== 'cnb.cool') {
    throw new Error('CNB asset has no anonymous download URL: ' + expected.name)
  }
  const response = await fetchImpl(url, { signal: AbortSignal.timeout(30 * 60 * 1000) })
  if (!response.ok || !response.body) throw new Error('CNB asset download failed: ' + expected.name)
  const hash = createHash('sha512')
  let size = 0
  for await (const chunk of response.body) {
    size += chunk.byteLength
    if (size > expected.size) throw new Error('CNB asset size mismatch: ' + expected.name)
    hash.update(chunk)
  }
  if (size !== expected.size || hash.digest('hex') !== expected.sha512) {
    throw new Error('CNB asset checksum mismatch: ' + expected.name)
  }
}

export async function validateMirrorAssets(release, directory) {
  if (!stableTag.test(release.tag_name) || release.draft || release.prerelease) {
    throw new Error('Mirror requires a published stable GitHub release')
  }
  const tag = release.tag_name
  const names = release.assets.map(a => a.name)
  if (names.length !== 2 || new Set(names).size !== 2 || !names.includes(manifestName) || !names.includes(binaryName)) {
    throw new Error('WhaleBridge release must contain exactly the component manifest and executable')
  }
  const files = []
  for (const asset of release.assets) {
    const file = join(directory, asset.name)
    const info = await stat(file)
    if (!info.isFile() || info.size !== asset.size) throw new Error('GitHub asset size mismatch: ' + asset.name)
    if (asset.digest && (!/^sha256:[a-f0-9]{64}$/.test(asset.digest) || asset.digest !== 'sha256:' + await fileHash(file, 'sha256'))) {
      throw new Error('GitHub asset digest mismatch: ' + asset.name)
    }
    files.push({ file, name: asset.name, size: info.size, sha512: await fileHash(file) })
  }
  const manifest = validateManifest(JSON.parse(await readFile(join(directory, manifestName), 'utf8')))
  const spec = manifest.platforms['win32-x64']
  if (tag !== 'whalebridge-v' + manifest.version || (await stat(join(directory, binaryName))).size !== spec.size ||
      await fileHash(join(directory, binaryName), 'sha256') !== spec.sha256) throw new Error('WhaleBridge release manifest mismatch')
  return files
}

// Partial uploads remain prereleases. Reruns verify existing bytes and resume missing attachments.
export async function mirrorRelease(options) {
  const files = await validateMirrorAssets(options.release, options.directory)
  return mirrorValidatedRelease({ ...options, files })
}

async function mirrorValidatedRelease({ release, files, sha, request, fetchImpl = fetch, repo, output = console.log }) {
  if (!safeRepo.test(repo) || !/^[a-f0-9]{40}$/.test(sha)) throw new Error('Mirror repository and source commit are required')
  const tag = release.tag_name
  const prefix = '/' + repo + '/-'
  const sourceTag = await request(prefix + '/git/tags/' + encodeURIComponent(tag))
  if (sourceTag?.commit?.sha !== sha) throw new Error('CNB tag does not match GitHub source: ' + tag)
  let target = await request(prefix + '/releases/tags/' + encodeURIComponent(tag))
  const body = { tag_name: tag, name: release.name || tag, body: release.body || ('GitHub release ' + tag) }
  if (!target) {
    const created = await request(prefix + '/releases', { method: 'POST', body: { ...body, target_commitish: sha, draft: false, prerelease: true, make_latest: 'false' } })
    if (!created?.id) throw new Error('CNB release creation returned no ID')
    target = await request(prefix + '/releases/' + created.id)
  }
  if (!target?.id || !Array.isArray(target.assets)) throw new Error('CNB release returned no ID or asset list')
  const staged = target.prerelease === true && target.draft !== true
  const assets = target.assets
  for (const file of files) {
    const existing = assets.filter(a => a.name === file.name)
    if (existing.length > 1) throw new Error('Duplicate CNB asset: ' + file.name)
    let asset = existing[0]
    if (!asset) {
      if (!staged) throw new Error('Published CNB release is incomplete: ' + file.name)
      output('Uploading ' + file.name)
      const upload = await request(prefix + '/releases/' + target.id + '/asset-upload-url', {
        method: 'POST', body: { asset_name: file.name, size: file.size, overwrite: false, ttl: 0 },
      })
      const destination = new URL(upload.upload_url)
      const confirmation = new URL(upload.verify_url, 'https://api.cnb.cool')
      if (destination.protocol !== 'https:' || confirmation.origin !== 'https://api.cnb.cool' ||
          !confirmation.pathname.startsWith(prefix + '/releases/' + target.id + '/asset-upload-confirmation/')) {
        throw new Error('CNB returned an invalid upload destination')
      }
      let response
      try {
        response = await fetchImpl(destination, { method: 'PUT', body: await openAsBlob(file.file),
          redirect: 'error', signal: AbortSignal.timeout(30 * 60 * 1000) })
      } catch { throw new Error('CNB asset upload failed: ' + file.name) }
      if (!response.ok) throw new Error('CNB asset upload failed: ' + file.name + ' (HTTP ' + response.status + ')')
      confirmation.searchParams.set('ttl', '0')
      await request(confirmation.pathname + confirmation.search, { method: 'POST' })
      const refreshed = await request(prefix + '/releases/' + target.id)
      asset = refreshed?.assets?.find(row => row.name === file.name)
    }
    await verifyDownload(asset, file, fetchImpl)
  }
  if (staged) await request(prefix + '/releases/' + target.id, { method: 'PATCH', body: {
    name: body.name, body: body.body, draft: false, prerelease: false,
    make_latest: 'false',
  } })
  const url = 'https://cnb.cool/' + repo + '/-/releases/tag/' + tag
  output('Verified CNB mirror: ' + url)
  return { url, assets: files.length }
}

export async function syncSource({ repo, token, tag, git = (args, options) => execFileSync('git', args, options) }) {
  if (!safeRepo.test(repo) || !token || !stableTag.test(tag)) throw new Error('CNB repository, push credentials and component tag are required')
  const directory = await mkdtemp(join(tmpdir(), 'cnb-auth-'))
  try {
    const askpass = join(directory, 'askpass.sh')
    await writeFile(askpass, '#!/bin/sh\ncase "$1" in\n  *Username*) printf "%s\\n" "cnb" ;;\n  *) printf "%s\\n" "$CNB_TOKEN" ;;\nesac\n', { mode: 0o700 })
    git(['-c', 'credential.helper=', '-c', 'core.askPass=' + askpass, 'push', 'https://cnb.cool/' + repo + '.git',
      'refs/tags/' + tag + ':refs/tags/' + tag], {
      env: { ...process.env, CNB_TOKEN: token, GIT_TERMINAL_PROMPT: '0' }, stdio: ['ignore', 'pipe', 'pipe'],
    })
  } catch { throw new Error('CNB component tag push failed; check token permissions or an existing tag with different source (no force push)') }
  finally { await rm(directory, { recursive: true, force: true }) }
}

async function main() {
  const token = process.env.CNB_TOKEN
  const mirrorRepo = process.env.CNB_REPO || defaultMirrorRepo
  const request = cnbClient(token)
  const repo = process.env.GITHUB_REPOSITORY || sourceRepo
  if (repo !== sourceRepo) throw new Error('Only the WhaleBridge GitHub repository can be mirrored')
  const tag = process.env.RELEASE_TAG || ''
  if (tag && !stableTag.test(tag)) throw new Error('RELEASE_TAG must be a stable WhaleBridge version tag')
  const gh = args => execFileSync(process.env.GH_PATH || 'gh', args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] })
  const release = JSON.parse(gh(['api', 'repos/' + repo + '/releases/' + (tag ? 'tags/' + tag : 'latest')]))
  if (!stableTag.test(release.tag_name) || release.draft || release.prerelease) throw new Error('Only stable published releases can be mirrored')
  const tagRef = 'refs/tags/' + release.tag_name
  execFileSync('git', ['fetch', '--no-tags', 'https://github.com/' + sourceRepo + '.git', tagRef + ':' + tagRef], { stdio: ['ignore', 'pipe', 'pipe'] })
  const sha = execFileSync('git', ['rev-parse', 'refs/tags/' + release.tag_name + '^{commit}'], { encoding: 'utf8' }).trim()
  const directory = await mkdtemp(join(tmpdir(), 'cnb-release-'))
  try {
    gh(['release', 'download', release.tag_name, '--repo', repo, '--dir', directory])
    const downloaded = await readdir(directory)
    if (downloaded.length !== release.assets.length) throw new Error('GitHub download did not contain the expected release assets')
    const files = await validateMirrorAssets(release, directory)
    await syncSource({ token, repo: mirrorRepo, tag: release.tag_name })
    console.log('CNB component tag synchronized: ' + release.tag_name)
    await mirrorValidatedRelease({ release, files, sha, request, repo: mirrorRepo })
  } finally { await rm(directory, { recursive: true, force: true }) }
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main().catch(error => { console.error(error.message); process.exitCode = 1 })
}
