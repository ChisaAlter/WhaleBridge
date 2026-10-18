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
	"github.com/ChisaAlter/WhaleBridge/internal/profile"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

type agentOption = agent.Option

type agentField struct {
	Key     string        `json:"key"`
	Label   string        `json:"label"`
	Value   string        `json:"value"`
	Options []agentOption `json:"options"`
	Quiet   bool          `json:"quiet"`
	Follows string        `json:"follows,omitempty"`
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
	ListsFor   string       `json:"listsFor"`
	Plugin     string       `json:"plugin,omitempty"`
}

// The launcher owns WhaleIsle's profile. Its default connection never passes
// through the general-purpose DSH adapter, which writes a different route.
func externalAgent(a *agent.Agent) bool {
	id, _, _ := strings.Cut(a.ID, "@wsl:")
	return id != "dsh"
}

// unlistedAgents are the clients the component does not offer to connect.
// Claude Science is connected by stopping its daemon and starting it again
// on the gateway; the component never starts a client for the user.
var unlistedAgents = map[string]bool{agent.ScienceID: true}

func listedAgent(a *agent.Agent) bool {
	id, _, _ := strings.Cut(a.ID, "@wsl:")
	return externalAgent(a) && !unlistedAgents[id]
}

// findListedAgent is agent.Find limited to the clients this page offers.
func findListedAgent(id string) (*agent.Agent, error) {
	a, err := agent.Find(id)
	if bare, _, _ := strings.Cut(id, "@wsl:"); err == nil && unlistedAgents[bare] {
		return nil, fmt.Errorf("鲸桥不提供 %s 的接入", a.Name)
	}
	return a, err
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
			if err == nil && a.Follow != nil {
				err = a.Follow()
			}
			if err == nil {
				err = a.SyncCurrent()
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

func managedAgent(a *agent.Agent) error {
	if !externalAgent(a) {
		return fmt.Errorf("鲸屿连接由启动器管理")
	}
	ids, err := managedAgents()
	if err != nil {
		return err
	}
	if !slices.Contains(ids, a.ID) {
		return fmt.Errorf("请先明确接入 %s，再修改它的设置", a.Name)
	}
	return agentWiringError(a)
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
	if (id == "opencode" || id == "mimocode" || id == "deveco" || id == "codebuddy" || id == "qoder" || id == "qoder-cn") && a.Unwire != nil {
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
		ListsFor: "dsh",
		Path:     os.Getenv("WHALEBRIDGE_DSH_HOME"),
		Notice:   "默认接入鲸屿，供应商与模型变更会自动同步。", Fields: []agentField{},
		SyncError: dshSyncError(),
	}}
	for _, a := range agent.All() {
		if !listedAgent(a) {
			continue
		}
		vals := a.Values()
		row := agentRow{ID: a.ID, Name: agentText(a.Name), Detected: a.Detected(), Managed: slices.Contains(ids, a.ID), Connected: slices.Contains(ids, a.ID) && a.Wired(), Path: agentText(a.Path), Fields: []agentField{}, SyncError: agentText(agentSyncErrors[a.ID]), ListsFor: a.ListsFor(), Plugin: a.Plugin}
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
				for _, option := range a.Picks(f, vals) {
					option.Value, option.Label, option.Note, option.Ref = agentText(option.Value), agentText(option.Label), agentText(option.Note), agentText(option.Ref)
					options = append(options, option)
				}
			}
			row.Fields = append(row.Fields, agentField{Key: f.Key, Label: agentText(f.Label), Value: agentText(vals[f.Key]), Options: options, Quiet: f.Quiet, Follows: f.Follows})
		}
		rows = append(rows, row)
	}
	return rows
}

func agentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agents", func(w http.ResponseWriter, r *http.Request) {
		mutations.Lock()
		defer mutations.Unlock()
		errs := map[string]string{}
		for spec, err := range agent.PluginErrors() {
			errs[spec] = agentText(err)
		}
		writeJSON(w, map[string]any{"defaultAgent": "dsh", "agents": agentRows(), "pluginErrors": errs})
	})
	for _, action := range []string{"connect", "disconnect", "set", "import", "launch"} {
		mux.HandleFunc("POST /api/agent/"+action, agentAction(action))
	}
	agentModelRoutes(mux)
	agentProfileRoutes(mux)
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
		a, err := findListedAgent(input.ID)
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
			if err == nil && a.Wired() && !slices.Contains(ids, a.ID) {
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
			if err = managedAgent(a); err != nil {
				break
			}
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
		if action != "disconnect" && (a.Wired() || slices.Contains(ids, a.ID) || action == "set") {
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

type agentModel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Group   string `json:"group"`
	Context int    `json:"context,omitempty"`
	Hidden  bool   `json:"hidden"`
	InUse   bool   `json:"inUse"`
	FastFor string `json:"fastFor,omitempty"`
	Fast    bool   `json:"fast,omitempty"`
	OwnFast bool   `json:"ownFast,omitempty"`
}

func agentModelList(a *agent.Agent) []agentModel {
	id := a.ListsFor()
	listed, _ := provider.ListedFor(id)
	off := provider.ModelOff(id)
	vals := a.Values()
	out := []agentModel{}
	for _, e := range listed {
		m := agentModel{ID: e.ID, Name: e.Name, Group: e.Provider.Name, Context: e.Context, Hidden: off(e.ID)}
		if m.Name == "" {
			m.Name = e.Model
		}
		if e.Group != "" {
			m.Group = "路由组"
		}
		for _, v := range vals {
			if a.SplitSuffix != nil {
				if model, _, one := a.SplitSuffix(v); one {
					v = model
				}
			}
			if v != "" && (v == e.ID || strings.HasSuffix(v, "/"+e.ID)) {
				m.InUse = true
			}
			if acc := e.Provider.Account; e.Group == "" && acc != nil && acc.Agent == id && v == e.Model {
				m.InUse = true
			}
		}
		if e.Group == "" && provider.CanFast(e.Provider, e.Model) {
			m.FastFor, m.Fast, m.OwnFast = id, provider.IsFastPick(id, e.ID), provider.OwnFast(e.Provider, e.Model)
		}
		out = append(out, m)
	}
	return out
}

func agentModelsState(a *agent.Agent) map[string]any {
	_, only := provider.PickedModels(a.ListsFor())
	own := false
	for _, f := range a.Fields {
		if f.Options == nil {
			continue
		}
		opts := f.Options(a.Values())
		own = own || (slices.ContainsFunc(opts, agent.Own) && slices.ContainsFunc(opts, func(o agent.Option) bool { return o.Ref != "" }))
	}
	return map[string]any{"id": a.ID, "for": a.ListsFor(), "models": agentModelList(a), "only": only, "ordered": len(provider.ModelOrder(a.ListsFor())) > 0, "own": own, "ownHidden": provider.OwnModelsHidden(a.ListsFor())}
}

func agentModelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agent-models/{id}", func(w http.ResponseWriter, r *http.Request) {
		mutations.Lock()
		defer mutations.Unlock()
		a, err := findListedAgent(r.PathValue("id"))
		if err != nil {
			agentFailure(w, err, 400)
			return
		}
		writeJSON(w, agentModelsState(a))
	})
	mux.HandleFunc("POST /api/agent-models/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Hidden    []string  `json:"hidden"`
			Shown     *[]string `json:"shown"`
			Order     *[]string `json:"order"`
			Only      *bool     `json:"only"`
			OwnHidden *bool     `json:"ownHidden"`
		}
		if err := decode(w, r, &in); err != nil {
			agentFailure(w, err, 400)
			return
		}
		mutations.Lock()
		defer mutations.Unlock()
		a, err := findListedAgent(r.PathValue("id"))
		if err == nil && externalAgent(a) {
			err = managedAgent(a)
		}
		if err != nil {
			agentFailure(w, err, 400)
			return
		}
		id := a.ListsFor()
		if in.OwnHidden != nil {
			err = provider.SetOwnModelsHidden(id, *in.OwnHidden)
		}
		if err == nil && in.Only != nil && in.Shown == nil && in.Hidden == nil {
			err = provider.SetOnlyPicked(id, *in.Only)
		}
		if err == nil && (in.Shown != nil || in.Hidden != nil || in.Order != nil) {
			l := provider.ModelListOf(id)
			if in.Only != nil {
				l.Only = *in.Only
			}
			if in.Order != nil {
				l.Order = *in.Order
			}
			if in.Shown != nil || in.Hidden != nil {
				l.Hidden, l.Picked = nil, nil
				for _, m := range agentModelList(a) {
					shown := !slices.Contains(in.Hidden, m.ID)
					if in.Shown != nil {
						shown = slices.Contains(*in.Shown, m.ID)
					}
					shown = shown || m.InUse
					if l.Only && shown {
						l.Picked = append(l.Picked, m.ID)
					} else if !l.Only && !shown {
						l.Hidden = append(l.Hidden, m.ID)
					}
				}
			}
			_, err = provider.SetModelLists(map[string]provider.ModelList{id: l})
		}
		if err == nil {
			err = syncDSH()
			syncManagedAgents()
		}
		if err != nil {
			agentFailure(w, err, 400)
			return
		}
		writeJSON(w, agentModelsState(a))
	})
	mux.HandleFunc("POST /api/agent-fast", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID, For, Ref string
			Fast         bool
		}
		if err := decode(w, r, &in); err != nil {
			agentFailure(w, err, 400)
			return
		}
		mutations.Lock()
		defer mutations.Unlock()
		if in.ID == "" {
			in.ID = in.For
		}
		a, err := findListedAgent(in.ID)
		if err == nil && externalAgent(a) {
			err = managedAgent(a)
		}
		if err == nil && in.For != "" && in.For != a.ListsFor() {
			err = fmt.Errorf("模型名单不属于这个 Agent")
		}
		if err == nil {
			err = provider.SetFastPick(a.ListsFor(), in.Ref, in.Fast)
		}
		if err != nil {
			agentFailure(w, err, 400)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "fast": provider.IsFastPick(a.ListsFor(), in.Ref)})
	})
}

// Profiles keep the upstream file format, but this component captures and
// applies only clients explicitly registered here. Library contents and the
// launcher's WhaleIsle profile never enter these snapshots.
func managedProfileAgents() ([]*agent.Agent, error) {
	ids, err := managedAgents()
	if err != nil {
		return nil, err
	}
	out := []*agent.Agent{}
	for _, id := range ids {
		a, err := agent.Find(id)
		if err != nil {
			return nil, err
		}
		if !externalAgent(a) {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

func managedProfile(p profile.Profile, agents []*agent.Agent) profile.Profile {
	out := profile.Profile{Fields: map[string]string{}, Models: map[string]provider.ModelList{}}
	for _, a := range agents {
		for _, f := range a.Fields {
			k := a.ID + "." + f.Key
			if v, ok := p.Fields[k]; ok {
				out.Fields[k] = v
			}
		}
		if l, ok := p.Models[a.ListsFor()]; ok {
			out.Models[a.ListsFor()] = l
		}
	}
	return out
}

func managedProfileDetails(p profile.Profile, agents []*agent.Agent) []profile.Group {
	groups := profile.Details(managedProfile(p, agents))
	for i := range groups {
		g := &groups[i]
		g.Name = agentText(g.Name)
		for n := range g.Fields {
			g.Fields[n].Value = agentText(g.Fields[n].Value)
		}
	}
	return groups
}

func agentProfilesState() (map[string]any, error) {
	ps, err := profile.Load()
	if err != nil {
		return nil, err
	}
	as, err := managedProfileAgents()
	if err != nil {
		return nil, err
	}
	now := map[string]map[string]string{}
	for _, a := range as {
		now[a.ID] = a.Values()
	}
	rows := []map[string]any{}
	for _, name := range profile.Names(ps) {
		p := managedProfile(ps[name], as)
		rows = append(rows, map[string]any{"name": name, "inUse": profile.InUse(p, as, now), "details": managedProfileDetails(p, as), "applicable": len(p.Fields)+len(p.Models) > 0})
	}
	return map[string]any{"profiles": rows, "managed": len(as)}, nil
}

func agentProfileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agent-profiles", func(w http.ResponseWriter, r *http.Request) {
		mutations.Lock()
		defer mutations.Unlock()
		out, err := agentProfilesState()
		if err != nil {
			agentFailure(w, err, 400)
			return
		}
		writeJSON(w, out)
	})
	for _, action := range []string{"save", "apply", "delete"} {
		mux.HandleFunc("POST /api/agent-profiles/"+action, func(w http.ResponseWriter, r *http.Request) {
			var in struct{ Name string }
			if err := decode(w, r, &in); err != nil {
				agentFailure(w, err, 400)
				return
			}
			mutations.Lock()
			defer mutations.Unlock()
			as, err := managedProfileAgents()
			if err != nil {
				agentFailure(w, err, 400)
				return
			}
			changed := 0
			switch action {
			case "save":
				if len(as) == 0 {
					err = fmt.Errorf("请先接入至少一个外部 Agent，再保存方案")
					break
				}
				p := profile.Profile{Fields: map[string]string{}, Models: map[string]provider.ModelList{}}
				for _, a := range as {
					if err = agentWiringError(a); err != nil {
						break
					}
					for k, v := range a.Values() {
						p.Fields[a.ID+"."+k] = v
					}
					p.Models[a.ListsFor()] = provider.ModelListOf(a.ListsFor())
				}
				if err == nil {
					err = profile.Save(in.Name, p)
				}
			case "delete":
				err = profile.Delete(in.Name)
			case "apply":
				var ps map[string]profile.Profile
				ps, err = profile.Load()
				if err != nil {
					break
				}
				p, found := ps[in.Name]
				if !found {
					err = fmt.Errorf("方案不存在")
					break
				}
				p = managedProfile(p, as)
				if len(p.Fields)+len(p.Models) == 0 {
					err = fmt.Errorf("这个方案没有当前已接入 Agent 的配置")
					break
				}
				for _, a := range as {
					if err = agentWiringError(a); err != nil {
						break
					}
				}
				if err != nil {
					break
				}
				var lists []string
				lists, err = provider.SetModelLists(p.Models)
				changed = len(lists)
				if err != nil {
					break
				}
				var fields int
				fields, err = profile.ApplyFieldsFor(p.Fields, as)
				changed += fields
				if err == nil {
					syncManagedAgents()
				}
			}
			if err != nil {
				agentFailure(w, err, 400)
				return
			}
			out, err := agentProfilesState()
			if err != nil {
				agentFailure(w, err, 500)
				return
			}
			out["ok"], out["changed"] = true, changed
			writeJSON(w, out)
		})
	}
}
