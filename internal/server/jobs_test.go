package server

import (
	"testing"
	"time"
)

func TestFailRunningExceptKeepsCurrentJob(t *testing.T) {
	js, err := NewJobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keep, err := js.Create(JobTypeGPUSmoke, "n1", Requirements{GPURequired: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := js.Create(JobTypeGPUSmoke, "n1", Requirements{GPURequired: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	js.mu.Lock()
	keep.Status = JobRunning
	keep.NodeID = "n1"
	keep.StartedAt = time.Now().UTC()
	js.jobs[keep.ID] = keep
	orphan.Status = JobRunning
	orphan.NodeID = "n1"
	orphan.StartedAt = time.Now().UTC()
	js.jobs[orphan.ID] = orphan
	js.mu.Unlock()

	n := js.FailRunningExcept("n1", keep.ID, "worker is no longer running this job")
	if n != 1 {
		t.Fatalf("failed %d jobs, want 1", n)
	}
	got, ok := js.Get(keep.ID)
	if !ok || got.Status != JobRunning {
		t.Fatalf("kept job status=%s", got.Status)
	}
	got, ok = js.Get(orphan.ID)
	if !ok || got.Status != JobFailed {
		t.Fatalf("orphan status=%s", got.Status)
	}
}
