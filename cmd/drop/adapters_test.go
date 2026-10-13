package main

import (
	"net"
	"strings"
	"testing"
)

func TestVPNNames(t *testing.T) {
	for name, want := range map[string]bool{
		"Radmin VPN":                  true,
		"Tailscale":                   true,
		"ZeroTier One [x]":            true,
		"wg0 (WireGuard)":             true,
		"Hamachi":                     true,
		"Ethernet":                    false,
		"Wi-Fi":                       false,
		"Loopback Pseudo-Interface 1": false,
	} {
		if got := isVPNName(name); got != want {
			t.Errorf("isVPNName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestAdapterListExplainsTheChoice(t *testing.T) {
	var out strings.Builder
	printAdapters(&out, []adapterAddr{
		{"Tailscale", net.ParseIP("100.101.102.103")},
		{"Ethernet", net.ParseIP("192.168.1.20")},
		{"Loopback", net.ParseIP("127.0.0.1")},
	})
	got := out.String()
	for _, want := range []string{
		"-addr 100.101.102.103:8443", "only people on that network",
		"-addr 192.168.1.20:8443",
		"-addr 127.0.0.1:8443", "for testing",
		"-addr :8443",
		settingsName,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestReachDescription(t *testing.T) {
	if got := reachDescription(&net.TCPAddr{IP: net.ParseIP("127.0.0.1")}); got != "this computer only" {
		t.Errorf("loopback: %q", got)
	}
	if got := reachDescription(&net.TCPAddr{IP: net.IPv6unspecified}); !strings.Contains(got, "internet") {
		t.Errorf("every adapter: %q should mention the internet", got)
	}
	if adapterFor(net.ParseIP("127.0.0.1")) == "" {
		t.Error("the loopback adapter should be found")
	}
}
