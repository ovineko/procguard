//go:build freebsd || openbsd || netbsd || dragonfly

package procguard

import (
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func bsdGroupRemaining(pid int, query func() ([]byte, error)) (bool, error) {
	for attempt := 0; attempt < 4; attempt++ {
		data, err := query()
		if err == nil {
			return bsdRecordsRemaining(data, pid)
		}
		if !errors.Is(err, unix.EINTR) && !errors.Is(err, unix.ENOMEM) {
			return false, fmt.Errorf("query BSD process group: %w", err)
		}
		if attempt == 3 {
			return false, fmt.Errorf("query BSD process group after retries: %w", err)
		}
	}
	panic("unreachable")
}

func bsdRecordsRemaining(data []byte, pid int) (bool, error) {
	if bsdRecordSize <= 0 || len(data)%bsdRecordSize != 0 {
		return false, fmt.Errorf("invalid BSD process record data length %d for stride %d", len(data), bsdRecordSize)
	}
	fields := [...]struct{ offset, width int }{
		{bsdPIDOffset, bsdPIDWidth},
		{bsdPGIDOffset, bsdPGIDWidth},
		{bsdStateOffset, bsdStateWidth},
	}
	for _, field := range fields {
		if !validBSDField(field.offset, field.width, bsdRecordSize) {
			return false, fmt.Errorf("invalid BSD process field offset=%d width=%d stride=%d", field.offset, field.width, bsdRecordSize)
		}
	}
	if bsdStructSizeWidth != 0 && !validBSDField(bsdStructSizeOffset, bsdStructSizeWidth, bsdRecordSize) {
		return false, fmt.Errorf("invalid BSD struct-size field offset=%d width=%d stride=%d", bsdStructSizeOffset, bsdStructSizeWidth, bsdRecordSize)
	}

	for record := 0; record < len(data); record += bsdRecordSize {
		if bsdStructSizeWidth != 0 {
			stride, err := bsdNativeUint(data[record+bsdStructSizeOffset:], bsdStructSizeWidth)
			if err != nil {
				return false, err
			}
			if stride != uint64(bsdRecordSize) {
				return false, fmt.Errorf("FreeBSD process record size %d does not match generated stride %d", stride, bsdRecordSize)
			}
		}
		recordPID, err := bsdNativeUint(data[record+bsdPIDOffset:], bsdPIDWidth)
		if err != nil {
			return false, err
		}
		recordPGID, err := bsdNativeUint(data[record+bsdPGIDOffset:], bsdPGIDWidth)
		if err != nil {
			return false, err
		}
		if recordPID == uint64(pid) || recordPGID != uint64(pid) {
			continue
		}
		state, err := bsdNativeUint(data[record+bsdStateOffset:], bsdStateWidth)
		if err != nil {
			return false, err
		}
		if !bsdDeadState(state) {
			return true, nil
		}
	}
	return false, nil
}

func validBSDField(offset, width, stride int) bool {
	if offset < 0 || width < 0 || offset > stride || width > stride-offset {
		return false
	}
	switch width {
	case 1, 2, 4, 8:
		return true
	default:
		return false
	}
}

func bsdNativeUint(data []byte, width int) (uint64, error) {
	if len(data) < width {
		return 0, fmt.Errorf("truncated BSD process field: need %d bytes, have %d", width, len(data))
	}
	switch width {
	case 1:
		return uint64(data[0]), nil
	case 2:
		return uint64(binary.NativeEndian.Uint16(data)), nil
	case 4:
		return uint64(binary.NativeEndian.Uint32(data)), nil
	case 8:
		return binary.NativeEndian.Uint64(data), nil
	default:
		return 0, fmt.Errorf("unsupported BSD process field width %d (supported widths: 1, 2, 4, 8)", width)
	}
}

func bsdDeadState(state uint64) bool {
	states := [...]uint64{bsdDeadState1, bsdDeadState2, bsdDeadState3}
	for i := 0; i < bsdDeadStateCount; i++ {
		if state == states[i] {
			return true
		}
	}
	return false
}
