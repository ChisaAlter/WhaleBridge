package whalebridge

import "github.com/ChisaAlter/WhaleBridge/internal/provider"

// Keep references written before built-in retirement usable. The provider
// layer excludes real custom endpoints from this alias map. Canonical values
// win when both spellings have settings already.
func migratePluginReferences() error {
	aliases := provider.LegacyPluginAliases()
	for oldID, newID := range aliases {
		if err := provider.MigrateLegacyPluginSettings(oldID, newID); err != nil {
			return err
		}
	}
	return provider.MigrateLegacyPluginRecords()
}
