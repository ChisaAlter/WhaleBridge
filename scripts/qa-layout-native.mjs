import { spawn } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import { resolve, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
const repo=resolve(dirname(fileURLToPath(import.meta.url)), '..');
const electron=process.env.ELECTRON_PATH||join(repo,'node_modules/electron/dist',process.platform==='win32'?'electron.exe':'electron');
if(!existsSync(electron))throw new Error('Install the local Electron UI test dependency first');
const env={...process.env};delete env.ELECTRON_RUN_AS_NODE;
const child=spawn(electron,[join(repo,'scripts/qa-layout-native.cjs')],{cwd:repo,env,stdio:'inherit'});
child.on('error',error=>{console.error(error.message);process.exitCode=1;});
child.on('exit',code=>{
  process.exitCode=code||0;
  const reportPath=join(resolve(env.WHALEBRIDGE_QA_LAYOUT||join(repo,'.tmp/ui-layout-audit')),'layout-report.json');
  if(!existsSync(reportPath)){console.error('Native UI audit did not produce a report');process.exitCode=1;return;}
  const report=JSON.parse(readFileSync(reportPath,'utf8'));
  if(report.status!=='passed'||report.runtimeErrors.length)process.exitCode=1;
});
