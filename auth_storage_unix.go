//go:build !windows

package main

import "os"

func authPrivateMode(st os.FileInfo) bool                  { return st.Mode().Perm()&0077 == 0 }
func sealAuthData(_ string, data []byte) ([]byte, error)   { return data, nil }
func unsealAuthData(_ string, data []byte) ([]byte, error) { return data, nil }
