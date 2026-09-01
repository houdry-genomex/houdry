package discovery

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/grandcat/zeroconf"
	"golang.org/x/net/ipv4"
)

// StopFunc ends LAN advertising.
type StopFunc func()

// Advertise announces this control plane on mDNS and UDP. If one transport
// fails, the other still runs. Returns an error only when both fail.
func Advertise(info Info) (StopFunc, error) {
	if strings.TrimSpace(info.Path) == "" {
		info.Path = DefaultPath
	}
	port, err := parseListenPort(info.Listen)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(info.Name) == "" {
		info.Name = instanceName()
	}
	if !strings.Contains(info.Name, strconv.Itoa(port)) {
		info.Name = fmt.Sprintf("%s-%d", info.Name, port)
	}

	var (
		mu    sync.Mutex
		stops []StopFunc
	)
	add := func(s StopFunc) {
		if s == nil {
			return
		}
		mu.Lock()
		stops = append(stops, s)
		mu.Unlock()
	}

	var errs []string
	if stop, err := advertiseMDNS(info); err != nil {
		errs = append(errs, "mdns: "+err.Error())
	} else {
		add(stop)
	}
	if stop, err := advertiseUDP(info); err != nil {
		errs = append(errs, "udp: "+err.Error())
	} else {
		add(stop)
	}

	mu.Lock()
	n := len(stops)
	mu.Unlock()
	if n == 0 {
		return nil, fmt.Errorf("LAN discovery: %s", strings.Join(errs, "; "))
	}
	return func() {
		mu.Lock()
		defer mu.Unlock()
		for i := len(stops) - 1; i >= 0; i-- {
			stops[i]()
		}
		stops = nil
	}, nil
}

func instanceName() string {
	h, err := os.Hostname()
	if err != nil || strings.TrimSpace(h) == "" {
		return "houdry"
	}
	h = strings.TrimSpace(h)
	h = strings.ReplaceAll(h, ".", "-")
	if len(h) > 48 {
		h = h[:48]
	}
	return "houdry-" + h
}

func advertiseMDNS(info Info) (StopFunc, error) {
	port, err := parseListenPort(info.Listen)
	if err != nil {
		return nil, err
	}
	server, err := zeroconf.Register(info.Name, ServiceType, Domain, port, txtRecords(info), nil)
	if err != nil {
		return nil, err
	}
	return func() { server.Shutdown() }, nil
}

func advertiseUDP(info Info) (StopFunc, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: UDPPort})
	if err != nil {
		return nil, err
	}
	pc := ipv4.NewPacketConn(conn)
	group := &net.UDPAddr{IP: net.ParseIP(MulticastGroup), Port: UDPPort}
	ifaces, _ := net.Interfaces()
	for i := range ifaces {
		ifi := &ifaces[i]
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		_ = pc.JoinGroup(ifi, group)
	}

	done := make(chan struct{})
	go serveUDP(conn, info, done)

	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			_ = conn.Close()
		})
	}, nil
}

func serveUDP(conn *net.UDPConn, info Info, done <-chan struct{}) {
	buf := make([]byte, 2048)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-done:
				return
			default:
				return
			}
		}
		if addr == nil || !isProbe(buf[:n]) {
			continue
		}
		ep, ok := replyEndpoint(info, addr.IP)
		if !ok {
			continue
		}
		_, _ = conn.WriteToUDP(advertisePayload(ep), addr)
	}
}

func replyEndpoint(info Info, remote net.IP) (Endpoint, bool) {
	port, err := parseListenPort(info.Listen)
	if err != nil {
		return Endpoint{}, false
	}
	ip := bindIP(info.Listen)
	if ip == nil || ip.IsUnspecified() {
		ip = reachableIP(remote)
	}
	if ip == nil {
		return Endpoint{}, false
	}
	if ip.IsLoopback() && remote != nil && !remote.IsLoopback() {
		if lan := reachableIP(remote); lan != nil && !lan.IsLoopback() {
			ip = lan
		}
	}
	return complete(Endpoint{
		Name:         info.Name,
		URL:          httpURL(ip, port),
		Path:         firstNonEmpty(info.Path, DefaultPath),
		Version:      info.Version,
		AuthRequired: info.AuthRequired,
		OpenAI:       info.OpenAI,
		Source:       "udp",
	}), true
}

func reachableIP(remote net.IP) net.IP {
	if remote == nil {
		return firstLANIP()
	}
	r := remote.String()
	if ip4 := remote.To4(); ip4 != nil {
		r = ip4.String()
	}
	c, err := net.Dial("udp4", net.JoinHostPort(r, "9"))
	if err != nil {
		return firstLANIP()
	}
	defer c.Close()
	la, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok || la.IP == nil {
		return firstLANIP()
	}
	return la.IP
}

func firstLANIP() net.IP {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip4 := ip.To4(); ip4 != nil && !ip4.IsLoopback() && !ip4.IsLinkLocalUnicast() {
				return ip4
			}
		}
	}
	return nil
}
