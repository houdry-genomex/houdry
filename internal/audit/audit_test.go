package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLogWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.Log("NODE_CERTIFICATE_ISSUED", "abc", "SHA256:ff", "1.2.3.4", "")
	l.Close()

	f, err := os.Open(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		t.Fatal("empty log")
	}
	var ev Event
	if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Event != "NODE_CERTIFICATE_ISSUED" || ev.NodeID != "abc" {
		t.Fatalf("%+v", ev)
	}
	if _, err := time.Parse(time.RFC3339Nano, ev.Timestamp); err != nil {
		t.Fatal(err)
	}
}
