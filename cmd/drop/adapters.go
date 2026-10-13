package main

import (
	"fmt"
	"io"
	"net"
	"strings"
)

// adapterAddr is one IPv4 address on one network adapter, such as
// "Tailscale" 100.101.102.103 or "Ethernet" 192.168.1.20.
type adapterAddr struct {
	adapter string
	ip      net.IP
}

func adapterAddrs() []adapterAddr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var list []adapterAddr
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil && !ipnet.IP.IsLinkLocalUnicast() {
				list = append(list, adapterAddr{iface.Name, ipnet.IP})
			}
		}
	}
	return list
}

// adapterFor names the adapter that owns ip, or "" if none does.
func adapterFor(ip net.IP) string {
	for _, a := range adapterAddrs() {
		if a.ip.Equal(ip) {
			return a.adapter
		}
	}
	return ""
}

// printAdapters shows where drop could listen, so the choice is deliberate.
func printAdapters(w io.Writer, list []adapterAddr) {
	fmt.Fprintln(w, "drop: choose where to listen with -addr. This computer's addresses:")
	fmt.Fprintln(w)
	width := 0
	for _, a := range list {
		width = max(width, len(a.adapter))
	}
	for _, a := range list {
		note := ""
		switch {
		case a.ip.IsLoopback():
			note = "this computer only, for testing"
		case isVPNName(a.adapter):
			note = "a VPN: only people on that network can reach it"
		}
		fmt.Fprintf(w, "  %-*s  -addr %-21s %s\n", width, a.adapter, a.ip.String()+":8443", note)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "For a router port forward, listen on every adapter: -addr :8443 -host <your public IP>")
	fmt.Fprintln(w, "(any port works, as long as the router forwards to the same one).")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "To skip this next time, save the choice in %s: {\"addr\": \":8443\"}\n", settingsName)
}

// isVPNName recognizes common VPN adapters by name. It only adds a hint to
// the list above; drop never chooses an address on its own.
func isVPNName(adapter string) bool {
	name := strings.ToLower(adapter)
	for _, v := range []string{"radmin", "tailscale", "zerotier", "wireguard", "hamachi", "netbird", "vpn"} {
		if strings.Contains(name, v) {
			return true
		}
	}
	return false
}

// reachDescription says, for the banner, who can reach the listen address.
func reachDescription(addr *net.TCPAddr) string {
	switch {
	case addr.IP.IsLoopback():
		return "this computer only"
	case addr.IP.IsUnspecified():
		return "every adapter: your LAN, your VPNs, and the internet if a port is forwarded"
	}
	adapter := adapterFor(addr.IP)
	if isVPNName(adapter) {
		return adapter + " only: just the people on that network"
	}
	if adapter != "" {
		return adapter + " only"
	}
	return "one adapter"
}
