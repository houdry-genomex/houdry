// Package discovery finds Houdry control planes on the local LAN/WiFi.
//
// Two transports, same payload:
//
//   - mDNS/DNS-SD service type _houdry._tcp (Bonjour / Avahi)
//   - UDP probe/response on port 41808 (broadcast + multicast), which is more
//     reliable on Windows
//
// Agents and GPU nodes browse; only houdry serve advertises. The HTTP API
// is never guessed: replies include the base URL, and clients confirm with
// GET /.well-known/houdry.json (fallback GET /healthz).
package discovery

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
)

const (
	// ServiceType is the DNS-SD type. Browse with this plus Domain.
	ServiceType = "_houdry._tcp"
	// Domain is the mDNS domain.
	Domain = "local."
	// UDPPort is the probe/response port. Keep in sync with Houdry Agent.
	UDPPort = 41808
	// MulticastGroup is a site-local UDP group for probes.
	MulticastGroup = "239.255.77.77"
	// ProtocolV is the JSON schema version.
	ProtocolV = 1
	// DefaultPath is the OpenAI-compatible API prefix.
	DefaultPath = "/v1"
)

const (
	kindDiscover     = "discover"
	kindControlPlane = "control-plane"
)

// Info is what a control plane advertises.
type Info struct {
	Name         string
	Listen       string // bind address of houdry serve, e.g. 0.0.0.0:8080
	Version      string
	Path         string
	AuthRequired bool
	OpenAI       bool
}

// Endpoint is a reachable control plane.
type Endpoint struct {
	Name         string `json:"name"`
	URL          string `json:"url"` // http://host:port, no trailing slash
	Path         string `json:"path"`
	API          string `json:"api"` // URL + path, what Agent uses as base_url
	Version      string `json:"version,omitempty"`
	AuthRequired bool   `json:"auth"`
	OpenAI       bool   `json:"openai"`
	Source       string `json:"source,omitempty"` // mdns | udp
}

// APIBase returns the OpenAI-shaped base URL (…/v1).
func (e Endpoint) APIBase() string {
	if e.API != "" {
		return e.API
	}
	path := e.Path
	if path == "" {
		path = DefaultPath
	}
	return strings.TrimRight(e.URL, "/") + path
}

type udpMessage struct {
	Houdry  string `json:"houdry"`
	V       int    `json:"v"`
	URL     string `json:"url,omitempty"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	Name    string `json:"name,omitempty"`
	Auth    bool   `json:"auth,omitempty"`
	OpenAI  bool   `json:"openai,omitempty"`
}

func probePayload() []byte {
	b, _ := json.Marshal(udpMessage{Houdry: kindDiscover, V: ProtocolV})
	return b
}

func isProbe(b []byte) bool {
	var m udpMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return false
	}
	return m.Houdry == kindDiscover && m.V == ProtocolV
}

func advertisePayload(ep Endpoint) []byte {
	b, _ := json.Marshal(udpMessage{
		Houdry:  kindControlPlane,
		V:       ProtocolV,
		URL:     ep.URL,
		Path:    firstNonEmpty(ep.Path, DefaultPath),
		Version: ep.Version,
		Name:    ep.Name,
		Auth:    ep.AuthRequired,
		OpenAI:  ep.OpenAI,
	})
	return b
}

func endpointFromUDP(b []byte, source string) (Endpoint, bool) {
	var m udpMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return Endpoint{}, false
	}
	if m.Houdry != kindControlPlane || m.V != ProtocolV || m.URL == "" {
		return Endpoint{}, false
	}
	return complete(Endpoint{
		Name:         m.Name,
		URL:          strings.TrimRight(m.URL, "/"),
		Path:         firstNonEmpty(m.Path, DefaultPath),
		Version:      m.Version,
		AuthRequired: m.Auth,
		OpenAI:       m.OpenAI,
		Source:       source,
	}), true
}

func complete(ep Endpoint) Endpoint {
	if ep.Path == "" {
		ep.Path = DefaultPath
	}
	if !strings.HasPrefix(ep.Path, "/") {
		ep.Path = "/" + ep.Path
	}
	ep.URL = strings.TrimRight(ep.URL, "/")
	ep.API = ep.URL + ep.Path
	return ep
}

func txtRecords(info Info) []string {
	path := firstNonEmpty(info.Path, DefaultPath)
	auth := "0"
	if info.AuthRequired {
		auth = "1"
	}
	openai := "0"
	if info.OpenAI {
		openai = "1"
	}
	return []string{
		"txtvers=1",
		"path=" + path,
		"version=" + info.Version,
		"auth=" + auth,
		"openai=" + openai,
		"proto=http",
	}
}

func endpointFromTXT(instance string, host string, port int, txt []string, ips []net.IP, source string) (Endpoint, bool) {
	ip := pickIP(ips)
	if ip == nil || port <= 0 {
		return Endpoint{}, false
	}
	fields := parseTXT(txt)
	path := firstNonEmpty(fields["path"], DefaultPath)
	ep := complete(Endpoint{
		Name:         firstNonEmpty(instance, host),
		URL:          httpURL(ip, port),
		Path:         path,
		Version:      fields["version"],
		AuthRequired: fields["auth"] == "1" || fields["auth"] == "true",
		OpenAI:       fields["openai"] != "0" && fields["openai"] != "false",
		Source:       source,
	})
	return ep, true
}

func parseTXT(txt []string) map[string]string {
	out := map[string]string{}
	for _, t := range txt {
		k, v, ok := strings.Cut(t, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

func pickIP(ips []net.IP) net.IP {
	var v6 net.IP
	for _, ip := range ips {
		if ip == nil || ip.IsUnspecified() {
			continue
		}
		if ip.IsLoopback() {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			return ip4
		}
		if v6 == nil {
			v6 = ip
		}
	}
	if v6 != nil {
		return v6
	}
	for _, ip := range ips {
		if ip != nil && !ip.IsUnspecified() {
			return ip
		}
	}
	return nil
}

func httpURL(ip net.IP, port int) string {
	host := ip.String()
	if ip.To4() == nil {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("http://%s:%d", host, port)
}

func parseListenPort(listen string) (int, error) {
	_, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return 0, fmt.Errorf("listen address %q: %w", listen, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		return 0, fmt.Errorf("listen port %q is invalid", portStr)
	}
	return port, nil
}

func bindIP(listen string) net.IP {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return nil
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		return nil
	}
	return net.ParseIP(strings.Trim(host, "[]"))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func unique(in []Endpoint) []Endpoint {
	seen := map[string]int{}
	out := make([]Endpoint, 0, len(in))
	for _, ep := range in {
		key := ep.URL
		if i, ok := seen[key]; ok {
			if out[i].Name == "" && ep.Name != "" {
				src := out[i].Source
				out[i] = ep
				if out[i].Source == "" {
					out[i].Source = src
				}
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, ep)
	}
	return out
}
