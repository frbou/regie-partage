//go:build windows

package main

import (
	"errors"
	"syscall"
	"unsafe"
)

// Chiffrement DPAPI : seul l'utilisateur Windows courant, sur ce poste,
// peut relire le jeton de connexion.

var (
	crypt32            = syscall.NewLazyDLL("crypt32.dll")
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procCryptProtect   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotect = crypt32.NewProc("CryptUnprotectData")
	procLocalFree      = kernel32.NewProc("LocalFree")
)

type blob struct {
	n uint32
	p *byte
}

func versBlob(b []byte) *blob {
	if len(b) == 0 {
		return &blob{}
	}
	return &blob{n: uint32(len(b)), p: &b[0]}
}

func (b *blob) octets() []byte {
	out := make([]byte, b.n)
	copy(out, unsafe.Slice(b.p, b.n))
	return out
}

const cryptprotectUIForbidden = 0x1

func appelDPAPI(proc *syscall.LazyProc, data []byte) ([]byte, error) {
	var sortie blob
	r, _, err := proc.Call(uintptr(unsafe.Pointer(versBlob(data))), 0, 0, 0, 0,
		cryptprotectUIForbidden, uintptr(unsafe.Pointer(&sortie)))
	if r == 0 {
		return nil, errors.New("DPAPI : " + err.Error())
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(sortie.p)))
	return sortie.octets(), nil
}

func proteger(clair []byte) ([]byte, error)     { return appelDPAPI(procCryptProtect, clair) }
func deproteger(chiffre []byte) ([]byte, error) { return appelDPAPI(procCryptUnprotect, chiffre) }
