package cli

import (
	"bytes"
	"strings"
	"testing"

	"houdry/internal/version"
)

// TestReleaseCLISurface locks the commands friends get after install.sh /
// make dist. A Phase-1-only binary fails these checks.
func TestReleaseCLISurface(t *testing.T) {
	var buf bytes.Buffer
	printUsage(&buf)
	usage := buf.String()

	required := []string{
		"houdry node join",
		"houdry node list",
		"houdry job submit",
		"houdry serve",
		"houdry discover",
		"houdry version",
		"houdry gpu detect",
		"houdry gpu register",
	}
	for _, want := range required {
		if !strings.Contains(usage, want) {
			t.Fatalf("usage missing %q:\n%s", want, usage)
		}
	}

	if err := Run([]string{"help"}); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"version"}); err != nil {
		t.Fatal(err)
	}
	if version.Version == "" || strings.Contains(version.Version, "dev") {
		// Release tags stamp a stable semver via ldflags; the source default
		// must also be a releasable version (not …-dev).
		t.Fatalf("version.Version should be a stable release string, got %q", version.Version)
	}
	if !strings.HasPrefix(version.Version, "0.6.") {
		t.Fatalf("expected 0.6.x release line, got %q", version.Version)
	}
}

func TestRouteWebRemoved(t *testing.T) {
	err := Run([]string{"route", "--web"})
	if err == nil {
		t.Fatal("expected --web to be removed")
	}
	if !strings.Contains(err.Error(), "houdry serve") {
		t.Fatalf("error should point at serve, got %v", err)
	}
}

func TestUnknownCommandStillMentionsNode(t *testing.T) {
	err := Run([]string{"not-a-command"})
	if err == nil {
		t.Fatal("expected error")
	}
}
