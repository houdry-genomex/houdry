//go:build !linux && !darwin && !windows

package gpu

import "context"

func detectPlatform(ctx context.Context, r Runner) ([]GPU, []string, []string) {
	return nil, nil, []string{"GPU discovery is not implemented for this operating system yet"}
}
