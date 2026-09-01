package discovery

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/grandcat/zeroconf"
)

// Browse waits for control planes on mDNS and UDP, then returns unique URLs.
func Browse(ctx context.Context, wait time.Duration) ([]Endpoint, error) {
	if wait <= 0 {
		wait = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	var (
		mu   sync.Mutex
		list []Endpoint
	)
	add := func(ep Endpoint) {
		if ep.URL == "" {
			return
		}
		mu.Lock()
		list = append(list, complete(ep))
		mu.Unlock()
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = browseMDNS(ctx, add)
	}()
	go func() {
		defer wg.Done()
		_ = browseUDP(ctx, add)
	}()
	wg.Wait()

	mu.Lock()
	out := unique(list)
	mu.Unlock()
	return out, nil
}

// Verify reports whether URL looks like a Houdry control plane.
func Verify(ctx context.Context, ep Endpoint) bool {
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	base := strings.TrimRight(ep.URL, "/")
	if probeJSON(ctx, base+"/.well-known/houdry.json") {
		return true
	}
	return probeJSON(ctx, base+"/healthz")
}

func probeJSON(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Resolve finds exactly one verified control plane.
// If several are on the LAN, it returns an error listing them so the user
// can pass --server. Falls back to unverified results if HTTP confirm fails
// (guest WiFi isolation can block HTTP while UDP still answers).
func Resolve(ctx context.Context, wait time.Duration) (Endpoint, error) {
	found, err := Browse(ctx, wait)
	if err != nil {
		return Endpoint{}, err
	}
	if len(found) == 0 {
		return Endpoint{}, fmt.Errorf("no Houdry control plane on this WiFi (is houdry serve running, and is client isolation off?)")
	}

	var ok []Endpoint
	for _, ep := range found {
		if Verify(ctx, ep) {
			ok = append(ok, ep)
		}
	}
	if len(ok) == 0 {
		ok = found
	}
	if len(ok) == 1 {
		return ok[0], nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "found %d Houdry control planes; pass --server URL to pick one:", len(ok))
	for _, ep := range ok {
		fmt.Fprintf(&b, "\n  %s  %s", ep.URL, ep.Name)
	}
	return Endpoint{}, fmt.Errorf("%s", b.String())
}

func browseMDNS(ctx context.Context, add func(Endpoint)) error {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return err
	}
	entries := make(chan *zeroconf.ServiceEntry, 16)
	go func() {
		for e := range entries {
			if e == nil {
				continue
			}
			ips := append(append([]net.IP{}, e.AddrIPv4...), e.AddrIPv6...)
			if ep, ok := endpointFromTXT(e.Instance, e.HostName, e.Port, e.Text, ips, "mdns"); ok {
				add(ep)
			}
		}
	}()
	if err := resolver.Browse(ctx, ServiceType, Domain, entries); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func browseUDP(ctx context.Context, add func(Endpoint)) error {
	lc := net.ListenConfig{Control: udpBroadcastControl}
	pc, err := lc.ListenPacket(ctx, "udp4", ":0")
	if err != nil {
		return err
	}
	defer pc.Close()
	conn, ok := pc.(*net.UDPConn)
	if !ok {
		return fmt.Errorf("udp: unexpected conn")
	}

	payload := probePayload()
	targets := udpTargets()
	for _, t := range targets {
		_, _ = conn.WriteToUDP(payload, t)
	}

	go func() {
		<-ctx.Done()
		_ = conn.SetReadDeadline(time.Now())
	}()

	buf := make([]byte, 2048)
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return nil
		}
		if ep, ok := endpointFromUDP(buf[:n], "udp"); ok {
			add(ep)
		}
	}
}

func udpTargets() []*net.UDPAddr {
	port := UDPPort
	out := []*net.UDPAddr{
		{IP: net.IPv4bcast, Port: port},
		{IP: net.ParseIP(MulticastGroup), Port: port},
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	seen := map[string]bool{}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil {
				continue
			}
			mask := ipn.Mask
			if len(mask) == net.IPv6len {
				mask = mask[12:]
			}
			if len(mask) != net.IPv4len {
				continue
			}
			bcast := make(net.IP, 4)
			for i := 0; i < 4; i++ {
				bcast[i] = ip4[i] | ^mask[i]
			}
			key := bcast.String()
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, &net.UDPAddr{IP: bcast, Port: port})
		}
	}
	return out
}

func udpBroadcastControl(network, address string, c syscall.RawConn) error {
	var sockErr error
	err := c.Control(func(fd uintptr) {
		sockErr = setBroadcast(fd)
	})
	if err != nil {
		return err
	}
	return sockErr
}
