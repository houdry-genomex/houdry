package discovery

import (
	"net"
	"testing"
)

func TestProbeRoundTrip(t *testing.T) {
	if !isProbe(probePayload()) {
		t.Fatal("probe not recognized")
	}
	ep := complete(Endpoint{
		Name:         "houdry-lab",
		URL:          "http://192.168.1.10:8080",
		Path:         "/v1",
		Version:      "0.6.0",
		AuthRequired: true,
		OpenAI:       true,
		Source:       "udp",
	})
	got, ok := endpointFromUDP(advertisePayload(ep), "udp")
	if !ok {
		t.Fatal("advertise not recognized")
	}
	if got.URL != ep.URL || got.API != "http://192.168.1.10:8080/v1" {
		t.Fatalf("%+v", got)
	}
	if !got.AuthRequired || !got.OpenAI || got.Name != "houdry-lab" {
		t.Fatalf("%+v", got)
	}
}

func TestTXTRoundTrip(t *testing.T) {
	info := Info{Name: "houdry-x", Listen: "0.0.0.0:8080", Version: "0.6.0", Path: "/v1", AuthRequired: false, OpenAI: true}
	ip := net.ParseIP("10.0.0.5")
	ep, ok := endpointFromTXT(info.Name, "host", 8080, txtRecords(info), []net.IP{ip}, "mdns")
	if !ok {
		t.Fatal("txt")
	}
	if ep.API != "http://10.0.0.5:8080/v1" {
		t.Fatalf("api=%s", ep.API)
	}
	if ep.AuthRequired {
		t.Fatal("auth")
	}
	if !ep.OpenAI {
		t.Fatal("openai")
	}
}

func TestPickIPPrefersV4LAN(t *testing.T) {
	loop := net.ParseIP("127.0.0.1")
	v6 := net.ParseIP("fe80::1")
	v4 := net.ParseIP("192.168.0.9")
	got := pickIP([]net.IP{loop, v6, v4})
	if !got.Equal(v4) {
		t.Fatalf("got %v", got)
	}
}

func TestUniqueByURL(t *testing.T) {
	in := []Endpoint{
		complete(Endpoint{URL: "http://a:8080", Name: "", Source: "udp"}),
		complete(Endpoint{URL: "http://a:8080", Name: "desk", Source: "mdns"}),
		complete(Endpoint{URL: "http://b:8080", Name: "other", Source: "udp"}),
	}
	out := unique(in)
	if len(out) != 2 {
		t.Fatalf("%d: %+v", len(out), out)
	}
	if out[0].Name != "desk" {
		t.Fatalf("merged name=%q", out[0].Name)
	}
}

func TestParseListenPort(t *testing.T) {
	p, err := parseListenPort("0.0.0.0:18080")
	if err != nil || p != 18080 {
		t.Fatalf("%d %v", p, err)
	}
	if _, err := parseListenPort("not-an-addr"); err == nil {
		t.Fatal("expected error")
	}
}
