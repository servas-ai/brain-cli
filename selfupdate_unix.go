//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func selfUpdateOwned(st os.FileInfo) bool {
	s, ok := st.Sys().(*syscall.Stat_t)
	return ok && s.Uid == uint32(os.Getuid()) && s.Nlink == 1
}
func selfUpdatePrivateDir(st os.FileInfo) bool {
	s, ok := st.Sys().(*syscall.Stat_t)
	return ok && s.Uid == uint32(os.Getuid())
}
func selfUpdatePlatform() error { return nil }
func selfUpdateSpawn(exe string) error {
	c := exec.Command(exe, "self-update", "_background")
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return errors.New("cannot start update background process")
	}
	return c.Process.Release()
}
