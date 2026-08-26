package gpu

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
)

// Runner abstracts command execution and filesystem reads so detectors
// can be tested without real GPUs or OS tools.
type Runner interface {
	LookPath(file string) (string, error)
	Run(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)
	ReadFile(name string) ([]byte, error)
	ReadDir(name string) ([]fs.DirEntry, error)
	ReadLink(name string) (string, error)
	Exists(name string) bool
}

// OSRunner calls the real operating system.
type OSRunner struct{}

func (OSRunner) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func (OSRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func (OSRunner) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(name)
}

func (OSRunner) ReadDir(name string) ([]fs.DirEntry, error) {
	return os.ReadDir(name)
}

func (OSRunner) ReadLink(name string) (string, error) {
	return os.Readlink(name)
}

func (OSRunner) Exists(name string) bool {
	_, err := os.Stat(name)
	return err == nil
}

func firstExisting(r Runner, candidates ...string) string {
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if filepath.IsAbs(c) {
			if r.Exists(c) {
				return c
			}
			continue
		}
		if p, err := r.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}
