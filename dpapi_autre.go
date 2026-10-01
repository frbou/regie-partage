//go:build !windows

package main

// Hors Windows (développement, Mac) : pas de DPAPI, le fichier jeton.bin est
// seulement protégé par ses droits (0600).

func proteger(clair []byte) ([]byte, error)     { return clair, nil }
func deproteger(chiffre []byte) ([]byte, error) { return chiffre, nil }
