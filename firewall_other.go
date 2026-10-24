//go:build !windows

package main

import "errors"

const firewallSupported = false

func applyFirewallRules(action string, public bool, aria2 string) error {
	return errors.New("仅 Windows 支持")
}

func requestFirewallRules(action string, public bool, aria2 string) error {
	return errors.New("仅 Windows 支持")
}

func firewallStatus() (exists, pathOK bool) { return false, false }
