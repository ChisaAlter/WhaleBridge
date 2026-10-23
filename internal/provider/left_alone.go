package provider

// leftAlone says whether background maintenance leaves a provider alone:
// switched off, or its account removed. Its last known state stays visible.
func leftAlone(id string) bool {
	if id == "" {
		return false
	}
	for _, p := range load().Providers {
		if p.ID == id && (p.Off || p.Hidden) {
			return true
		}
	}
	return false
}
