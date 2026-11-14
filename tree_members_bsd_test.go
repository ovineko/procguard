//go:build freebsd || openbsd || netbsd || dragonfly

package procguard

import (
	"encoding/binary"
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestBSDRecordsRemaining(t *testing.T) {
	const leader = 4100
	cases := []struct {
		name    string
		records []bsdTestRecord
		want    bool
	}{
		{name: "leader only", records: []bsdTestRecord{{pid: leader, pgid: leader, state: 1}}},
		{name: "live descendant", records: []bsdTestRecord{{pid: leader, pgid: leader, state: 1}, {pid: leader + 1, pgid: leader, state: 1}}, want: true},
		{name: "dead descendant", records: []bsdTestRecord{{pid: leader + 1, pgid: leader, state: bsdDeadState1}}},
		{name: "different group", records: []bsdTestRecord{{pid: leader + 1, pgid: leader + 2, state: 1}}},
		{name: "unknown state is live", records: []bsdTestRecord{{pid: leader + 1, pgid: leader, state: 0xff}}, want: true},
	}
	if bsdDeadStateCount > 1 {
		cases = append(cases, struct {
			name    string
			records []bsdTestRecord
			want    bool
		}{name: "second dead state", records: []bsdTestRecord{{pid: leader + 1, pgid: leader, state: bsdDeadState2}}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := make([]byte, len(tc.records)*bsdRecordSize)
			for i, rec := range tc.records {
				writeBSDTestField(data[i*bsdRecordSize:], bsdPIDOffset, bsdPIDWidth, uint64(rec.pid))
				writeBSDTestField(data[i*bsdRecordSize:], bsdPGIDOffset, bsdPGIDWidth, uint64(rec.pgid))
				writeBSDTestField(data[i*bsdRecordSize:], bsdStateOffset, bsdStateWidth, rec.state)
				if bsdStructSizeWidth != 0 {
					writeBSDTestField(data[i*bsdRecordSize:], bsdStructSizeOffset, bsdStructSizeWidth, uint64(bsdRecordSize))
				}
			}
			got, err := bsdRecordsRemaining(data, leader)
			if err != nil || got != tc.want {
				t.Fatalf("remaining=%v err=%v, want %v", got, err, tc.want)
			}
		})
	}
}

type bsdTestRecord struct {
	pid, pgid int
	state     uint64
}

func writeBSDTestField(dst []byte, offset, width int, value uint64) {
	dst = dst[offset:]
	switch width {
	case 1:
		dst[0] = byte(value)
	case 2:
		binary.NativeEndian.PutUint16(dst, uint16(value))
	case 4:
		binary.NativeEndian.PutUint32(dst, uint32(value))
	case 8:
		binary.NativeEndian.PutUint64(dst, value)
	default:
		panic("unsupported generated field width")
	}
}

func TestBSDRecordsRejectMalformedData(t *testing.T) {
	if _, err := bsdRecordsRemaining(make([]byte, bsdRecordSize-1), 1); err == nil {
		t.Fatal("partial process record accepted")
	}
	if bsdStructSizeWidth != 0 {
		data := make([]byte, bsdRecordSize)
		writeBSDTestField(data, bsdStructSizeOffset, bsdStructSizeWidth, uint64(bsdRecordSize+1))
		if _, err := bsdRecordsRemaining(data, 1); err == nil {
			t.Fatal("mismatched FreeBSD record size accepted")
		}
	}
}

func TestBSDGroupRemainingRetriesOnlyTransientQueryErrors(t *testing.T) {
	transient := []error{unix.EINTR, unix.ENOMEM}
	for _, firstErr := range transient {
		t.Run(firstErr.Error(), func(t *testing.T) {
			calls := 0
			got, err := bsdGroupRemaining(1, func() ([]byte, error) {
				calls++
				if calls == 1 {
					return []byte{1}, firstErr
				}
				return nil, nil
			})
			if err != nil || got || calls != 2 {
				t.Fatalf("remaining=%v err=%v calls=%d", got, err, calls)
			}
		})
	}
	failure := errors.New("query failed")
	calls := 0
	if _, err := bsdGroupRemaining(1, func() ([]byte, error) {
		calls++
		return nil, failure
	}); !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
	calls = 0
	if _, err := bsdGroupRemaining(1, func() ([]byte, error) {
		calls++
		return nil, unix.ENOMEM
	}); !errors.Is(err, unix.ENOMEM) || calls != 4 {
		t.Fatalf("exhausted error=%v calls=%d", err, calls)
	}
}
