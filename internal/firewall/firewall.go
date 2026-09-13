package firewall

import (
	"fmt"
	"strconv"
	"strings"
)

// ExtractPortFromAddr extracts the port number from an address string like "0.0.0.0:8090" or ":8090"
func ExtractPortFromAddr(addr string) (int, error) {
	parts := strings.Split(addr, ":")
	if len(parts) < 2 {
		return 0, fmt.Errorf("invalid address format: %s", addr)
	}

	portStr := parts[len(parts)-1]
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("invalid port number: %s", portStr)
	}

	return port, nil
}
