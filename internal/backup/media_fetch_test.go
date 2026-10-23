package backup

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

// A backup may carry the source machine's network list as configuration;
// restoring its shared preferences must still use this machine's grants.
func TestMediaFetchBackupSealOpenRestoreLocalGrant(t *testing.T) {
	for _, keys := range []bool{false, true} {
		t.Run(fmt.Sprintf("keys=%t", keys), func(t *testing.T) {
			upstreamSettingsHome(t)
			sourceGrant := []string{"source-media.internal.test:8443", "10.17.0.0/16"}
			source := settings.Load()
			source.Theme, source.Currency = "dark", "cny"
			source.FetchPrivate = slices.Clone(sourceGrant)
			if err := settings.Save(source); err != nil {
				t.Fatal(err)
			}
			collected, err := Collect(keys, "")
			if err != nil || collected.Settings == nil || !slices.Equal(collected.Settings.FetchPrivate, sourceGrant) {
				t.Fatalf("collection lost the serialized network configuration: %v", err)
			}
			plain, err := json.Marshal(collected)
			if err != nil {
				t.Fatal(err)
			}
			var decoded Bundle
			if err := json.Unmarshal(plain, &decoded); err != nil || decoded.Settings == nil || !slices.Equal(decoded.Settings.FetchPrivate, sourceGrant) {
				t.Fatalf("bundle JSON did not carry the source configuration: %v", err)
			}
			sealed, err := Seal(collected, "synthetic-media-backup-passphrase")
			if err != nil {
				t.Fatal(err)
			}
			opened, err := Open(sealed, "synthetic-media-backup-passphrase")
			if err != nil || opened.Settings == nil || !slices.Equal(opened.Settings.FetchPrivate, sourceGrant) {
				t.Fatalf("sealed backup did not open with the source list intact: %v", err)
			}
			for _, local := range []struct {
				name  string
				grant []string
			}{
				{"fresh", nil},
				{"empty", []string{}},
				{"different", []string{"own-media.internal.test:9443", "192.168.61.0/24"}},
			} {
				t.Run(local.name, func(t *testing.T) {
					upstreamSettingsHome(t)
					cur := settings.Load()
					cur.Theme, cur.Currency, cur.FetchPrivate = "light", "usd", slices.Clone(local.grant)
					if err := settings.Save(cur); err != nil {
						t.Fatal(err)
					}
					result, err := Restore(opened, Parts{Settings: true})
					if err != nil || !result.Settings {
						t.Fatalf("restoring encrypted source settings: %+v (%v)", result, err)
					}
					got := settings.Load()
					if got.Theme != "dark" || got.Currency != "cny" || !slices.Equal(got.FetchPrivate, local.grant) {
						t.Fatalf("restore must transfer preferences and retain local grants: theme=%s currency=%s grants=%v", got.Theme, got.Currency, got.FetchPrivate)
					}
					if !slices.Equal(opened.Settings.FetchPrivate, sourceGrant) {
						t.Fatal("restore mutated the imported bundle's grant list")
					}
				})
			}
		})
	}
}
