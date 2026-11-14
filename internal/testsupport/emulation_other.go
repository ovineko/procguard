//go:build !linux

package testsupport

func userEmulation() (bool, error) { return false, nil }
