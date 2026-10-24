//go:build !windows

package main

func processMemory() (ws, private, peak uint64) { return 0, 0, 0 }
