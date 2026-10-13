package whalebridge

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ChisaAlter/WhaleBridge/internal/agent"
	"github.com/ChisaAlter/WhaleBridge/internal/appdir"
	"github.com/ChisaAlter/WhaleBridge/internal/edit"
	"github.com/ChisaAlter/WhaleBridge/internal/gateway"
)

type agentOption struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
	Note  string `json:"note"`
}

type agentField struct {
	Key     string        `json:"key"`
	Label   string        `json:"label"`
	Value   string        `json:"value"`
	Options []agentOption `json:"options"`
	Quiet   bool          `json:"quiet"`
}

type agentRow struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Detected   bool         `json:"detected"`
	Connected  bool         `json:"connected"`
	Managed    bool         `json:"managed"`
	Path       string       `json:"path"`
	Notice     string       `json:"notice"`
	Fields     []agentField `json:"fields"`
	SyncError  string       `json:"syncError,omitempty"`
	Importable bool         `json:"importable"`
	Added      bool         `json:"added"`
	Launchable bool         `json:"launchable"`
}

// The launcher owns WhaleIsle's profile. Its default connection never passes
// through the general-purpose DSH adapter, which writes a different route.
func externalAgent(a *agent.Agent) bool {
	id, _, _ := strings.Cut(a.ID, "@wsl:")
	return id != "dsh"
}

func managedAgentsPath() string { return filepath.Join(appdir.Config(), "agents.json") }

// Only a connection the user explicitly made here receives catalog updates.
// An existing Magpie configuration on the machine is not a grant to edit it.
func managedAgents() ([]string, error) {
	b, err := os.ReadFile(managedAgentsPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal(b, &ids); err != nil {
		return nil, fmt.Errorf("接入状态文件无效: %w", err)
	}
	return ids, nil
}

func saveManagedAgents(ids []string) error {
	slices.Sort(ids)
	ids = slices.Compact(ids)
	b, err := json.MarshalIndent(ids, "", "  ")
	if err != nil {
		return err
	}
	return edit.WriteAtomic(managedAgentsPath(), append(b, '\n'))
}

// Access is serialized by mutations, alongside agent config writes.
var agentSyncErrors = map[string]string{}

// Some upstream notices include an endpoint or API key as setup advice. The
// component's management API gives no caller its private gateway credential.
func agentText(value string) string {
	for _, token := range []string{os.Getenv("WHALEBRIDGE_GATEWAY_TOKEN"), gateway.Token} {
		if token != "" && token != "magpie" {
			value = strings.ReplaceAll(value, token, "[鲸桥密钥]")
		}
	}
	return value
}

func agentFailure(w http.ResponseWriter, err error, status int) {
	http.Error(w, agentText(err.Error()), status)
}

func syncManagedAgents() {
	ids, err := managedAgents()
	if err != nil {
		agentSyncErrors["*"] = err.Error()
		return
	}
	delete(agentSyncErrors, "*")
	for _, id := range ids {
		a, err := agent.Find(id)
		if err == nil && externalAgent(a) && a.Wired() {
			err = agentWiringError(a)
			if err == nil && a.Sync != nil {
				err = a.Sync()
			}
		}
		delete(agentSyncErrors, id)
		if err != nil {
			agentSyncErrors[id] = err.Error()
		}
	}
}

func agentWiringError(a *agent.Agent) error {
	if a.Check != nil {
		if detail := a.Check(); detail != "" {
			return fmt.Errorf("%s 的现有配置指向其他网关或已更改，请先在原应用断开连接: %s", a.Name, detail)
		}
	}
	return nil
}

func disconnectExternalAgent(a *agent.Agent) error {
	if err := agentWiringError(a); err != nil {
		return err
	}
	if a.Wired() {
		return a.Disconnect()
	}
	id, _, _ := strings.Cut(a.ID, "@wsl:")
	// A native model picked in these clients can leave our model list behind.
	// Remove only that credential-checked list, preserving the current model.
	if (id == "opencode" || id == "codebuddy" || id == "qoder" || id == "qoder-cn") && a.Unwire != nil {
		if err := a.Unwire(); err != nil {
			return err
		}
		a.Keep()
		return nil
	}
	return a.Disconnect()
}

func disconnectManagedAgents() error {
	ids, err := managedAgents()
	if err != nil || len(ids) == 0 {
		return err
	}
	// Uninstall runs in a new CLI process. Restore with the same key that
	// created these configurations, without generating a replacement key.
	stored, err := os.ReadFile(filepath.Join(appdir.Config(), "gateway.key"))
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(stored))
	if key, err := hex.DecodeString(token); err != nil || len(key) != 16 {
		return fmt.Errorf("鲸桥网关密钥文件无效")
	}
	gateway.Token = token
	os.Setenv("WHALEBRIDGE_GATEWAY_TOKEN", token)
	for len(ids) > 0 {
		id := ids[0]
		a, err := agent.Find(id)
		if err != nil {
			return err
		}
		if !externalAgent(a) {
			return fmt.Errorf("外部 Agent 接入记录不能包含鲸屿")
		}
		if err := disconnectExternalAgent(a); err != nil {
			return fmt.Errorf("恢复 %s 失败: %s", a.Name, agentText(err.Error()))
		}
		ids = ids[1:]
		if err := saveManagedAgents(ids); err != nil {
			return fmt.Errorf("%s 已恢复，但接入状态保存失败: %w", a.Name, err)
		}
		delete(agentSyncErrors, id)
	}
	return nil
}

func agentRows() []agentRow {
	ids, registryError := managedAgents()
	rows := []agentRow{{
		ID: "dsh", Name: "鲸屿", Detected: true, Connected: true,
		Path:   os.Getenv("WHALEBRIDGE_DSH_HOME"),
		Notice: "默认接入鲸屿，供应商与模型变更会自动同步。", Fields: []agentField{},
		SyncError: dshSyncError(),
	}}
	for _, a := range agent.All() {
		if !externalAgent(a) {
			continue
		}
		vals := a.Values()
		row := agentRow{ID: a.ID, Name: agentText(a.Name), Detected: a.Detected(), Managed: slices.Contains(ids, a.ID), Connected: slices.Contains(ids, a.ID) && a.Wired(), Path: agentText(a.Path), Fields: []agentField{}, SyncError: agentText(agentSyncErrors[a.ID])}
		row.Importable = a.Import != nil
		row.Added = a.Added != nil && a.Added()
		row.Launchable = a.Launch != nil && a.Launch() != ""
		if row.Importable {
			row.Connected = row.Added
		}
		if row.Connected {
			if err := agentWiringError(a); err != nil {
				row.Connected = false
				row.SyncError = agentText(err.Error())
			}
		}
		if registryError != nil {
			row.SyncError = agentText(registryError.Error())
		}
		if row.SyncError == "" {
			row.SyncError = agentText(agentSyncErrors["*"])
		}
		if a.Notice != nil {
			row.Notice = agentText(a.Notice())
		}
		for _, f := range a.Fields {
			options := []agentOption{}
			if f.Options != nil {
				for _, option := range f.Options(vals) {
					options = append(options, agentOption{Value: agentText(option.Value), Label: agentText(option.Label), Note: agentText(option.Note)})
				}
			}
			row.Fields = append(row.Fields, agentField{Key: f.Key, Label: agentText(f.Label), Value: agentText(vals[f.Key]), Options: options, Quiet: f.Quiet})
		}
		rows = append(rows, row)
	}
	return rows
}

func agentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agents", func(w http.ResponseWriter, r *http.Request) {
		mutations.Lock()
		defer mutations.Unlock()
		writeJSON(w, map[string]any{"defaultAgent": "dsh", "agents": agentRows()})
	})
	for _, action := range []string{"connect", "disconnect", "set", "import", "launch"} {
		mux.HandleFunc("POST /api/agent/"+action, agentAction(action))
	}
}

func agentAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ID    string `json:"id"`
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := decode(w, r, &input); err != nil {
			agentFailure(w, err, http.StatusBadRequest)
			return
		}
		mutations.Lock()
		defer mutations.Unlock()
		a, err := agent.Find(input.ID)
		if err != nil {
			agentFailure(w, err, http.StatusBadRequest)
			return
		}
		if !externalAgent(a) {
			http.Error(w, "鲸屿由启动器自动接入，请在模型管理中设置。", http.StatusBadRequest)
			return
		}
		// These products accept their own import link or launch command.
		// Only an explicit generation action reveals that private artifact.
		if action == "import" {
			if a.Import == nil {
				http.Error(w, "这个 Agent 不使用导入链接接入", http.StatusBadRequest)
				return
			}
			link := a.Import()
			if link == "" {
				http.Error(w, "这个 Agent 没有可用的导入链接", http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{"ok": true, "link": link, "added": a.Added != nil && a.Added(), "notice": "在目标应用打开并确认导入，然后刷新接入状态。卸载鲸桥前，请在目标应用移除该供应商。"})
			return
		}
		if action == "launch" {
			if a.Launch == nil {
				http.Error(w, "这个 Agent 不需要专用启动命令", http.StatusBadRequest)
				return
			}
			command := a.Launch()
			if command == "" {
				http.Error(w, "请先接入并选择模型，或安装该 Agent，再生成启动命令", http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{"ok": true, "command": command, "notice": "复制命令并在本机终端运行，以鲸桥配置启动该 Agent。命令包含本机鲸桥密钥，请勿分享。"})
			return
		}
		if a.Import != nil && len(a.Fields) == 0 && a.Native == nil {
			http.Error(w, "这个 Agent 使用导入链接接入，请生成链接并在目标应用确认；连接由目标应用管理。", http.StatusBadRequest)
			return
		}
		if len(a.Fields) == 0 && a.Native == nil {
			http.Error(w, "这个 Agent 没有可配置的接入设置", http.StatusBadRequest)
			return
		}
		ids, err := managedAgents()
		if err != nil {
			agentFailure(w, err, http.StatusInternalServerError)
			return
		}
		if action != "disconnect" {
			err = agentWiringError(a)
			if err == nil && a.Wired() && a.Check == nil && !slices.Contains(ids, a.ID) {
				err = fmt.Errorf("%s 已由其他网关配置，请先在原应用断开连接", a.Name)
			}
			if err != nil {
				agentFailure(w, err, http.StatusBadRequest)
				return
			}
		}
		switch action {
		case "connect":
			_, err = a.ConnectHow()
		case "disconnect":
			if !slices.Contains(ids, a.ID) {
				err = fmt.Errorf("%s 未在鲸桥接入", a.Name)
				break
			}
			err = disconnectExternalAgent(a)
		case "set":
			if a.Field(input.Key) == nil {
				err = fmt.Errorf("未知 Agent 设置")
				break
			}
			var value string
			value, err = a.Spell(input.Key, strings.TrimSpace(input.Value))
			if err == nil {
				err = a.Pick(input.Key, value)
			}
		}
		if err != nil {
			agentFailure(w, err, http.StatusBadRequest)
			return
		}
		ids = slices.DeleteFunc(ids, func(id string) bool { return id == a.ID })
		if action != "disconnect" && a.Wired() {
			ids = append(ids, a.ID)
		}
		if err := saveManagedAgents(ids); err != nil {
			agentFailure(w, fmt.Errorf("Agent 配置已更改，但接入状态保存失败: %w", err), http.StatusInternalServerError)
			return
		}
		delete(agentSyncErrors, a.ID)
		out := map[string]any{"ok": true}
		if a.Notice != nil {
			out["notice"] = agentText(a.Notice())
		}
		writeJSON(w, out)
	}
}
