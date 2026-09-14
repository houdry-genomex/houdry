//go:build windows

package firewall

import (
	"fmt"
	"os/exec"
	"strings"
)

// EnsurePortOpen ensures that the specified TCP port is allowed through Windows Firewall.
// It checks if a rule already exists, and if not, attempts to create it.
// Returns nil if the port is already open or successfully opened.
func EnsurePortOpen(port int, ruleName string) error {
	if ruleName == "" {
		ruleName = fmt.Sprintf("Houdry Control Plane (port %d)", port)
	}

	// Check if rule already exists
	exists, err := ruleExists(ruleName)
	if err != nil {
		return fmt.Errorf("failed to check firewall rule: %w", err)
	}

	if exists {
		// Rule already exists, nothing to do
		return nil
	}

	// Try to add the firewall rule with explicit profile=any to ensure it works on all network types
	cmd := exec.Command("netsh", "advfirewall", "firewall", "add", "rule",
		fmt.Sprintf("name=%s", ruleName),
		"dir=in",
		"action=allow",
		"protocol=TCP",
		fmt.Sprintf("localport=%d", port),
		"profile=any")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add firewall rule (try running as Administrator): %w\nOutput: %s", err, string(output))
	}

	// Check if Windows Firewall has "Block all inbound connections" enabled
	// This setting overrides individual Allow rules and is common on Public networks
	if err := checkBlockAllInbound(); err != nil {
		return fmt.Errorf("firewall rule added, but: %w", err)
	}

	return nil
}

// ruleExists checks if a firewall rule with the given name already exists
func ruleExists(ruleName string) (bool, error) {
	cmd := exec.Command("netsh", "advfirewall", "firewall", "show", "rule", fmt.Sprintf("name=%s", ruleName))
	output, err := cmd.CombinedOutput()

	if err != nil {
		// If the command fails with "No rules match", the rule doesn't exist
		if strings.Contains(string(output), "No rules match") {
			return false, nil
		}
		return false, fmt.Errorf("failed to query firewall rules: %w", err)
	}

	// If we get here and no error, the rule exists
	return true, nil
}

// checkBlockAllInbound checks if Windows Firewall has "Block all inbound connections" enabled
// This setting overrides individual Allow rules
func checkBlockAllInbound() error {
	// Check all three profiles: Domain, Private, Public
	profiles := []string{"domainprofile", "privateprofile", "publicprofile"}
	blockedProfiles := []string{}

	for _, profile := range profiles {
		cmd := exec.Command("netsh", "advfirewall", "show", profile, "state")
		output, err := cmd.CombinedOutput()
		if err != nil {
			// If we can't check, assume it's OK rather than failing
			continue
		}

		// Look for "Block all connections" or "BlockAllInbound"
		outputStr := string(output)
		if strings.Contains(outputStr, "Block all connections") && strings.Contains(outputStr, "Yes") {
			blockedProfiles = append(blockedProfiles, profile)
		}
	}

	if len(blockedProfiles) > 0 {
		return fmt.Errorf("Windows Firewall 'Block all inbound connections' is enabled for %v. This overrides individual allow rules. Disable it or manually allow the port in Windows Defender Firewall settings", blockedProfiles)
	}

	return nil
}
