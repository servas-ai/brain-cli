//go:build windows

package main

import (
	"errors"
	"os"
)

// Windows authentication/check are prepared, including DPAPI token storage.
// Atomic replacement and detached background execution still need a separate
// implementation and native runtime verification; this preparation fails closed.
func selfUpdateOwned(st os.FileInfo) bool   { return st.Mode().IsRegular() }
func selfUpdatePrivateDir(os.FileInfo) bool { return false }
func selfUpdatePlatform() error {
	return errors.New("Windows binary replacement is not implemented in this preparation")
}
func selfUpdateSpawn(string) error { return selfUpdatePlatform() }
