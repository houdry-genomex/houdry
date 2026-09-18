// Package audit writes non-blocking JSONL security events.
package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const fileName = "audit.log"

type Event struct {
	Event       string `json:"event"`
	NodeID      string `json:"node_id,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	RemoteIP    string `json:"remote_ip,omitempty"`
	Error       string `json:"error,omitempty"`
	Timestamp   string `json:"timestamp"`
}

type Logger struct {
	ch   chan Event
	done chan struct{}
	once sync.Once
}

func Open(dataDir string) (*Logger, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, fileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	l := &Logger{ch: make(chan Event, 256), done: make(chan struct{})}
	go func() {
		defer close(l.done)
		defer f.Close()
		enc := json.NewEncoder(f)
		for ev := range l.ch {
			_ = enc.Encode(ev)
		}
	}()
	return l, nil
}

func (l *Logger) Log(event, nodeID, fingerprint, remoteIP, errMsg string) {
	if l == nil {
		return
	}
	ev := Event{
		Event:       event,
		NodeID:      nodeID,
		Fingerprint: fingerprint,
		RemoteIP:    remoteIP,
		Error:       errMsg,
		Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
	}
	select {
	case l.ch <- ev:
	default:
	}
}

func (l *Logger) Close() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		close(l.ch)
		<-l.done
	})
}
