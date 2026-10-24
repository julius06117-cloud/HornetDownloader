//go:build !windows

package main

import "fmt"

func ensureAria2(dataDir string) (string, error) {
	_ = dataDir
	return "", fmt.Errorf("非 Windows 构建未内嵌 aria2c，请用 -aria2 指定路径或放入 PATH")
}
