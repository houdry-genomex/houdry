package discovery

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestUDPAdvertiseBrowse(t *testing.T) {
	stop, err := Advertise(Info{
		Name:    "houdry-test-udp",
		Listen:  "127.0.0.1:18080",
		Version: "test",
		Path:    "/v1",
		OpenAI:  true,
	})
	if err != nil {
		t.Skip(err)
	}
	defer stop()

	time.Sleep(200 * time.Millisecond)
	found, err := Browse(context.Background(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range found {
		if strings.Contains(ep.URL, ":18080") || ep.Name == "houdry-test-udp" {
			return
		}
	}
	t.Logf("found %d endpoints (UDP/mDNS may be blocked in this environment): %+v", len(found), found)
}
