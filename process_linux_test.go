//go:build linux

package procguard

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func processGone(_ *testing.T, pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if os.IsNotExist(err) {
		return true
	}
	if err != nil {
		return false
	}
	fields := strings.Fields(string(data)[strings.LastIndex(string(data), ")")+1:])
	return len(fields) > 0 && (fields[0] == "Z" || fields[0] == "X")
}
