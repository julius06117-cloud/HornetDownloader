package main

import (
	"strings"
	"testing"
)

// aria2 is HTTP-first: it must never open DHT/LPD/PEX sockets or a non-loopback RPC
// listener (those trigger Windows Firewall prompts). BT is handled by the BT engine.
func TestAria2ArgsLoopbackOnly(t *testing.T) {
	a := &App{cfg: defaultConfig(), dataDir: t.TempDir()}
	args := strings.Join(a.ariaArgs(), "\n") + "\n"
	for _, want := range []string{
		"--rpc-listen-all=false\n",
		"--enable-dht=false\n",
		"--enable-dht6=false\n",
		"--bt-enable-lpd=false\n",
		"--enable-peer-exchange=false\n",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("aria2 args missing %q", strings.TrimSpace(want))
		}
	}
	for _, bad := range []string{"--rpc-listen-all=true", "--enable-dht=true", "--enable-dht6=true", "--bt-enable-lpd=true", "--enable-peer-exchange=true", "--dht-listen-port"} {
		if strings.Contains(args, bad) {
			t.Errorf("aria2 args must not contain %q", bad)
		}
	}
}

func TestConfigBTPortNormalize(t *testing.T) {
	a := &App{}
	for in, want := range map[int]int{0: 0, 6881: 6881, 65535: 65535, 80: 0, -1: 0, 70000: 0} {
		c := defaultConfig()
		c.BTPort = in
		a.normalize(&c)
		if c.BTPort != want {
			t.Errorf("BTPort %d normalized to %d, want %d", in, c.BTPort, want)
		}
	}
}
