// hdl 是 HornetDownloader 的控制台版命令行（console 子系统），供 PowerShell/cmd/编程助手使用：
// stdout、退出码在任何调用方式下都可靠。用法见 `hdl help` 与 docs/AGENT_API.md。
package main

import (
	"os"

	"hornetdl/internal/agentcli"
)

func main() {
	os.Exit(agentcli.Main(os.Args[1:], agentcli.Options{}))
}
