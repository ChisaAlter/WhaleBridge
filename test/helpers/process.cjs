'use strict';

const { spawn } = require('node:child_process');

function killProcessTree(child, { platform = process.platform, spawnKiller = spawn } = {}) {
  if (platform === 'win32' && typeof child.pid === 'number') {
    try {
      const killer = spawnKiller('taskkill', ['/PID', String(child.pid), '/T', '/F'], {
        windowsHide: true,
        stdio: 'ignore',
      });
      killer.on('error', () => { child.kill(); });
      killer.on('exit', (code) => {
        if (code !== 0) child.kill();
      });
      killer.unref();
      return;
    } catch {
      // Synchronous spawn failure; fall through to direct termination.
    }
  }
  child.kill();
}

module.exports = { killProcessTree };
