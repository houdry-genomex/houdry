package enrollment

import (
	"testing"
	"time"
)

func TestCreateAndConsumeOnce(t *testing.T) {
	s, err := NewStore(t.TempDir(), time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	iss, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	if iss.Token[:5] != "HDRY_" || iss.Uses != 1 {
		t.Fatalf("%+v", iss)
	}
	if err := s.Consume(iss.Token, "n1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Consume(iss.Token, "n1"); err == nil {
		t.Fatal("expected reuse rejection")
	}
}

func TestExpiredToken(t *testing.T) {
	s, err := NewStore(t.TempDir(), time.Millisecond, 1)
	if err != nil {
		t.Fatal(err)
	}
	iss, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := s.Consume(iss.Token, "n1"); err == nil {
		t.Fatal("expected expiry")
	}
}

func TestUnknownToken(t *testing.T) {
	s, err := NewStore(t.TempDir(), time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Consume("HDRY_deadbeef", "n"); err == nil {
		t.Fatal("expected invalid")
	}
}

func TestPersistsAcrossOpen(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	iss, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	s2, err := NewStore(dir, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Consume(iss.Token, "n1"); err != nil {
		t.Fatal(err)
	}
}
