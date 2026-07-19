//go:build !windows

package desktoptun

// CleanupStaleRoutes is a Windows recovery hook. Other platforms clean their
// routes through Tunnel.Stop and do not use the desktop watchdog command.
func CleanupStaleRoutes(string) error { return nil }
