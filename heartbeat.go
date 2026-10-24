package main

import (
	"log"
	"time"
)

// waitHeartbeat 用于浏览器窗口模式：界面每 2 秒 ping 一次，
// 窗口关闭后超过 grace 未收到 ping 即退出程序。
func (a *App) waitHeartbeat(firstWait, grace time.Duration) {
	start := time.Now()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.quit:
			return
		case <-t.C:
			a.pingMu.Lock()
			last := a.lastPing
			a.pingMu.Unlock()
			if last.IsZero() {
				if time.Since(start) > firstWait {
					log.Println("界面一直未连接，退出")
					return
				}
				continue
			}
			if time.Since(last) > grace {
				log.Println("界面窗口已关闭，退出")
				return
			}
		}
	}
}
