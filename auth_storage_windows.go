//go:build windows

package main

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

// DPAPI binds the encrypted session to the current Windows user. Go chmod does
// not provide POSIX 0600 confidentiality on Windows; plaintext tokens are never
// written there. No application private key or password is generated/stored.
// API contract: https://learn.microsoft.com/windows/win32/api/dpapi/nf-dpapi-cryptprotectdata
type authBlob struct {
	Size uint32
	Data *byte
}

var authCrypt32 = syscall.NewLazyDLL("crypt32.dll")
var authKernel32 = syscall.NewLazyDLL("kernel32.dll")

func authPrivateMode(_ os.FileInfo) bool { return true }
func authDPAPI(data []byte, protect bool) ([]byte, error) {
	if len(data) == 0 || len(data) > 1<<20 {
		return nil, errors.New("invalid protected authentication data")
	}
	in := authBlob{uint32(len(data)), &data[0]}
	var out authBlob
	name := "CryptUnprotectData"
	if protect {
		name = "CryptProtectData"
	}
	ok, _, _ := authCrypt32.NewProc(name).Call(uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, 1, uintptr(unsafe.Pointer(&out)))
	if ok == 0 {
		return nil, errors.New("Windows user token protection failed")
	}
	defer authKernel32.NewProc("LocalFree").Call(uintptr(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}
func sealAuthData(name string, data []byte) ([]byte, error) {
	if name != "session.json" && name != "update-session.json" {
		return data, nil
	}
	return authDPAPI(data, true)
}
func unsealAuthData(name string, data []byte) ([]byte, error) {
	if name != "session.json" && name != "update-session.json" {
		return data, nil
	}
	return authDPAPI(data, false)
}
