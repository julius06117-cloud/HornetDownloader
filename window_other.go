//go:build !windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const runtimeIsWindows = false
const canPickFolder = false

func prepareChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func attachChild(cmd *exec.Cmd) {}

func fatal(msg string) {
	log.Println("错误:", msg)
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}

func openExternalWindow(url, dataDir string) { _ = exec.Command("xdg-open", url).Start() }

func runWindow(app *App) {
	fmt.Println("请在浏览器中打开:", app.uiURL)
	openExternalWindow(app.uiURL, app.dataDir)
	app.waitHeartbeat(10*time.Minute, 15*time.Second)
}

func pickFolder(initial string) (string, error) {
	return "", fmt.Errorf("当前平台不支持图形化选择，请直接输入路径")
}

func revealInFolder(p string) error {
	if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
		p = filepath.Dir(p)
	}
	return exec.Command("xdg-open", p).Start()
}
