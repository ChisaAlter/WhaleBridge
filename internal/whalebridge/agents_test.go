package whalebridge

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/appdir"
	"github.com/ChisaAlter/WhaleBridge/internal/edit"
	"github.com/ChisaAlter/WhaleBridge/internal/gateway"
	"github.com/ChisaAlter/WhaleBridge/internal/library"
	"github.com/ChisaAlter/WhaleBridge/internal/profile"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/testenv"
)

func TestMain(m *testing.M) {
	if marker := os.Getenv("WHALEBRIDGE_AGENT_SPAWN_MARKER"); marker != "" {
		f, err := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			os.Exit(2)
		}
		f.WriteString(strings.Join(os.Args, " ") + "\n")
		f.Close()
		os.Exit(0)
	}
	os.Exit(testenv.RunIn(m, func(home string) {
		// Listing clients must not invoke this computer's WSL or Aside.
		// All executable lookups stay in testenv's stand-in directory.
		if err := os.Setenv("PATH", filepath.Join(home, "bin")); err != nil {
			panic(err)
		}
	}))
}

func TestAgentHTTPDiscoveryDoesNotSpawnOrWriteStash(t *testing.T) {
	a := newAgentHTTP(t)
	home, _ := os.UserHomeDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"powershell.exe", "codex.exe"} {
		if err := os.WriteFile(filepath.Join(bin, name), binary, 0700); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(home, "spawned.txt")
	t.Setenv("WHALEBRIDGE_AGENT_SPAWN_MARKER", marker)
	t.Setenv("PATH", bin)
	config := writeAgentFixture(t, ".codex/config.toml", "model = \"gpt-own\"\n")
	stash := filepath.Join(appdir.Config(), "stash.json")
	if err := os.WriteFile(stash, []byte(`{"keep":"native discovery must remain read only"}`), 0600); err != nil {
		t.Fatal(err)
	}
	beforeConfig, _ := os.ReadFile(config)
	beforeStash, _ := os.ReadFile(stash)
	readAgentRows(t, a)
	if b, err := os.ReadFile(marker); err == nil {
		t.Fatalf("GET agents started a native executable: %s", b)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(config); !bytes.Equal(b, beforeConfig) {
		t.Fatal("GET agents changed Codex config")
	}
	if b, _ := os.ReadFile(stash); !bytes.Equal(b, beforeStash) {
		t.Fatal("GET agents recorded native config change timestamps")
	}
}

func TestAgentHTTPClaudePrivateSignInAndRestore(t *testing.T) {
	a := newAgentHTTP(t)
	originalURL := a.vendor + "/original"
	fixture, _ := json.Marshal(map[string]any{"theme": "dark", "model": "sonnet", "env": map[string]string{"ANTHROPIC_BASE_URL": originalURL, "ANTHROPIC_AUTH_TOKEN": "fixture-original-native-token"}})
	path := writeAgentFixture(t, ".claude/settings.json", string(fixture))
	a.call("POST", "agent/connect", map[string]string{"id": "claude"}, http.StatusOK)
	a.call("POST", "agent/set", map[string]string{"id": "claude", "key": "login", "value": "claudeai"}, http.StatusOK)
	check := func(wantURL, wantToken, login string) {
		t.Helper()
		if u, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL"); u != wantURL {
			t.Fatal("Claude login mode wrote the wrong authenticated endpoint")
		}
		if token, has := edit.GetJSON(path, "env.ANTHROPIC_AUTH_TOKEN"); !has || token != wantToken {
			t.Fatal("Claude login mode wrote the wrong authentication state")
		}
		row := findAgentRow(t, a, "claude")
		if !row.Connected || row.SyncError != "" {
			t.Fatalf("Claude login mode lost ownership: connected=%v error=%s", row.Connected, row.SyncError)
		}
		for _, field := range row.Fields {
			if field.Key == "login" && field.Value != login {
				t.Fatal("Claude login value did not read back")
			}
		}
	}
	check(gateway.WhaleBridgeAnthropicURL(), "", "claudeai")
	a.call("POST", "agent/set", map[string]string{"id": "claude", "key": "model", "value": "test/second-model"}, http.StatusOK)
	syncManagedAgents()
	check(gateway.WhaleBridgeAnthropicURL(), "", "claudeai")
	a.call("POST", "agent/set", map[string]string{"id": "claude", "key": "login", "value": ""}, http.StatusOK)
	check(gateway.URL(), gateway.Token, "")
	a.call("POST", "agent/set", map[string]string{"id": "claude", "key": "login", "value": "claudeai"}, http.StatusOK)
	a.call("POST", "agent/disconnect", map[string]string{"id": "claude"}, http.StatusOK)
	if u, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL"); u != originalURL {
		t.Fatal("Claude original endpoint not restored")
	}
	if token, _ := edit.GetJSON(path, "env.ANTHROPIC_AUTH_TOKEN"); token != "fixture-original-native-token" {
		t.Fatal("Claude original native credential not restored")
	}
	if model, _ := edit.GetJSON(path, "model"); model != "sonnet" {
		t.Fatal("Claude original model not restored")
	}
}

func TestAgentHTTPClaudePrivateSignInPreservesLaterCredentials(t *testing.T) {
	a := newAgentHTTP(t)
	path := writeAgentFixture(t, ".claude/settings.json", `{"model":"sonnet"}`)
	a.call("POST", "agent/connect", map[string]string{"id": "claude"}, http.StatusOK)
	a.call("POST", "agent/set", map[string]string{"id": "claude", "key": "login", "value": "claudeai"}, http.StatusOK)
	laterURL := a.vendor + "/later"
	if err := edit.SetJSON(path, edit.KV{Path: "env.ANTHROPIC_BASE_URL", Value: laterURL}, edit.KV{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: "fixture-later-native-token"}); err != nil {
		t.Fatal(err)
	}
	a.call("POST", "agent/set", map[string]string{"id": "claude", "key": "login", "value": ""}, http.StatusBadRequest)
	a.call("POST", "agent/disconnect", map[string]string{"id": "claude"}, http.StatusBadRequest)
	if u, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL"); u != laterURL {
		t.Fatal("Claude private login overwrote a later endpoint")
	}
	if token, _ := edit.GetJSON(path, "env.ANTHROPIC_AUTH_TOKEN"); token != "fixture-later-native-token" {
		t.Fatal("Claude private login overwrote a later native credential")
	}
}

func TestAgentHTTPManagedProfilesIgnoreUnmanagedAndLibrary(t *testing.T) {
	a := newAgentHTTP(t)
	claude := writeAgentFixture(t, ".claude/settings.json", `{"model":"sonnet"}`)
	codex := writeAgentFixture(t, ".codex/config.toml", "model = \"gpt-own\"\n")
	instructions := writeAgentFixture(t, ".claude/CLAUDE.md", "native instructions stay\n")
	dsh := filepath.Join(os.Getenv("WHALEBRIDGE_DSH_HOME"), "profiles", "whalebridge.json")
	if err := os.MkdirAll(filepath.Dir(dsh), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dsh, []byte(`{"componentManaged":true,"model":"keep-default"}`), 0600); err != nil {
		t.Fatal(err)
	}
	a.call("POST", "agent/connect", map[string]string{"id": "claude"}, http.StatusOK)
	a.call("POST", "agent-profiles/save", map[string]string{"name": "managed"}, http.StatusOK)
	ps, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	p := ps["managed"]
	if p.Library != nil || len(p.Models) != 1 {
		t.Fatal("profile captured library or unmanaged model lists")
	}
	for key := range p.Fields {
		if !strings.HasPrefix(key, "claude.") {
			t.Fatal("profile captured an unmanaged Agent field")
		}
	}
	p.Fields["codex.model"], p.Fields["dsh.model"] = "test/second-model", "test/second-model"
	p.Models["codex"], p.Models["dsh"] = provider.ModelList{Only: true}, provider.ModelList{Only: true}
	p.Library = &library.Setup{Instructions: library.SetupInstructions{Shared: "must never execute", Agents: []string{"claude"}}}
	if err := profile.Save("managed", p); err != nil {
		t.Fatal(err)
	}
	a.call("POST", "agent/set", map[string]string{"id": "claude", "key": "model", "value": "test/second-model"}, http.StatusOK)
	before := map[string][]byte{}
	for _, path := range []string{codex, instructions, dsh} {
		before[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	a.call("POST", "agent-profiles/apply", map[string]string{"name": "managed"}, http.StatusOK)
	if model, _ := edit.GetJSON(claude, "model"); model != p.Fields["claude.model"] {
		t.Fatal("profile did not apply the currently managed field")
	}
	for path, want := range before {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("profile changed excluded file %s: %v", path, err)
		}
	}
	if provider.ModelListOf("codex").Only || provider.ModelListOf("dsh").Only {
		t.Fatal("profile changed an unmanaged/default model list")
	}
	a.call("GET", "agent-profiles", nil, http.StatusOK)
	if err := edit.SetJSON(claude, edit.KV{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: "fixture-later-native-token"}); err != nil {
		t.Fatal(err)
	}
	changed, _ := os.ReadFile(claude)
	a.call("POST", "agent-profiles/apply", map[string]string{"name": "managed"}, http.StatusBadRequest)
	if got, _ := os.ReadFile(claude); !bytes.Equal(got, changed) {
		t.Fatal("profile took over a later native credential")
	}
	a.call("POST", "agent-profiles/delete", map[string]string{"name": "managed"}, http.StatusOK)
	ps, err = profile.Load()
	if err != nil || len(ps) != 0 {
		t.Fatal("profile delete was not persisted")
	}
}

func TestAgentHTTPModelListsStayPerAgentAndKeepInUse(t *testing.T) {
	a := newAgentHTTP(t)
	writeAgentFixture(t, ".claude/settings.json", `{"model":"sonnet"}`)
	writeAgentFixture(t, ".pi/agent/settings.json", `{"defaultProvider":"own","defaultModel":"own-model"}`)
	a.call("POST", "agent-models/pi", map[string]any{"only": true}, http.StatusBadRequest)
	a.call("POST", "agent/connect", map[string]string{"id": "claude"}, http.StatusOK)
	a.call("POST", "agent-models/claude", map[string]any{"shown": []string{"test/second-model"}, "order": []string{"test/second-model", "test/test-model"}, "only": true, "ownHidden": true}, http.StatusOK)
	l := provider.ModelListOf("claude")
	if !l.Only || !slices.Contains(l.Picked, "test/test-model") || !slices.Contains(l.Picked, "test/second-model") || !slices.Equal(l.Order, []string{"test/second-model", "test/test-model"}) {
		t.Fatalf("per-Agent list lost active selection/order: %+v", l)
	}
	if !provider.OwnModelsHidden("claude") || !provider.ModelListOf("pi").Default() || !provider.ModelListOf("dsh").Default() {
		t.Fatal("model list visibility affected another Agent")
	}
	if err := provider.Save(provider.Provider{ID: "fast", Name: "Fast relay", Key: "fixture", Chat: a.vendor + "/v1", Models: []string{"gpt-6.1-sol"}}); err != nil {
		t.Fatal(err)
	}
	a.call("POST", "agent-fast", map[string]any{"id": "claude", "for": "codex", "ref": "fast/gpt-6.1-sol", "fast": true}, http.StatusBadRequest)
	a.call("POST", "agent-fast", map[string]any{"id": "claude", "for": "claude", "ref": "fast/gpt-6.1-sol", "fast": true}, http.StatusOK)
	if !provider.IsFastPick("claude", "fast/gpt-6.1-sol") || provider.IsFastPick("codex", "fast/gpt-6.1-sol") {
		t.Fatal("Fast choice did not stay per Agent")
	}
	var state struct {
		Models    []agentModel `json:"models"`
		OwnHidden bool         `json:"ownHidden"`
	}
	if err := json.Unmarshal(a.call("GET", "agent-models/claude", nil, http.StatusOK), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Models) < 3 || state.Models[0].ID != "test/second-model" || !state.OwnHidden {
		t.Fatal("model list readback lost order or own visibility")
	}
	a.call("POST", "agent-models/claude", map[string]any{"order": []string{}}, http.StatusOK)
	if len(provider.ModelListOf("claude").Order) != 0 {
		t.Fatal("default model ordering did not restore")
	}
}

func TestAgentHTTPNewBuiltinsAreDiscoverable(t *testing.T) {
	a := newAgentHTTP(t)
	rows := readAgentRows(t, a)
	if len(rows) != 58 {
		t.Fatalf("builtin Agent count = %d, want 58", len(rows))
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	for _, id := range []string{"deveco", "stepcode", "prime-agent", "copilot-jetbrains", "qwen", "ante", "astrbot", "jcode", "grok-app", "workbuddy-ai", "snow"} {
		if !slices.Contains(ids, id) {
			t.Fatalf("new builtin %s missing from actual Agent endpoint", id)
		}
	}
}

func newAgentHTTP(t *testing.T) providerHTTP {
	t.Helper()
	a := newProviderHTTP(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	was := gateway.Token
	gateway.Token = os.Getenv("WHALEBRIDGE_GATEWAY_TOKEN")
	agentSyncErrors = map[string]string{}
	t.Cleanup(func() { gateway.Token = was; agentSyncErrors = map[string]string{} })
	if err := provider.Save(provider.Provider{ID: "test", Name: "Test", Chat: a.vendor + "/v1/chat/completions", Key: "supplier-secret", Models: []string{"test-model", "second-model"}}); err != nil {
		t.Fatal(err)
	}
	if err := edit.WriteAtomic(filepath.Join(appdir.Config(), "gateway.key"), []byte(gateway.Token)); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	agentRoutes(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	a.url = server.URL
	return a
}

func writeAgentFixture(t *testing.T, name, contents string) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readAgentRows(t *testing.T, a providerHTTP) []agentRow {
	t.Helper()
	b := a.call("GET", "agents", nil, http.StatusOK)
	assertSecretsAbsent(t, b, gateway.Token, "supplier-secret")
	var out struct {
		DefaultAgent string     `json:"defaultAgent"`
		Agents       []agentRow `json:"agents"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.DefaultAgent != "dsh" || len(out.Agents) == 0 || out.Agents[0].ID != "dsh" || out.Agents[0].Name != "鲸屿" || !out.Agents[0].Connected {
		t.Fatalf("default WhaleIsle connection missing: %+v", out)
	}
	return out.Agents
}

func findAgentRow(t *testing.T, a providerHTTP, id string) agentRow {
	t.Helper()
	for _, row := range readAgentRows(t, a) {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("missing agent row %q", id)
	return agentRow{}
}

func TestAgentHTTPReadOnlyDiscovery(t *testing.T) {
	a := newAgentHTTP(t)
	files := map[string][]byte{}
	for name, data := range map[string]string{
		".claude/settings.json":           "{\n  // keep my settings\n  \"theme\": \"light\", \"model\": \"sonnet\"\n}\n",
		".codex/config.toml":              "# keep my Codex settings\nmodel = \"gpt-own\"\napproval_policy = \"on-request\"\n",
		".config/opencode/opencode.jsonc": "{\n // keep my providers\n \"model\": \"own/model\", \"theme\": \"system\"\n}\n",
	} {
		files[writeAgentFixture(t, name, data)] = []byte(data)
	}
	rows := readAgentRows(t, a)
	for _, row := range rows[1:] {
		if row.Connected {
			t.Fatalf("discovery silently connected %s", row.ID)
		}
	}
	for path, before := range files {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("discovery changed %s: %v", path, err)
		}
	}
	if _, err := os.Stat(managedAgentsPath()); !os.IsNotExist(err) {
		t.Fatalf("discovery wrote connection grants: %v", err)
	}
}

func TestAgentHTTPConnectSetDisconnect(t *testing.T) {
	for _, id := range []string{"claude", "codex", "opencode"} {
		t.Run(id, func(t *testing.T) {
			a := newAgentHTTP(t)
			var path string
			switch id {
			case "claude":
				path = writeAgentFixture(t, ".claude/settings.json", "{\n // user comment\n \"theme\":\"light\",\"model\":\"sonnet\"\n}\n")
			case "codex":
				path = writeAgentFixture(t, ".codex/config.toml", "# user comment\nmodel = \"gpt-own\"\napproval_policy = \"on-request\"\n")
			case "opencode":
				path = writeAgentFixture(t, ".config/opencode/opencode.jsonc", "{\n // user comment\n \"model\":\"own/model\",\"theme\":\"system\"\n}\n")
			}
			a.call("POST", "agent/connect", map[string]string{"id": id}, http.StatusOK)
			row := findAgentRow(t, a, id)
			if !row.Connected {
				t.Fatalf("connect was not persisted: %+v", row)
			}
			value := "test/second-model"
			if id == "opencode" {
				value = "magpie/" + value
			}
			a.call("POST", "agent/set", map[string]string{"id": id, "key": "model", "value": value}, http.StatusOK)
			if id == "codex" {
				catalog, _ := edit.GetTOMLTop(path, "model_catalog_json")
				if !filepath.IsAbs(catalog) {
					catalog = filepath.Join(filepath.Dir(path), catalog)
				}
				contents, err := os.ReadFile(catalog)
				var parsed struct {
					Models []json.RawMessage `json:"models"`
				}
				if err != nil || json.Unmarshal(contents, &parsed) != nil || len(parsed.Models) == 0 || !bytes.Contains(contents, []byte("test/second-model")) {
					t.Fatalf("Codex cannot read its generated model catalog: %q %v", catalog, err)
				}
			}
			a.call("POST", "agent/disconnect", map[string]string{"id": id}, http.StatusOK)
			if findAgentRow(t, a, id).Connected {
				t.Fatal("disconnect left the row connected")
			}
			b, err := os.ReadFile(path)
			if err != nil || !bytes.Contains(b, []byte("user comment")) {
				t.Fatalf("user formatting lost: %v", err)
			}
			if id == "codex" {
				if model, _ := edit.GetTOMLTop(path, "model"); model != "gpt-own" {
					t.Fatalf("Codex model was not restored: %q", model)
				}
				if policy, _ := edit.GetTOMLTop(path, "approval_policy"); policy != "on-request" {
					t.Fatal("Codex's unrelated setting changed")
				}
			} else {
				want := "sonnet"
				if id == "opencode" {
					want = "own/model"
				}
				if model, _ := edit.GetJSON(path, "model"); model != want {
					t.Fatalf("%s model was not restored: %q", id, model)
				}
			}
			ids, err := managedAgents()
			if err != nil || slices.Contains(ids, id) {
				t.Fatalf("disconnect retained catalog grant: %v %v", ids, err)
			}
		})
	}
}

func TestAgentHTTPOriginalMagpieIsPreserved(t *testing.T) {
	a := newAgentHTTP(t)
	contents := `{"model":"magpie/test/test-model","theme":"system","provider":{"magpie":{"options":{"baseURL":"http://127.0.0.1:12345/v1","apiKey":"magpie"},"models":{"test/test-model":{}}}}}`
	path := writeAgentFixture(t, ".config/opencode/opencode.json", contents)
	if findAgentRow(t, a, "opencode").Connected {
		t.Fatal("original Magpie was reported as WhaleBridge")
	}
	syncManagedAgents()
	a.call("POST", "agent/connect", map[string]string{"id": "opencode"}, http.StatusBadRequest)
	a.call("POST", "agent/set", map[string]string{"id": "opencode", "key": "model", "value": "magpie/test/second-model"}, http.StatusBadRequest)
	after, err := os.ReadFile(path)
	if err != nil || string(after) != contents {
		t.Fatalf("original Magpie configuration was changed: %v", err)
	}
}

func TestAgentHTTPSyncErrorAndValidation(t *testing.T) {
	a := newAgentHTTP(t)
	path := writeAgentFixture(t, ".config/opencode/opencode.json", `{"model":"own/model"}`)
	a.call("POST", "agent/connect", map[string]string{"id": "opencode"}, http.StatusOK)
	a.call("POST", "agent/set", map[string]string{"id": "opencode", "key": "unknown", "value": "x"}, http.StatusBadRequest)
	a.call("POST", "agent/set", map[string]string{"id": "opencode", "key": "model", "value": "magpie/missing/model"}, http.StatusBadRequest)
	a.call("POST", "agent/disconnect", map[string]string{"id": "dsh"}, http.StatusBadRequest)
	if err := edit.SetJSON(path, edit.KV{Path: "provider.magpie.options.apiKey", Value: "changed"}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	syncManagedAgents()
	row := findAgentRow(t, a, "opencode")
	if row.Connected || row.SyncError == "" {
		t.Fatalf("broken wiring was not shown: %+v", row)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("sync replaced a user edit")
	}
}

func TestAgentUninstallStopsBeforeWhaleIsleOnRestoreFailure(t *testing.T) {
	a := newAgentHTTP(t)
	path := writeAgentFixture(t, ".claude/settings.json", `{"model":"sonnet","theme":"light"}`)
	a.call("POST", "agent/connect", map[string]string{"id": "claude"}, http.StatusOK)
	if err := syncDSH(); err != nil {
		t.Fatal(err)
	}
	dshPath := profilePath(os.Getenv("WHALEBRIDGE_DSH_HOME"))
	dshBefore, err := os.ReadFile(dshPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveManagedAgents([]string{"claude", "unknown-agent"}); err != nil {
		t.Fatal(err)
	}
	err = Disconnect(os.Getenv("WHALEBRIDGE_DSH_HOME"))
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("uninstall ignored an external restore failure: %v", err)
	}
	if model, _ := edit.GetJSON(path, "model"); model != "sonnet" {
		t.Fatalf("successful earlier restore was not kept: %q", model)
	}
	ids, err := managedAgents()
	if err != nil || !slices.Equal(ids, []string{"unknown-agent"}) {
		t.Fatalf("remaining restore records were lost: %v %v", ids, err)
	}
	dshAfter, err := os.ReadFile(dshPath)
	if err != nil || !bytes.Equal(dshBefore, dshAfter) {
		t.Fatalf("failed uninstall changed WhaleIsle's route: %v", err)
	}
}

func TestAgentHTTPImportAndLaunchRequireExplicitGeneration(t *testing.T) {
	t.Run("Cindy", func(t *testing.T) {
		a := newAgentHTTP(t)
		configDir, err := os.UserConfigDir()
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(configDir, "Cindy", "cindy-local-v1.db")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if _, err := db.Exec(`CREATE TABLE custom_providers (id TEXT, name TEXT, runtimes TEXT)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO custom_providers VALUES (?,?,?)`, "magpie", "Magpie", gateway.URL()); err != nil {
			t.Fatal(err)
		}
		row := findAgentRow(t, a, "cindy")
		if !row.Importable || row.Added || row.Connected {
			t.Fatalf("original provider was mistaken for WhaleBridge: %+v", row)
		}
		a.call("POST", "agent/connect", map[string]string{"id": "cindy"}, http.StatusBadRequest)
		b := a.call("POST", "agent/import", map[string]string{"id": "cindy"}, http.StatusOK)
		var result struct {
			Link  string `json:"link"`
			Added bool   `json:"added"`
		}
		if json.Unmarshal(b, &result) != nil || result.Added {
			t.Fatalf("generation claimed import completion: %s", b)
		}
		link, err := url.Parse(result.Link)
		if err != nil || link.Scheme != "cindy" {
			t.Fatalf("invalid Cindy import link: %v", err)
		}
		payload, err := base64.RawURLEncoding.DecodeString(link.Query().Get("data"))
		if err != nil {
			t.Fatal(err)
		}
		var imported struct {
			ID   string `json:"id"`
			Auth struct {
				APIKey string `json:"apiKey"`
			} `json:"auth"`
		}
		if json.Unmarshal(payload, &imported) != nil || imported.ID != "whalebridge" || imported.Auth.APIKey != gateway.TokenFor("cindy") {
			t.Fatal("generated Cindy link does not contain its usable private provider")
		}
		// Stand in for the separate confirmation Cindy owns; the HTTP action
		// above must only generate a link, never write its private database.
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM custom_providers`).Scan(&count); err != nil || count != 1 {
			t.Fatal("link generation wrote Cindy's database")
		}
		if _, err := db.Exec(`INSERT INTO custom_providers VALUES (?,?,?)`, "whalebridge", "鲸桥", gateway.URL()); err != nil {
			t.Fatal(err)
		}
		row = findAgentRow(t, a, "cindy")
		if !row.Added || !row.Connected {
			t.Fatalf("Cindy's own confirmed import was not read back: %+v", row)
		}
	})
	t.Run("Antigravity CLI", func(t *testing.T) {
		a := newAgentHTTP(t)
		path := writeAgentFixture(t, ".gemini/antigravity-cli/settings.json", `{"model":"own"}`)
		a.call("POST", "agent/launch", map[string]string{"id": "agy"}, http.StatusBadRequest)
		a.call("POST", "agent/connect", map[string]string{"id": "agy"}, http.StatusOK)
		if !findAgentRow(t, a, "agy").Launchable {
			t.Fatal("connected environment-only client has no launch entry")
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		b := a.call("POST", "agent/launch", map[string]string{"id": "agy"}, http.StatusOK)
		var result struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(b, &result) != nil || !strings.Contains(result.Command, gateway.TokenFor("agy")) || !strings.Contains(result.Command, "agy --model") {
			t.Fatal("explicit launch generation returned an unusable command")
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("launch generation wrote a client configuration")
		}
		a.call("POST", "agent/import", map[string]string{"id": "agy"}, http.StatusBadRequest)
		a.call("POST", "agent/launch", map[string]string{"id": "claude"}, http.StatusBadRequest)
	})
}

func TestAgentHTTPLaterCredentialsStayUntouched(t *testing.T) {
	for _, id := range []string{"qoder", "workbuddy"} {
		t.Run(id, func(t *testing.T) {
			a := newAgentHTTP(t)
			var path string
			if id == "qoder" {
				path = writeAgentFixture(t, ".qoder/settings.json", `{"model":{"name":"native/own"}}`)
			} else {
				path = writeAgentFixture(t, ".workbuddy/models.json", `[]`)
			}
			a.call("POST", "agent/connect", map[string]string{"id": id}, http.StatusOK)
			if err := syncDSH(); err != nil {
				t.Fatal(err)
			}
			dshPath := profilePath(os.Getenv("WHALEBRIDGE_DSH_HOME"))
			dshBefore, err := os.ReadFile(dshPath)
			if err != nil {
				t.Fatal(err)
			}
			changedCredential := "user-replaced-secret"
			if id == "qoder" {
				if err := edit.SetJSON(path, edit.KV{Path: "providers.magpie.apiKey", Value: changedCredential}); err != nil {
					t.Fatal(err)
				}
			} else {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var entries []map[string]any
				if err := json.Unmarshal(raw, &entries); err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					entry["apiKey"] = changedCredential
				}
				raw, err = json.Marshal(entries)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			syncManagedAgents()
			row := findAgentRow(t, a, id)
			if row.Connected || row.SyncError == "" || strings.Contains(row.SyncError, changedCredential) {
				t.Fatalf("changed credentials weren't protected: %+v", row)
			}
			b := a.call("POST", "agent/disconnect", map[string]string{"id": id}, http.StatusBadRequest)
			assertSecretsAbsent(t, b, gateway.Token, changedCredential)
			if err := Disconnect(os.Getenv("WHALEBRIDGE_DSH_HOME")); err == nil {
				t.Fatal("uninstall ignored ownership loss")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("catalog sync or disconnect replaced a later credential")
			}
			dshAfter, err := os.ReadFile(dshPath)
			if err != nil || !bytes.Equal(dshBefore, dshAfter) {
				t.Fatal("uninstall failure changed WhaleIsle")
			}
			ids, err := managedAgents()
			if err != nil || !slices.Contains(ids, id) {
				t.Fatal("failed restoration lost its connection record")
			}
		})
	}
}

func TestAgentNativePickThenDisconnectRemovesOnlyOwnedCatalog(t *testing.T) {
	for _, id := range []string{"opencode", "codebuddy", "qoder", "qoder-cn"} {
		for _, action := range []string{"disconnect", "uninstall"} {
			t.Run(id+"/"+action, func(t *testing.T) {
				a := newAgentHTTP(t)
				var catalogPath, settingsPath string
				modelKey := "model"
				if id == "opencode" {
					catalogPath = writeAgentFixture(t, ".config/opencode/opencode.jsonc", "{\n // personal settings\n \"model\":\"own/start\",\"theme\":\"light\",\"provider\":{\"own\":{\"options\":{\"baseURL\":\"https://own.example\",\"apiKey\":\"private-native\"}}}\n}\n")
					settingsPath = catalogPath
				} else if strings.HasPrefix(id, "qoder") {
					dir := ".qoder"
					if id == "qoder-cn" {
						dir = ".qoder-cn"
					}
					catalogPath = writeAgentFixture(t, dir+"/settings.json", `{"model":{"name":"own/start"},"theme":"light","providers":{"own":{"baseUrl":"https://own.example","apiKey":"private-native"}}}`)
					settingsPath, modelKey = catalogPath, "model.name"
				} else {
					catalogPath = writeAgentFixture(t, ".codebuddy/models.json", `{"models":[{"id":"own/start","vendor":"Custom","url":"https://own.example","apiKey":"private-native"}],"customProperty":"keep"}`)
					settingsPath = writeAgentFixture(t, ".codebuddy/settings.json", `{"model":"own/start","theme":"light"}`)
				}
				a.call("POST", "agent/connect", map[string]string{"id": id}, http.StatusOK)
				if !findAgentRow(t, a, id).Connected {
					t.Fatal("fixture failed to connect")
				}
				// The user's own client can change the selected model without
				// removing the catalog WhaleBridge previously registered.
				if err := edit.SetJSON(settingsPath, edit.KV{Path: modelKey, Value: "own/after"}); err != nil {
					t.Fatal(err)
				}
				row := findAgentRow(t, a, id)
				if row.Connected || !row.Managed {
					t.Fatal("native pick must retain the cleanup grant without claiming a routed model")
				}
				if action == "disconnect" {
					a.call("POST", "agent/disconnect", map[string]string{"id": id}, http.StatusOK)
				} else {
					if err := syncDSH(); err != nil {
						t.Fatal(err)
					}
					if err := Disconnect(os.Getenv("WHALEBRIDGE_DSH_HOME")); err != nil {
						t.Fatal(err)
					}
					doc, err := profileDocument(os.Getenv("WHALEBRIDGE_DSH_HOME"))
					if err != nil {
						t.Fatal(err)
					}
					if route := member(member(member(configRow(doc, "llm-pi-ai"), "config"), "providers"), "whalebridge"); route != nil {
						t.Fatal("successful uninstall retained WhaleIsle's route")
					}
				}
				if model, _ := edit.GetJSON(settingsPath, modelKey); model != "own/after" {
					t.Fatalf("user's latest native model changed: %q", model)
				}
				if theme, _ := edit.GetJSON(settingsPath, "theme"); theme != "light" {
					t.Fatal("user's unrelated setting changed")
				}
				if id == "opencode" {
					if _, ok := edit.GetJSON(catalogPath, "provider.magpie"); ok {
						t.Fatal("owned provider remained after disconnect")
					}
					if key, _ := edit.GetJSON(catalogPath, "provider.own.options.apiKey"); key != "private-native" {
						t.Fatal("native provider credential changed")
					}
					if raw, _ := os.ReadFile(catalogPath); !bytes.Contains(raw, []byte("personal settings")) {
						t.Fatal("native file comment was lost")
					}
				} else if strings.HasPrefix(id, "qoder") {
					if _, ok := edit.GetJSON(catalogPath, "providers.magpie"); ok {
						t.Fatal("owned Qoder provider remained after cleanup")
					}
					if key, _ := edit.GetJSON(catalogPath, "providers.own.apiKey"); key != "private-native" {
						t.Fatal("native Qoder provider credential changed")
					}
				} else {
					models, _ := edit.GetJSON(catalogPath, "models")
					var entries []map[string]any
					if json.Unmarshal([]byte(models), &entries) != nil || len(entries) != 1 || entries[0]["id"] != "own/start" || entries[0]["apiKey"] != "private-native" {
						t.Fatal("cleanup did not preserve only the original native model entry")
					}
					if custom, _ := edit.GetJSON(catalogPath, "customProperty"); custom != "keep" {
						t.Fatal("native model file property changed")
					}
				}
				ids, err := managedAgents()
				if err != nil || slices.Contains(ids, id) {
					t.Fatal("successful cleanup retained the connection grant")
				}
			})
		}
	}
}

func TestAgentHTTPForeignProviderWithNativeModelIsPreserved(t *testing.T) {
	for _, id := range []string{"opencode", "codebuddy", "qoder", "qoder-cn"} {
		t.Run(id, func(t *testing.T) {
			a := newAgentHTTP(t)
			var path string
			switch id {
			case "opencode":
				path = writeAgentFixture(t, ".config/opencode/opencode.json", `{"model":"native/own","provider":{"magpie":{"options":{"baseURL":"http://127.0.0.1:12345/v1","apiKey":"original-provider-secret"}}}}`)
			case "codebuddy":
				path = writeAgentFixture(t, ".codebuddy/models.json", `{"models":[{"id":"test/test-model","vendor":"magpie","url":"http://127.0.0.1:12345/v1/chat/completions","apiKey":"original-provider-secret"}]}`)
				writeAgentFixture(t, ".codebuddy/settings.json", `{"model":"native/own"}`)
			default:
				dir := ".qoder"
				if id == "qoder-cn" {
					dir = ".qoder-cn"
				}
				path = writeAgentFixture(t, dir+"/settings.json", `{"model":{"name":"native/own"},"providers":{"magpie":{"baseUrl":"http://127.0.0.1:12345/v1","apiKey":"original-provider-secret"}}}`)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			row := findAgentRow(t, a, id)
			if row.Connected || row.Managed {
				t.Fatal("a foreign provider was claimed by WhaleBridge")
			}
			for _, action := range []string{"connect", "set"} {
				b := a.call("POST", "agent/"+action, map[string]string{"id": id, "key": "model", "value": "magpie/test/second-model"}, http.StatusBadRequest)
				assertSecretsAbsent(t, b, gateway.Token, "original-provider-secret")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("connect or set replaced a foreign provider while a native model was selected")
			}
			ids, err := managedAgents()
			if err != nil || slices.Contains(ids, id) {
				t.Fatal("failed connection recorded an editing grant")
			}
		})
	}
}
