//go:build !windows

package main

import "log"

func (a *App) watchClipboard() {
	log.Println("当前平台未启用剪贴板监视")
}
