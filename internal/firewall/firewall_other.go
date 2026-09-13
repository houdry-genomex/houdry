//go:build !windows

package firewall

// EnsurePortOpen is a no-op on non-Windows platforms
func EnsurePortOpen(port int, ruleName string) error {
	// Linux/macOS typically don't have restrictive firewalls by default
	// or require manual configuration per-distro
	return nil
}
