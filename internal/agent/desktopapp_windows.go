//go:build windows

package agent

import "golang.org/x/sys/windows/registry"

const appxPackagesKey = `Software\Classes\Local Settings\Software\Microsoft\Windows\CurrentVersion\AppModel\Repository\Packages`

// installedWindowsStorePackages returns package full names registered for the
// current user. Reading this key needs no access to the protected WindowsApps
// folder and does not start PowerShell.
func installedWindowsStorePackages() []string {
	key, err := registry.OpenKey(registry.CURRENT_USER, appxPackagesKey, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	defer key.Close()
	names, _ := key.ReadSubKeyNames(-1)
	return names
}
