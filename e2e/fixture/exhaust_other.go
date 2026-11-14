//go:build e2e && !linux

package main

import "github.com/ovineko/procguard"

func exhaustedPIDs(config) error { return procguard.ErrUnsupported }
