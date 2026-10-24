//go:build !windows

package main

func registerAssociations() error                { return nil }
func unregisterAssociations() error              { return nil }
func tryRegisterAssociationsOnce(dataDir string) { _ = dataDir }
func associationsStatus() (bool, bool)           { return false, false }
