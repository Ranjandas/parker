package spec

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ByteSize is a size in bytes. In a lab file it is a number of bytes or
// a string with a unit: 8KB, 1MB, 2GB (KB is 1024 bytes; KiB, K and k
// work too).
type ByteSize int64

// ParseByteSize reads 8192, "8192", "8KB", "1.5MB" or "1MiB".
func ParseByteSize(s string) (ByteSize, error) {
	t := strings.TrimSpace(s)
	i := strings.IndexFunc(t, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	num, unit := t, ""
	if i >= 0 {
		num, unit = t[:i], strings.TrimSpace(t[i:])
	}
	n, err := strconv.ParseFloat(num, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("want a size like 8192, 8KB or 1MB, got %q", s)
	}
	mult := map[string]float64{"": 1, "b": 1, "k": 1 << 10, "kb": 1 << 10, "kib": 1 << 10,
		"m": 1 << 20, "mb": 1 << 20, "mib": 1 << 20, "g": 1 << 30, "gb": 1 << 30, "gib": 1 << 30}[strings.ToLower(unit)]
	if mult == 0 {
		return 0, fmt.Errorf("want a size like 8192, 8KB or 1MB, got %q", s)
	}
	return ByteSize(n * mult), nil
}

// String is the size in the largest unit that keeps it whole: 8KB, 1MB,
// 9120B.
func (b ByteSize) String() string {
	for _, u := range []struct {
		n    ByteSize
		name string
	}{{1 << 30, "GB"}, {1 << 20, "MB"}, {1 << 10, "KB"}} {
		if b >= u.n && b%u.n == 0 {
			return fmt.Sprintf("%d%s", b/u.n, u.name)
		}
	}
	return fmt.Sprintf("%dB", int64(b))
}

func (b *ByteSize) UnmarshalYAML(unmarshal func(any) error) error {
	var n int64
	if err := unmarshal(&n); err == nil {
		if n < 0 {
			return fmt.Errorf("want a size of 0 or more, got %d", n)
		}
		*b = ByteSize(n)
		return nil
	}
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	v, err := ParseByteSize(s)
	if err != nil {
		return err
	}
	*b = v
	return nil
}

func (b *ByteSize) UnmarshalJSON(data []byte) error {
	var n int64
	if err := json.Unmarshal(data, &n); err == nil {
		*b = ByteSize(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	v, err := ParseByteSize(s)
	if err != nil {
		return err
	}
	*b = v
	return nil
}
