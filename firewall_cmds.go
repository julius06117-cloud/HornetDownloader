package main

import (
	"fmt"
	"os"
	"strings"
)

const firewallRuleName = "HornetDownloader"

// firewallNetshCommands 生成 netsh 参数（不含 netsh.exe 本身）：先删除同名规则（幂等），
// action=add 时再为主程序与 aria2c 各加一条入站允许规则。纯函数，便于测试与 --firewall-dry-run。
func firewallNetshCommands(action string, public bool, exe, aria2 string) ([]string, error) {
	cmds := []string{`advfirewall firewall delete rule name="` + firewallRuleName + `"`}
	switch action {
	case "remove":
		return cmds, nil
	case "add":
	default:
		return nil, fmt.Errorf("未知操作 %q", action)
	}
	profile := "private"
	if public {
		profile = "private,public"
	}
	progs := []string{exe}
	if aria2 != "" {
		if _, err := os.Stat(aria2); err == nil {
			progs = append(progs, aria2)
		}
	}
	for _, p := range progs {
		if strings.ContainsAny(p, "\"\r\n") || strings.TrimSpace(p) == "" {
			return nil, fmt.Errorf("路径不安全，拒绝写入防火墙规则: %q", p)
		}
		cmds = append(cmds, `advfirewall firewall add rule name="`+firewallRuleName+
			`" dir=in action=allow enable=yes profile=`+profile+` program="`+p+`"`)
	}
	return cmds, nil
}
