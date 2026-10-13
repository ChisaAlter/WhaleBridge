package backup

import (
	"slices"
	"strings"

	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

// KeepOtherAgentSettings makes dsh the only agent whose model preferences
// this component imports or exports. Other agents' settings are opaque:
// retain those already at the destination, including an explicit empty
// PickedModels entry, instead of overwriting them with the source's copy.
func KeepOtherAgentSettings(in *settings.Settings, current settings.Settings) {
	lists := func(source, destination map[string][]string) map[string][]string {
		out := map[string][]string{}
		for id, values := range destination {
			if !strings.EqualFold(id, "dsh") {
				out[id] = slices.Clone(values)
			}
		}
		for id, values := range source {
			if strings.EqualFold(id, "dsh") {
				out[id] = slices.Clone(values)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	in.Visible = lists(in.Visible, current.Visible)
	in.HiddenModels = lists(in.HiddenModels, current.HiddenModels)
	in.OrderedModels = lists(in.OrderedModels, current.OrderedModels)
	in.PickedModels = lists(in.PickedModels, current.PickedModels)
	in.FastPicks = lists(in.FastPicks, current.FastPicks)
	hidden := slices.DeleteFunc(slices.Clone(current.OwnModelsHidden), func(id string) bool { return strings.EqualFold(id, "dsh") })
	for _, id := range in.OwnModelsHidden {
		if strings.EqualFold(id, "dsh") {
			hidden = append(hidden, id)
		}
	}
	in.OwnModelsHidden = hidden
	efforts := map[string]string{}
	for id, value := range current.AgentEfforts {
		if !strings.EqualFold(id, "dsh") {
			efforts[id] = value
		}
	}
	for id, value := range in.AgentEfforts {
		if strings.EqualFold(id, "dsh") {
			efforts[id] = value
		}
	}
	if len(efforts) == 0 {
		efforts = nil
	}
	in.AgentEfforts = efforts
}
