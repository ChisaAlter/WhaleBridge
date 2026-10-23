package provider

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/plugin"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

// The migration fixture never executes a plugin or a vendor CLI. It keeps
// synthetic auth in memory and uses the real private component files for
// transaction state and account ordering.
type bridgeMigrationFixture struct {
	t                                 *testing.T
	id, spec, pkg                     string
	provider                          plugin.Provider
	auth                              map[string]map[string]any
	installs, imports, removes, takes int
	installErr                        error
	importer                          func(context.Context, map[string]any) error
	check                             func(context.Context, string) (plugin.Checked, error)
	backErr                           error
}

func bridgeMigration(t *testing.T) *bridgeMigrationFixture {
	t.Helper()
	isolate(t)
	if err := writeLogins(nil); err != nil {
		t.Fatal(err)
	}
	f := &bridgeMigrationFixture{t: t, id: "zed", pkg: "@fixture/zed", spec: filepath.Join(t.TempDir(), "fixture"), auth: map[string]map[string]any{}}
	writeFile(t, filepath.Join(f.spec, "package.json"), map[string]string{"name": f.pkg, "version": "1.0.0"})
	f.provider = plugin.Provider{ID: f.id, Name: "Fixture", Spec: f.spec}
	f.list(true, false)
	oldMovers, oldRetiring := movers, Retiring
	oldInstall, oldProviders, oldRemove := installPlugin, pluginProviders, removePlugin
	oldImport, oldCheck, oldAuths, oldTake, oldRestore := importAuth, checkAccount, pluginAuths, takeAuths, restoreAuth
	oldModels := modelsInUse
	movers = map[string]*mover{f.id: {pkg: f.pkg, min: "1.0.0", agents: []string{f.id}, out: func() ([]Moving, error) {
		var out []Moving
		for _, l := range readLogins() {
			if l.Agent != f.id {
				continue
			}
			var auth map[string]any
			_ = json.Unmarshal(l.Auth, &auth)
			if auth == nil {
				auth = map[string]any{"type": "oauth", "refresh": "native", "accountId": l.User}
			}
			out = append(out, Moving{User: l.User, First: l.First, On: l.On, Lapsed: l.Lapsed != "", Plan: l.Plan, Own: l.own(), Auth: auth})
		}
		slices.SortStableFunc(out, func(a, b Moving) int {
			if a.First && !b.First {
				return -1
			}
			if b.First && !a.First {
				return 1
			}
			return 0
		})
		return out, nil
	}, back: func(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error) {
		if f.backErr != nil {
			return ls, "", f.backErr
		}
		i := backInto(&ls, f.id, user)
		ls[i].Auth = json.RawMessage(jsonText(auth))
		return ls, user, nil
	}}}
	Retiring = []string{f.id}
	installPlugin = func(context.Context, string, string) error { f.installs++; return f.installErr }
	pluginProviders = func(context.Context) ([]plugin.Provider, error) { return []plugin.Provider{f.provider}, nil }
	removePlugin = func(context.Context, string) error { f.removes++; f.list(false, false); return nil }
	importAuth = func(ctx context.Context, _ string, auth map[string]any) (string, error) {
		f.imports++
		if f.importer != nil {
			if err := f.importer(ctx, auth); err != nil {
				return "", err
			}
		}
		key := f.id + "#" + str(auth["accountId"])
		f.auth[key] = maps.Clone(auth)
		return key, nil
	}
	checkAccount = func(ctx context.Context, _ string, key string) (plugin.Checked, error) {
		if f.check != nil {
			return f.check(ctx, key)
		}
		return plugin.Checked{Models: []string{"plugin-only"}}, nil
	}
	pluginAuths = func(string) map[string]map[string]any {
		out := map[string]map[string]any{}
		for k, a := range f.auth {
			out[k] = maps.Clone(a)
		}
		return out
	}
	takeAuths = func(ctx context.Context, _ string, keys []string) (map[string]map[string]any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f.takes++
		out := map[string]map[string]any{}
		for _, k := range keys {
			out[k] = f.auth[k]
			delete(f.auth, k)
		}
		return out, nil
	}
	restoreAuth = func(ctx context.Context, _ string, key string, auth map[string]any) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		f.auth[key] = maps.Clone(auth)
		return nil
	}
	modelsInUse = func(string) []string { return []string{"old-unserved-model"} }
	t.Cleanup(func() {
		movers, Retiring = oldMovers, oldRetiring
		installPlugin, pluginProviders, removePlugin = oldInstall, oldProviders, oldRemove
		importAuth, checkAccount, pluginAuths, takeAuths, restoreAuth = oldImport, oldCheck, oldAuths, oldTake, oldRestore
		modelsInUse = oldModels
		plugin.UseCached(nil)
		Changed()
	})
	return f
}

func (f *bridgeMigrationFixture) list(installed, off bool) {
	f.t.Helper()
	es := []plugin.Entry{}
	if installed {
		es = append(es, plugin.Entry{Spec: f.spec, Off: off})
	}
	writeFile(f.t, filepath.Join(settings.Dir(), "plugins.json"), plugin.List{Plugins: es})
	plugin.UseCached([]plugin.Provider{f.provider})
}
func (f *bridgeMigrationFixture) seed(ls ...savedLogin) {
	f.t.Helper()
	if err := writeLogins(ls); err != nil {
		f.t.Fatal(err)
	}
}
func bridgeSource(user string, first, on bool) savedLogin {
	return savedLogin{Agent: "zed", User: user, First: first, On: on, Owned: true, Auth: json.RawMessage(jsonText(map[string]any{"type": "oauth", "access": "access-" + user, "refresh": "refresh-" + user, "accountId": user}))}
}

func TestWhaleBridgeMigrationRetiredGatesBeforeNativeProbe(t *testing.T) {
	isolate(t)
	if len(retiredBuiltins) != 13 {
		t.Fatalf("retired count=%d", len(retiredBuiltins))
	}
	for _, id := range []string{"zed", "kiro", "cursor", "qoder", QoderCNID, "workbuddy", WorkBuddyAIID, MiMoID, "zcode", "factory", "grok", "devin", CommandCodePlanID} {
		if !BuiltinRetired(id) || !PluginServes(id) || Moved(id) {
			t.Fatalf("retired gate %s", id)
		}
		if err := MoveBack(context.Background(), id); err == nil || WhyOf(err) == nil || WhyOf(err).Code != "retired" {
			t.Fatalf("MoveBack(%s)=%v", id, err)
		}
		if _, err := StartSignInWith(id, SignInOptions{}); err == nil {
			t.Fatalf("native sign-in allowed for %s", id)
		}
	}
	old := cursorStatus
	cursorStatus = &cliIdentity{name: "cursor", at: time.Now(), read: true, user: "native@fixture", plan: "pro", ok: true}
	t.Cleanup(func() { cursorStatus = old })
	if got := WhaleBridgeLogins("cursor"); len(got) != 0 {
		t.Fatalf("retired Cursor exposed native login: %+v", got)
	}
	for _, p := range Accounts() {
		if BuiltinRetired(p.ID) && !p.IsPlugin() {
			t.Fatalf("native retired provider served: %s", p.ID)
		}
	}
}

func TestWhaleBridgeMigrationAutomaticWholeBatchOwnership(t *testing.T) {
	for _, tc := range []struct {
		name     string
		own      bool
		borrowed bool
	}{{"own", true, false}, {"borrowed", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := bridgeMigration(t)
			owned := bridgeSource("owned@fixture", true, true)
			native := savedLogin{Agent: f.id, User: "native@fixture"}
			if !tc.own {
				native = bridgeSource("native@fixture", false, true)
				native.Owned = false
			}
			f.seed(owned, native)
			if tc.borrowed {
				base := movers[f.id].out
				movers[f.id].out = func() ([]Moving, error) {
					as, e := base()
					for i := range as {
						if as[i].User == native.User {
							as[i].Own = false
						}
					}
					return as, e
				}
				// A source row with no private home or auth is borrowed even
				// when an adapter forgot its Moving.Own bit.
				f.seed(owned, savedLogin{Agent: f.id, User: native.User})
			}
			before := jsonText(readLogins())
			results := moveRetiring(context.Background(), whaleBridgeMoveAllowed)
			err := results[f.id]
			if err == nil || WhyOf(err) == nil || WhyOf(err).Code != "native" {
				t.Fatalf("no native diagnostic: %v", err)
			}
			if f.installs != 0 || f.imports != 0 || f.takes != 0 || Moved(f.id) || jsonText(readLogins()) != before {
				t.Fatal("automatic move changed part of a mixed batch")
			}
			cs := MoveCandidates()
			if len(cs) != 1 || cs[0].Accounts != 2 || cs[0].Why == nil || cs[0].Why.Code != "native" {
				t.Fatalf("candidate diagnostic: %+v", cs)
			}
		})
	}
}

func TestWhaleBridgeMigrationLapsedUnservedRetainsOrderAndDiagnostic(t *testing.T) {
	f := bridgeMigration(t)
	a, b, c := bridgeSource("a@fixture", false, true), bridgeSource("b@fixture", true, true), bridgeSource("c@fixture", false, false)
	a.Order, b.Order, c.Order = 2, 1, 3
	c.Lapsed = "saved vendor refusal"
	f.seed(a, b, c)
	if err := setMigration(f.id, func(m *Migration) {
		*m = Migration{State: MoveFailed, At: time.Now().Add(-time.Hour), Err: "old model unavailable", Why: &MoveWhy{Code: "unserved"}}
	}); err != nil {
		t.Fatal(err)
	}
	checked := 0
	f.check = func(context.Context, string) (plugin.Checked, error) {
		checked++
		return plugin.Checked{}, errors.New("fixture vendor offline")
	}
	if err := moveRetiring(context.Background(), whaleBridgeMoveAllowed)[f.id]; err != nil {
		t.Fatal(err)
	}
	m, _ := MigrationOf(f.id)
	if m.State != MovePlugin || len(m.Accounts) != 3 || len(m.Backup) != 3 || m.Why == nil || m.Why.Code != "unserved" || !strings.Contains(m.Err, "vendor offline") {
		t.Fatalf("lost retired move details: %+v", m)
	}
	if checked != 2 {
		t.Fatalf("checked lapsed account: %d", checked)
	}
	if m.Accounts[0].User != "b@fixture" {
		t.Fatal("source first account order lost")
	}
	if m.Backup[0].Order != 1 || m.Backup[1].Order != 2 || m.Backup[2].Order != 3 {
		t.Fatal("backup routing order changed")
	}
	rows := map[string]savedLogin{}
	for _, l := range readLogins() {
		if l.Agent == f.id {
			t.Fatal("committed source remained")
		}
		rows[l.User] = l
	}
	if rows["b@fixture"].Order != 1 || rows["a@fixture"].Order != 2 || rows["c@fixture"].Order != 3 || rows["c@fixture"].Lapsed != "saved vendor refusal" {
		t.Fatalf("source routing order or refusal text lost: %+v", rows)
	}
	if !rows["b@fixture"].First || !rows["a@fixture"].On || rows["c@fixture"].On || rows["c@fixture"].Lapsed == "" {
		t.Fatalf("account switches/lapse lost: %+v", rows)
	}
}

func TestWhaleBridgeMigrationRollbackKeepsRotatedTokenAfterCancel(t *testing.T) {
	f := bridgeMigration(t)
	f.seed(bridgeSource("a@fixture", true, true), bridgeSource("b@fixture", false, false))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.importer = func(_ context.Context, a map[string]any) error {
		if a["accountId"] == "b@fixture" {
			f.auth[f.id+"#a@fixture"]["refresh"] = "rotated-once"
			cancel()
			return errors.New("fixture import failed")
		}
		return nil
	}
	if err := Move(ctx, f.id); err == nil {
		t.Fatal("failed import committed")
	}
	if Moved(f.id) || len(f.auth) != 0 || f.takes != 1 {
		t.Fatalf("rollback left tokens/committed: %+v", f)
	}
	for _, l := range readLogins() {
		if l.User == "a@fixture" {
			var a map[string]any
			_ = json.Unmarshal(l.Auth, &a)
			if a["refresh"] != "rotated-once" {
				t.Fatal("spent token restored over rotated token")
			}
		}
	}
}

func TestWhaleBridgeMigrationFailedRollbackRetainsRecoveryRecord(t *testing.T) {
	f := bridgeMigration(t)
	f.seed(bridgeSource("a@fixture", true, true), bridgeSource("b@fixture", false, true))
	f.list(false, false)
	f.backErr = errors.New("fixture private storage unavailable")
	f.importer = func(_ context.Context, a map[string]any) error {
		if a["accountId"] == "b@fixture" {
			f.auth[f.id+"#a@fixture"]["refresh"] = "rotated-once"
			return errors.New("import failed")
		}
		return nil
	}
	if err := Move(context.Background(), f.id); err == nil {
		t.Fatal("expected failed rollback")
	}
	m, _ := MigrationOf(f.id)
	if m.State != MoveMoving || len(m.Accounts) != 1 || len(f.auth) != 1 || f.removes != 0 {
		t.Fatalf("lost recovery state: %+v auth=%v removes=%d", m, f.auth, f.removes)
	}
	f.backErr = nil
	f.importer = nil
	if err := Move(context.Background(), f.id); err != nil {
		t.Fatal(err)
	}
	m, _ = MigrationOf(f.id)
	if m.State != MovePlugin {
		t.Fatalf("retry did not recover and move: %+v", m)
	}
}

func TestWhaleBridgeMigrationTidyKeepsUnimportedNative(t *testing.T) {
	f := bridgeMigration(t)
	f.seed(bridgeSource("a@fixture", true, true))
	if err := Move(context.Background(), f.id); err != nil {
		t.Fatal(err)
	}
	rows := slices.Clone(readLogins())
	rows = append(rows, savedLogin{Agent: f.id, User: "later-native@fixture"})
	f.seed(rows...)
	tidyMoved(f.id)
	if !slices.ContainsFunc(readLogins(), func(l savedLogin) bool { return l.Agent == f.id && l.User == "later-native@fixture" }) {
		t.Fatal("tidy claimed a native account absent from the transaction")
	}
	m, _ := MigrationOf(f.id)
	if len(m.Backup) != 1 {
		t.Fatal("unimported native was added to migration backup")
	}
}

func TestWhaleBridgeMigrationSourceChangeRollsBackWholeBatch(t *testing.T) {
	f := bridgeMigration(t)
	f.seed(bridgeSource("a@fixture", true, true))
	f.check = func(context.Context, string) (plugin.Checked, error) {
		ls := slices.Clone(readLogins())
		ls = append(ls, savedLogin{Agent: f.id, User: "new-native@fixture"})
		f.seed(ls...)
		return plugin.Checked{}, nil
	}
	if err := moveWithGate(context.Background(), f.id, whaleBridgeMoveAllowed); err == nil || WhyOf(err) == nil || WhyOf(err).Code != "native" {
		t.Fatalf("source change accepted: %v", err)
	}
	if Moved(f.id) || len(f.auth) != 0 {
		t.Fatal("part of changed source committed")
	}
	if len(readLogins()) != 2 {
		t.Fatal("source batch lost accounts")
	}
}

func TestWhaleBridgeMigrationRemoveOffRestorePreservesCredentials(t *testing.T) {
	f := bridgeMigration(t)
	f.seed(bridgeSource("a@fixture", true, true))
	if err := Move(context.Background(), f.id); err != nil {
		t.Fatal(err)
	}
	before := jsonText(f.auth)
	if err := SetPluginOff(context.Background(), f.spec, true); err != nil {
		t.Fatal(err)
	}
	if !Moved(f.id) || !plugin.Load().Plugins[0].Off || jsonText(f.auth) != before {
		t.Fatal("disable moved credentials back")
	}
	f.installs = 0
	keepMovedCurrent(context.Background())
	// Off entries are skipped before consulting any install hook.
	// An entry removed outside this API is also never recreated.
	f.list(false, false)
	keepMovedCurrent(context.Background())
	if f.installs != 0 {
		t.Fatal("removed entry automatically reinstalled")
	}
	f.list(true, false)
	if err := RemovePlugin(context.Background(), f.spec); err != nil {
		t.Fatal(err)
	}
	m, _ := MigrationOf(f.id)
	if m.State != MoveRemoved || len(m.Backup) != 1 || len(m.Accounts) != 1 || jsonText(f.auth) != before {
		t.Fatalf("removal lost credentials: %+v", m)
	}
	f.installs = 0
	moveRetiring(context.Background(), whaleBridgeMoveAllowed)
	keepMovedCurrent(context.Background())
	if f.installs != 0 {
		t.Fatal("removed plugin installed without user action")
	}
	f.list(true, false)
	f.provider.Spec = "@fixture/other"
	if err := WhaleBridgeHandOver(context.Background())[f.id]; err == nil {
		t.Fatal("restored with wrong winning plugin")
	}
	if m, _ := MigrationOf(f.id); m.State != MoveRemoved {
		t.Fatal("wrong package took credentials")
	}
	f.provider.Spec = f.spec
	if err := WhaleBridgeHandOver(context.Background())[f.id]; err != nil {
		t.Fatal(err)
	}
	m, _ = MigrationOf(f.id)
	if !Moved(f.id) || len(m.Backup) != 1 || jsonText(f.auth) != before || f.installs != 0 {
		t.Fatal("explicit reinstall failed to restore record")
	}
}

func TestWhaleBridgeMigrationPluginLeftProgressRestoresPicks(t *testing.T) {
	f := bridgeMigration(t)
	if err := store(file{Providers: []Provider{{ID: "fixture-provider", Models: []string{"model-a"}}, {ID: "user-provider", Chat: "https://fixture.invalid/v1/chat", Key: "private"}}}); err != nil {
		t.Fatal(err)
	}
	if err := setAside(f.pkg, []string{"fixture-provider", "user-provider"}); err != nil {
		t.Fatal(err)
	}
	stored, err := read()
	if err != nil || len(stored.PluginLeft) != 1 || len(stored.Providers) != 1 {
		t.Fatalf("set aside=%+v err=%v", stored, err)
	}
	old := addPluginWithProgress
	t.Cleanup(func() { addPluginWithProgress = old })
	addPluginWithProgress = func(_ context.Context, spec string, downloaded func()) (plugin.Entry, error) {
		downloaded()
		return plugin.Entry{Spec: f.spec}, nil
	}
	calls := 0
	if _, err := AddPluginWithProgress(context.Background(), f.spec, func() { calls++ }); err != nil {
		t.Fatal(err)
	}
	stored, _ = read()
	if calls != 1 || len(stored.PluginLeft) != 0 || len(stored.Providers) != 2 {
		t.Fatalf("progress/restore failed: calls=%d providers=%+v", calls, stored)
	}
}

func TestWhaleBridgeMigrationKeylessAndCLIGates(t *testing.T) {
	f := bridgeMigration(t)
	f.provider.Methods = []plugin.Method{{Type: "api", Keyless: true}, {Type: "api"}, {Type: "oauth", Keyless: true}}
	plugin.UseCached([]plugin.Provider{f.provider})
	old := pluginAPIKey
	t.Cleanup(func() { pluginAPIKey = old })
	reached := errors.New("fixture reached plugin API")
	calls := 0
	pluginAPIKey = func(_ context.Context, id string, method int, inputs map[string]string, key, account string) (plugin.Saved, error) {
		calls++
		if id != f.id || method != 0 || key != "" {
			t.Fatalf("wrong keyless arguments: %s %d %q", id, method, key)
		}
		return plugin.Saved{}, reached
	}
	if _, err := PluginAPIKey(context.Background(), f.id, 0, nil, " "); !errors.Is(err, reached) || calls != 1 {
		t.Fatalf("keyless API was not dispatched: %v", err)
	}
	for _, method := range []int{-1, 1, 2, 3} {
		if _, err := PluginAPIKey(context.Background(), f.id, method, nil, ""); err == nil || errors.Is(err, reached) {
			t.Fatalf("blank key accepted for method %d: %v", method, err)
		}
	}
	if calls != 1 {
		t.Fatal("invalid methods reached plugin")
	}
	for _, id := range []string{"cursor", "grok", "devin"} {
		if _, install := missingPluginCLI(id); install {
			t.Fatalf("component would install %s's vendor CLI", id)
		}
	}
}

func TestWhaleBridgeMigrationFactoryKeyUsesAPIAuth(t *testing.T) {
	f := bridgeMigration(t)
	var imported map[string]any
	importAuth = func(_ context.Context, id string, auth map[string]any) (string, error) {
		if id != "factory" {
			t.Fatal("wrong provider")
		}
		imported = auth
		return "factory#fixture", nil
	}
	r := factoryKeyToPlugin(context.Background(), "factory@fixture", factoryCreds{Access: "fk-fixture-key", Key: true, Email: "factory@fixture"})
	if r.Status != "added" || imported["type"] != "api" || imported["key"] != "fk-fixture-key" {
		t.Fatalf("wrong key shape: %+v %v", r, imported)
	}
	_ = f
}
