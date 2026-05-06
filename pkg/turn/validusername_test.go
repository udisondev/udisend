package turn

import (
	"strings"
	"testing"
	"time"
)

func TestValidUsername(t *testing.T) {
	t.Parallel()
	now := time.Now().Unix()
	maxLifetime := 24 * time.Hour

	mk := func(expiryUnix int64, user string) string {
		return formatUsername(expiryUnix, user)
	}

	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"no colon", "12345", false},
		{"trailing colon (no user portion)", mk(now+60, ""), false},
		{"leading colon", ":alice", false},
		{"non-numeric expiry", "abc:alice", false},
		{"expired", mk(now-1, "alice"), false},
		{"current edge — exactly now", mk(now, "alice"), false},
		{"valid current", mk(now+60, "alice"), true},
		{"valid up to maxLifetime", mk(now+int64(maxLifetime/time.Second), "alice"), true},
		{"too far future", mk(now+int64(maxLifetime/time.Second)+1, "alice"), false},
		{"overlong > 256", mk(now+60, strings.Repeat("a", 260)), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := validUsername(tc.in, maxLifetime)
			if got != tc.want {
				t.Errorf("validUsername(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func formatUsername(expiryUnix int64, user string) string {
	return string([]byte(intToASCII(expiryUnix))) + ":" + user
}

func intToASCII(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
