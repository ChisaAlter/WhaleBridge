'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { killProcessTree } = require('../helpers/process.cjs');

const electron = [
  process.env.ELECTRON_PATH,
  path.resolve(__dirname, '../../node_modules/electron/dist/electron.exe'),
  path.resolve(__dirname, '../../node_modules/electron/dist/Electron.app/Contents/MacOS/Electron'),
  path.resolve(__dirname, '../../node_modules/electron/dist/electron'),
].find(file => file && fs.existsSync(file));

test('real WhaleBridge account UI retains connection feedback and consistent responsive layout', { skip: !electron }, async () => {
  const tempRoot = path.resolve(os.tmpdir());
  const profile = fs.mkdtempSync(path.join(tempRoot, 'whalebridge-accounts-'));
  let failure;
  try {
    const result = await new Promise((resolve, reject) => {
      const env = { ...process.env };
      delete env.ELECTRON_RUN_AS_NODE;
      const child = spawn(electron, [path.join(__dirname, 'whalebridge-accounts.child.cjs'), profile], {
        env, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'],
      });
      let stdout = '', stderr = '', childError, timedOut = false;
      child.stdout.on('data', data => { stdout += data; });
      child.stderr.on('data', data => { stderr += data; });
      const timer = setTimeout(() => { timedOut = true; killProcessTree(child); }, 120000);
      child.once('error', error => { childError = error; clearTimeout(timer); });
      // close follows exit and drained stdio; cleanup must not race Electron.
      child.once('close', (code, signal) => {
        clearTimeout(timer);
        if (timedOut || childError) return reject(new Error(
          `WhaleBridge account renderer ${timedOut ? 'timeout' : 'spawn failed'}: ${childError || signal || code}\n${stderr}\n${stdout}`,
          childError ? { cause: childError } : undefined,
        ));
        const marker = 'WHALEBRIDGE_ACCOUNTS_RESULT:';
        const line = stdout.split(/\r?\n/).find(row => row.startsWith(marker));
        if (code !== 0 || !line) return reject(new Error(`WhaleBridge account renderer ${code}: ${stderr}\n${stdout}`));
        try { resolve(JSON.parse(line.slice(marker.length))); } catch (error) { reject(error); }
      });
    });
    if (process.env.WHALEBRIDGE_QA_SCREENSHOTS) {
      fs.writeFileSync(path.join(path.resolve(process.env.WHALEBRIDGE_QA_SCREENSHOTS), 'layout-result.json'), JSON.stringify(result, null, 2));
    }

    assert.equal(result.initial.selected, 'claude', 'a new add-account dialog retains its default');
    assert.equal(result.beforeInstall.selected, 'cursor');
    assert.equal(result.beforeInstall.hasAdapterButton, true);
    assert.equal(result.installed.selected, 'cursor-plugin', 'refresh selects the installed backend, not the built-in');
    assert.deepEqual(result.installed.methods, ['Cursor OAuth', 'Cursor API key']);
    assert.equal(result.installed.hasAdapterButton, false);
    assert.deepEqual(result.signin, { agent: 'cursor', plugin: true, method: 0, inputs: {} });
    assert.equal(result.migrated.selected, 'cursor', 'provider identity comes from the refreshed catalog, not a suffix');
    assert.deepEqual(result.migrated.methods, ['Cursor OAuth', 'Cursor API key']);
    assert.equal(result.migrated.hasAdapterButton, false);
    assert.equal(result.failureDefault, 'claude', 'the prior adapter does not leak into a new dialog');
    assert.equal(result.failed.selected, 'cursor');
    assert.equal(result.failed.hasAdapterButton, true);
    assert.equal(result.failed.adapterDisabled, false);
    assert.equal(result.failed.error, 'fixture adapter installation failed');
    assert.equal(result.failureRefreshes, 0, 'a failed installation does not replace the form');
    assert.equal(result.addAccount.selected, 'cursor', 'adding an existing provider account keeps its exact identity');
    assert.equal(result.addAccount.hasAdapterButton, true);
    assert.deepEqual(result.addAccount.methods, []);
    assert.deepEqual([result.catalogs[0], result.catalogs.find(row => row.installed)].map(row => row.ids), [
      ['claude', 'codex', 'cursor'],
      ['claude', 'codex', 'cursor', 'cursor-plugin'],
    ], 'the adapter API actually changes the HTTP catalog fixture');
    assert.deepEqual(result.installs.map(call => call.body), [{ id: 'cursor', action: 'install' }, { id: 'cursor', action: 'install' }, { id: 'cursor', action: 'install' }]);
    assert.deepEqual(result.prompts.filter(call => call.scenario === 'normal').map(call => call.body), [
      { id: 'cursor', method: 0, inputs: {} },
    ]);
    assert.equal(result.promptValidationError, 'fixture tenant rejected');
    assert.deepEqual(result.prompts.filter(call => call.scenario === 'prompts').map(call => call.body), [
      { id: 'cursor', method: 0, inputs: {} },
      { id: 'cursor', method: 0, inputs: {}, key: 'tenant', value: 'invalid' },
      { id: 'cursor', method: 0, inputs: {}, key: 'tenant', value: 'fixture-tenant' },
      { id: 'cursor', method: 0, inputs: { tenant: 'fixture-tenant' }, key: 'region', value: 'eu' },
    ], 'prompt answers are validated by key/value before joining the accepted inputs');
    assert.deepEqual(result.promptSignin, {
      agent: 'cursor', plugin: true, method: 0, inputs: { tenant: 'fixture-tenant', region: 'eu' },
    }, 'sign-in receives the accepted sequential prompt answers');

    const parity = result.accountParity;
    assert.equal(parity.initial.alias, 'Personal fixture', 'the account alias is visible on its original management row');
    assert.ok(parity.initial.identity.includes('fixture.person@example.test'), 'an alias does not hide the original account identity');
    for (const text of ['Fixture shared seat', '最近成功请求', '重置使用记录', '手动使用', '2 个窗口', '重置到期时间', '配置套餐访问凭据']) {
      assert.ok(parity.initial.detail.includes(text), `the safe quota DTO exposes ${text} on the original account surface`);
    }
    assert.ok(parity.initial.lapsed.includes('revoked; sign in again'), 'the vendor refusal reason remains visible');
    assert.equal(parity.initial.lapsedToggleDisabled, true, 'a revoked/lapsed sign-in cannot be misleadingly enabled');
    assert.equal(parity.initial.canRelogin, true, 're-login remains actionable when authorization lapsed');
    assert.equal(parity.initial.canRemove, true, 'the lapsed account retains its removal action');
    assert.deepEqual(parity.refreshQueries, ['', '?refresh=true'], 'automatic account reads and explicit refresh retain distinct HTTP requests');
    assert.equal(parity.accessKeyTarget.name, 'Fixture plan credential settings', 'NeedsAccessKey opens the actual linked provider editor');
    assert.deepEqual(parity.draftReads, ['', ''], 'both delayed ordinary HTTP reads actually execute');
    assert.deepEqual(parity.draftLate, {
      alias: 'Draft personal name', checkin: false, cap: '88', proxy: 'http://fixture-draft-proxy.invalid:8118',
      windows: ['5 hours 使用上限（%）', 'Weekly 使用上限（%）'],
    }, 'late usage adds the newly reported window without overwriting the account drafts or adding unlimited/tier caps');
    const written = path => parity.writes.filter(call => call.path === path).map(call => call.body);
    assert.deepEqual(written('/api/accounts/name'), [{ id: 'qoder', user: 'fixture.person@example.test', alias: 'Draft personal name' }]);
    assert.deepEqual(written('/api/accounts/checkin'), [{ key: 'fixture-checkin-account', on: false }], 'the checkbox changes only the backend-provided account check-in key');
    assert.deepEqual(written('/api/accounts/windowcap'), [
      { id: 'qoder', user: 'fixture.person@example.test', window: 'fiveHour', cap: 88 },
      { id: 'qoder', user: 'fixture.person@example.test', window: 'weekly', cap: 100 },
    ], 'window cap writes retain the reported cap IDs and the 100 no-cap value');
    assert.ok(parity.saved.includes('Draft personal name'), 'reopening uses the alias persisted by the HTTP fixture');
    assert.ok(parity.saved.includes('此账号不参与每日签到'), 'the saved account participation is read back');
    assert.equal(parity.cleared, 'fixture.person@example.test', 'clearing an alias restores the original identity');
    assert.deepEqual(parity.clearWrites.filter(c => c.path === '/api/accounts/windowcap').map(c => c.body), [
      { id: 'qoder', user: 'fixture.person@example.test', window: 'fiveHour', cap: 0 },
      { id: 'qoder', user: 'fixture.person@example.test', window: 'weekly', cap: 0 },
    ], 'both an empty cap and an explicit zero return to inheritance');
    assert.equal(parity.reloginForm.hasProxy, false, 're-login does not invent an extra proxy control');
    assert.ok(parity.reloginForm.identity.includes('fixture.person@example.test'));
    assert.equal(parity.relogin.user, 'fixture.person@example.test', 'explicit re-login sends the original account identity');
    assert.equal(Object.hasOwn(parity.relogin, 'proxy'), false, 'omitted login proxy is not converted to an empty override');
    assert.equal(parity.proxyAfterRelogin, 'http://fixture-draft-proxy.invalid:8118', 'the existing independently saved account proxy remains intact');
    assert.equal(parity.copilotSite, 'fixture-enterprise.ghe.com', 'Copilot enterprise re-login preserves its host');
    assert.equal(parity.copilotSignin.site, 'fixture-enterprise.ghe.com');
    assert.equal(parity.copilotSignin.user, 'fixture-person@fixture-enterprise.ghe.com');
    assert.equal(Object.hasOwn(parity.switchedSignin, 'user'), false, 'switching providers cannot send the previous account identity');
    assert.equal(parity.switchedSignin.agent, 'codex');
    assert.deepEqual(parity.pluginSignin, { agent: 'cursor', plugin: true, method: 0, inputs: {}, user: 'fixture.person@example.test' },
      'the plugin OAuth path preserves the canonical provider and original account');
    assert.deepEqual(parity.claudeRefresh, ['', '?refresh=true'], 'saved Claude can still request explicit refresh through the original account button');
    assert.deepEqual(parity.staleList, { type: 'sources', scope: 'plans', open: true }, 'a late account list cannot replace an immediately reopened source picker');
    assert.deepEqual(parity.staleQuota, { type: 'sources', scope: 'api', open: true, injectedAccountQuota: false },
      'a completed usage read cannot inject data into a closed/replaced account editor');

    const assertSuccess = (success, title, provider) => {
      assert.equal(success.open, true, 'a successful connection remains in its modal until acknowledged');
      assert.equal(success.visible, true, 'success feedback has rendered geometry');
      assert.equal(success.title, title);
      assert.ok(success.detail.includes(provider), 'the success detail names the connected provider');
      assert.equal(success.sourceName, provider, 'the summary identifies the connected provider separately from explanatory copy');
      assert.ok(success.identity, 'the summary retains the account or API source type');
      assert.equal(success.checkedStatus, true, 'a checked, labeled status replaces the solitary decoration');
      assert.equal(success.statusWithinSummary, true, 'the checked status is grouped with the connected source');
      assert.equal(success.whaleRendered, true, 'the next step includes the loaded Whale Isle identity');
      assert.ok(success.nextStep.includes('\u4e0b\u4e00\u6b65'), 'the next action has a visible heading');
      assert.ok(success.detail.includes('\u6a21\u578b\u5df2\u540c\u6b65\u5230\u9cb8\u5c7f'), 'success confirms model synchronization');
      assert.ok(success.instruction.includes('\u6a21\u578b\u9009\u62e9\u5668'), 'the success detail explains the next action');
      assert.ok(success.instruction.includes('\u9cb8\u6865'), 'the next action identifies the desktop model channel');
      assert.equal(success.primary, '\u67e5\u770b\u4f9b\u5e94\u5546');
      assert.equal(success.primaryHidden, false);
      assert.equal(success.primaryDisabled, false);
      assert.equal(success.completion, '\u5b8c\u6210');
      assert.equal(success.fieldCount, 0, 'success replaces the login form instead of leaving resubmittable inputs');
      assert.equal(success.error, '');
    };
    const subscriptionSuccessTitle = '\u8ba2\u9605\u8d26\u53f7\u63a5\u5165\u6210\u529f';
    assertSuccess(result.immediateSuccess, subscriptionSuccessTitle, 'Cursor');
    assertSuccess(result.promptSuccess, subscriptionSuccessTitle, 'Cursor');
    assertSuccess(result.providerSuccess, '\u4f9b\u5e94\u5546\u6dfb\u52a0\u6210\u529f', 'Fixture API');
    assert.equal(result.providerCompletion.selectedTab, 'providers', 'the primary success action opens the provider list');
    assert.ok(result.providerCompletion.names.includes('Fixture API'), 'the new provider is visible after refreshing the list');
    assert.equal(result.providerCompletion.writes, 1, 'acknowledging success does not submit the provider a second time');

    assertSuccess(result.polled[0].success, subscriptionSuccessTitle, 'Claude');
    assert.equal(result.polled[0].polls, 2, 'a waiting status keeps the flow alive until a later done status');
    assertSuccess(result.polled[1].success, '\u8ba2\u9605\u8d26\u53f7\u6388\u6743\u5df2\u66f4\u65b0', 'Codex');
    assert.ok(result.polled[1].success.detail.includes('\u767b\u5f55\u6388\u6743\u5df2\u66f4\u65b0'), 'renewing an existing account is not reported as a new account');
    for (const row of result.polled) {
      assert.ok(row.success.identity.includes('fixture@example.test'), 'the account identity appears in the source summary');
      assert.equal(row.refreshes, 1, 'a completed login refreshes the provider and model state once');
    }
    assert.deepEqual(result.unsuccessful.map(row => row.error), ['fixture sign-in failed', '\u767b\u5f55\u5df2\u53d6\u6d88']);
    for (const row of result.unsuccessful) {
      assert.equal(row.open, true, 'a failed or canceled login remains visible with its error');
      assert.equal(row.visible, false, 'a failed or canceled login never shows success feedback');
      assert.equal(row.primaryHidden, false, 'failed authorization restores the original button');
      assert.equal(row.primaryDisabled, false);
      assert.notEqual(row.completion, '\u5b8c\u6210');
    }
    assert.deepEqual(result.cancellations.map(call => call.path), ['/api/signin/signin-pending-cancel-flow/cancel'],
      'closing successful login feedback never cancels it, while closing an unfinished login still does');
    for (const geometry of [result.desktopAdapterGap, result.narrowAdapterGap]) {
      assert.equal(geometry.gap, 12, 'the install button has a rendered 12px gap below its explanation');
      assert.equal(geometry.visible, true);
      assert.equal(geometry.insideDialog, true, 'the adapter button stays inside the dialog on desktop and narrow windows');
    }
    assert.ok(result.desktopAdapterGap.width > 600);
    assert.equal(result.narrowAdapterGap.width, 420);

    const layout = result.layout;
    assert.equal(layout.windowVisible, true, 'layout checks render the target window visibly');
    assert.equal(layout.windowFocused, false, 'the layout fixture never takes the user input focus');
    assert.equal(layout.pages.length, 50, 'five pages are inspected in five widths and both color schemes');
    assert.deepEqual([...new Set(layout.pages.map(row => row.requestedWidth))], [1120, 820, 600, 420, 380]);
    assert.equal(layout.windowSizes.length, 10, 'each theme and window size retains actual native dimensions');
    for (const size of layout.windowSizes) {
      assert.ok(Math.abs(size.renderer.width - size.requested.width) <= 1 && Math.abs(size.renderer.height - size.requested.height) <= 1, JSON.stringify(size));
      assert.ok(Math.abs(size.renderer.width - size.contentBounds.width) <= 1 && Math.abs(size.renderer.height - size.contentBounds.height) <= 1, JSON.stringify(size));
    }
    for (const row of layout.pages) {
      assert.ok(Math.abs(row.width - row.requestedWidth) <= 1, 'page geometry uses the native window size with at most one pixel of DPI rounding');
      const context = `${row.scheme} ${row.width}px ${row.page}`;
      for (const key of ['documentOverflowX', 'bodyOverflowX', 'mainOverflowX', 'contentOverflowX']) {
        assert.ok(row[key] <= 1, `${context} ${key}: ${row[key]}px; ${JSON.stringify(row.overflowingElements)}`);
      }
      assert.deepEqual(row.overflowingElements, [], `${context} visible controls stay within the viewport`);
    }
    const assertDialog = (dialog, context) => {
      assert.equal(dialog.open, true, `${context} dialog is open`);
      assert.equal(dialog.dialogInsideViewport, true, `${context} dialog stays within the viewport`);
      assert.ok(dialog.bodyOverflowX <= 1, `${context} form body does not overflow horizontally`);
      assert.equal(dialog.fieldsInside, true, `${context} inputs remain inside the form body`);
      assert.ok(dialog.buttons.length >= 2, `${context} has visible dismiss and completion controls`);
      for (const button of dialog.buttons) assert.equal(button.visibleInside, true,
        `${context} ${button.text} remains reachable after scrolling the form`);
      if (dialog.bodyScrollable) assert.ok(dialog.scrollTop > 0, `${context} long form was inspected after scrolling`);
    };
    for (const row of layout.subscriptions) {
      assert.equal(row.methodGap, 20, `${row.scheme} ${row.dialog.width}px provider and method have a 20px field gap`);
      assert.equal(row.keyGap, 20, `${row.scheme} ${row.dialog.width}px method and API key have a 20px field gap`);
      assertDialog(row.dialog, `subscription ${row.scheme} ${row.dialog.width}px`);
    }
    for (const row of layout.callbacks) {
      assert.equal(row.callbackGap, 12, 'the callback submit action is separated from its input');
      assert.deepEqual(row.progressGaps, [12, 12], 'login instructions, link and code retain readable separation without a pending text row');
      assertDialog(row.dialog, `callback ${row.scheme} ${row.dialog.width}px`);
    }
    for (const row of layout.accounts) {
      assert.equal(row.rowCount, 2, 'account geometry includes both an active identity and actionable account');
      assert.equal(row.actionGap, 8, 'account toolbar and its identity explanation have an 8px gap');
      assertDialog(row.dialog, `accounts ${row.scheme} ${row.dialog.width}px`);
    }
    for (const row of layout.keys) {
      assert.equal(row.nameGap, 20, 'the new key section heading is separated from its first field');
      assertDialog(row.table, `key table ${row.scheme} ${row.dialog.width}px`);
      assertDialog(row.dialog, `keys ${row.scheme} ${row.dialog.width}px`);
    }
    for (const row of layout.providers) {
      assert.equal(row.regionGap, 20, 'the conditional region field is separated from credential tools');
      assert.equal(row.workspaceGap, 20, 'the workspace field is separated from its selected region');
      assert.equal(row.proxyGap, 20, 'the conditional proxy field is separated from proxy policy');
      assert.deepEqual(row.sections.map(section => section.id), ['connection','models','allocation','limits','network','compatibility','balance','headers','identity']);
      for (const section of row.sections) assertDialog(section.dialog, `provider ${section.id} ${row.scheme} ${row.dialog.width}px`);
      assertDialog(row.dialog, `provider ${row.scheme} ${row.dialog.width}px`);
    }
    for (const row of layout.routes) {
      assert.equal(row.gridGap, row.dialog.width > 760 ? 24 : 20, 'route identity fields use the workbench section spacing');
      assert.deepEqual(row.sections.map(section => section.id), ['members','routing','conditions','levels']);
      for (const section of row.sections) assertDialog(section.dialog, `route ${section.id} ${row.scheme} ${row.dialog.width}px`);
      assert.ok(Math.abs(row.advancedGap - 16) < 0.01,
        'route advanced settings retain the shared 16px divider inset, allowing subpixel border rounding');
      assertDialog(row.dialog, `route ${row.scheme} ${row.dialog.width}px`);
    }
    for (const row of layout.successes) {
      assertSuccess(row.success, subscriptionSuccessTitle, 'Cursor');
      assert.ok(row.success.identity.includes('fixture.long.account.identity@example.test'), 'the summary keeps a long account identity readable');
      assertDialog(row.dialog, `success ${row.scheme} ${row.dialog.width}px`);
    }
    for (const row of layout.confirmations) {
      assert.equal(row.fieldGap, 20, 'weight input is separated from its confirmation explanation');
      assertDialog(row.dialog, `confirmation ${row.scheme} ${row.dialog.width}px`);
    }
    assert.ok(layout.providers.some(row => row.dialog.bodyScrollable), 'long provider settings exercise form scrolling');
    if (process.env.WHALEBRIDGE_QA_SCREENSHOTS) {
      assert.equal(result.screenshots.length, 16, 'desktop and narrow screenshots include four representative dialogs in both schemes');
      for (const screenshot of result.screenshots) {
        assert.ok(screenshot.width > 0 && screenshot.height > 0, 'hidden capture contains rendered pixels');
        assert.ok(fs.existsSync(screenshot.file), 'the representative screenshot is saved');
      }
    }
  } catch (error) {
    failure = error;
    throw error;
  } finally {
    try {
      const relative = path.relative(tempRoot, path.resolve(profile));
      assert.ok(relative && !relative.startsWith('..') && !path.isAbsolute(relative), 'cleanup remains inside the temporary directory');
      fs.rmSync(profile, { recursive: true, force: true });
    } catch (cleanupError) {
      if (failure) throw new AggregateError([failure, cleanupError], 'WhaleBridge account renderer failed and cleanup failed', { cause: failure });
      throw cleanupError;
    }
  }
});
