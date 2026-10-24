//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const (
	assocProgID   = "HornetDownloader.Torrent"
	assocMagnetID = "HornetDownloader"
	assocMarker   = "assoc.registered"
)

func exePath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Abs(p)
}

func registerAssociations() error {
	exe, err := exePath()
	if err != nil {
		return err
	}
	// magnet:
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\magnet`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	_ = k.SetStringValue("", "URL:Magnet Protocol")
	_ = k.SetStringValue("URL Protocol", "")
	k.Close()
	k, _, err = registry.CreateKey(registry.CURRENT_USER, `Software\Classes\magnet\shell\open\command`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	_ = k.SetStringValue("", fmt.Sprintf(`"%s" "%s"`, exe, "%1"))
	k.Close()

	// .torrent → ProgID
	k, _, err = registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+assocProgID, registry.SET_VALUE)
	if err != nil {
		return err
	}
	_ = k.SetStringValue("", "BT 种子文件")
	k.Close()
	k, _, err = registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+assocProgID+`\shell\open\command`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	_ = k.SetStringValue("", fmt.Sprintf(`"%s" "%s"`, exe, "%1"))
	k.Close()
	k, _, err = registry.CreateKey(registry.CURRENT_USER, `Software\Classes\.torrent`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	_ = k.SetStringValue("", assocProgID)
	k.Close()
	// 默认程序列表里露个脸
	k, _, err = registry.CreateKey(registry.CURRENT_USER, `Software\Classes\Applications\`+filepath.Base(exe)+`\shell\open\command`, registry.SET_VALUE)
	if err == nil {
		_ = k.SetStringValue("", fmt.Sprintf(`"%s" "%s"`, exe, "%1"))
		k.Close()
	}
	return nil
}

func unregisterAssociations() error {
	_ = registry.DeleteKey(registry.CURRENT_USER, `Software\Classes\magnet\shell\open\command`)
	_ = registry.DeleteKey(registry.CURRENT_USER, `Software\Classes\magnet\shell\open`)
	_ = registry.DeleteKey(registry.CURRENT_USER, `Software\Classes\magnet\shell`)
	_ = registry.DeleteKey(registry.CURRENT_USER, `Software\Classes\magnet`)
	_ = registry.DeleteKey(registry.CURRENT_USER, `Software\Classes\`+assocProgID+`\shell\open\command`)
	_ = registry.DeleteKey(registry.CURRENT_USER, `Software\Classes\`+assocProgID+`\shell\open`)
	_ = registry.DeleteKey(registry.CURRENT_USER, `Software\Classes\`+assocProgID+`\shell`)
	_ = registry.DeleteKey(registry.CURRENT_USER, `Software\Classes\`+assocProgID)
	// 不硬删 .torrent，避免误伤用户其它关联；若当前指向我们则清空
	if k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\.torrent`, registry.QUERY_VALUE|registry.SET_VALUE); err == nil {
		if v, _, e := k.GetStringValue(""); e == nil && v == assocProgID {
			_ = k.SetStringValue("", "")
		}
		k.Close()
	}
	return nil
}

func tryRegisterAssociationsOnce(dataDir string) {
	marker := filepath.Join(dataDir, assocMarker)
	if _, err := os.Stat(marker); err == nil {
		return
	}
	if err := registerAssociations(); err != nil {
		log.Println("自动注册 magnet/.torrent 关联失败:", err)
		return
	}
	_ = os.WriteFile(marker, []byte("1"), 0600)
	log.Println("已注册 magnet: 协议和 .torrent 文件关联")
}

func associationsStatus() (magnet, torrent bool) {
	if k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\magnet\shell\open\command`, registry.QUERY_VALUE); err == nil {
		if v, _, e := k.GetStringValue(""); e == nil {
			exe, _ := exePath()
			magnet = strings.Contains(strings.ToLower(v), strings.ToLower(filepath.Base(exe)))
		}
		k.Close()
	}
	if k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\.torrent`, registry.QUERY_VALUE); err == nil {
		if v, _, e := k.GetStringValue(""); e == nil {
			torrent = v == assocProgID
		}
		k.Close()
	}
	return
}
