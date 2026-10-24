//go:build windows

package main

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed embed/aria2c.exe
var embeddedAria2 []byte

//go:embed embed/aria2-COPYING.txt
var embeddedAria2License []byte

// ensureAria2 把内嵌的 aria2c.exe 释放到固定路径 %APPDATA%\HornetDownloader\bin\aria2c.exe。
// 路径固定不变（防火墙规则按程序路径生效）；仅当文件内容（SHA-256）与内嵌版本不同才重写。
func ensureAria2(dataDir string) (string, error) {
	dir := filepath.Join(dataDir, "bin")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, "aria2c.exe")
	lic := filepath.Join(dir, "aria2-COPYING.txt")
	if sameContent(dest, embeddedAria2) {
		if _, err := os.Stat(lic); err != nil {
			_ = os.WriteFile(lic, embeddedAria2License, 0600)
		}
		return dest, nil
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, embeddedAria2, 0700); err != nil {
		return "", fmt.Errorf("写出 aria2c 失败: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		// Windows 上若旧文件正被占用，Rename 可能失败；再试直接覆盖写入
		if err2 := os.WriteFile(dest, embeddedAria2, 0700); err2 != nil {
			return "", fmt.Errorf("更新 aria2c 失败: %v / %v", err, err2)
		}
	}
	_ = os.WriteFile(lic, embeddedAria2License, 0600)
	return dest, nil
}

// sameContent 判断磁盘文件与内嵌数据是否完全相同（先比大小，再比 SHA-256）。
func sameContent(path string, want []byte) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() != int64(len(want)) {
		return false
	}
	got, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	a, b := sha256.Sum256(got), sha256.Sum256(want)
	return bytes.Equal(a[:], b[:])
}
