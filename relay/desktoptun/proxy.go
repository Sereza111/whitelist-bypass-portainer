package desktoptun

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
)

func socksProxyURL(host string, port int, user, pass string) (string, string) {
	endpoint := net.JoinHostPort(host, strconv.Itoa(port))
	proxy := &url.URL{Scheme: "socks5", Host: endpoint}
	redacted := fmt.Sprintf("socks5://%s", endpoint)
	if user != "" || pass != "" {
		proxy.User = url.UserPassword(user, pass)
		redacted = fmt.Sprintf("socks5://%s:[REDACTED]@%s", user, endpoint)
	}
	return proxy.String(), redacted
}

func socksBypassIPv4(host string) net.IP {
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	v4 := ip.To4()
	if v4 == nil || v4.IsLoopback() || v4.IsUnspecified() {
		return nil
	}
	return v4
}

// isOnLinkIPv4 reports whether destination is already covered by a connected
// subnet on iface. Such a route is more specific than our /1 split defaults,
// so replacing it with a gateway route is both unnecessary and harmful on
// Wi-Fi networks that do not hairpin traffic between clients.
func isOnLinkIPv4(iface string, destination net.IP) bool {
	if destination == nil {
		return false
	}
	if iface != "" {
		if preferred, err := net.InterfaceByName(iface); err == nil && interfaceContains(preferred, destination) {
			return true
		}
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for index := range interfaces {
		networkInterface := &interfaces[index]
		if networkInterface.Name == iface || networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if interfaceContains(networkInterface, destination) {
			return true
		}
	}
	return false
}

func interfaceContains(networkInterface *net.Interface, destination net.IP) bool {
	addrs, err := networkInterface.Addrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		_, network, parseErr := net.ParseCIDR(addr.String())
		if parseErr == nil && network.Contains(destination) {
			return true
		}
	}
	return false
}
