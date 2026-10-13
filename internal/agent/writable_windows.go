package agent

// writable says whether this user can write in dir: on Windows npm's
// global folder is the user's own (%APPDATA%\npm).
func writable(string) bool { return true }
