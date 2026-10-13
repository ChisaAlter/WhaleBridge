package usageapi

import (
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/agentenv"
	"github.com/ChisaAlter/WhaleBridge/internal/usage"
)

func TestComponentUsageAPINamesAndDateAgentRetryFilters(t *testing.T) {
	root := t.TempDir()
	for _, name := range agentenv.Vars {
		t.Setenv(name, "")
	}
	for _, name := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(name, root)
	}
	t.Setenv("LAUNCHER_COMPONENT_DATA_DIR", filepath.Join(root, "component"))
	t.Setenv("MAGPIE_NO_ACCOUNTS", "1")
	t.Setenv("PATH", "")
	before := Clients
	Clients = func() []Client { return []Client{{ID: "dsh", Name: "鲸屿"}, {ID: "codex", Name: "Codex"}} }
	t.Cleanup(func() { Clients = before })
	now := time.Now()
	day := now.Format(time.DateOnly)
	p := PeriodOf(day + ".." + day)
	usage.Append(usage.Record{Time: now.Add(-time.Second), Agent: "dsh", Provider: "fixture", Model: "m", Status: 200, Input: 7, Tries: 2})
	usage.Append(usage.Record{Time: now.Add(-time.Second), Agent: "codex", Provider: "fixture", Model: "m", Status: 200, Input: 13})
	usage.Append(usage.Record{Time: now.Add(-time.Second), Agent: "dsh", Provider: "fixture", Model: "m", Status: 200, Input: 999, Source: usage.SourceReported})
	s := Page(p)
	if s.Calls != 2 || s.Input != 20 || len(s.Agents) != 2 || s.Agents[0].Name != "Codex" && s.Agents[1].Name != "Codex" {
		t.Fatalf("named summary/scope: %+v", s)
	}
	f := FilterOf(url.Values{"agent": {"dsh"}, "retried": {"1"}, "via": {usage.SourceGateway}})
	page := LedgerPage(p, f, 0, 50)
	if page.Total != 1 || page.Input != 7 || page.Calls != 1 || page.Retried != 1 || page.Rows[0].AgentName != "鲸屿" {
		t.Fatalf("named date/agent/retry ledger: %+v", page)
	}
	if PeriodOf("invalid") != usage.Month || !p.IsRange() {
		t.Fatal("period contract changed")
	}
}
