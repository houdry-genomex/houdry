package server

import (
	"context"
	"fmt"
	"time"
)

// WaitJob polls until the job reaches a terminal state or the context ends.
func (s *Server) WaitJob(ctx context.Context, id string) (Job, error) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		j, ok := s.jobs.Get(id)
		if !ok {
			return Job{}, fmt.Errorf("job not found")
		}
		switch j.Status {
		case JobSucceeded, JobFailed:
			return j, nil
		}
		select {
		case <-ctx.Done():
			return j, ctx.Err()
		case <-ticker.C:
		}
	}
}
