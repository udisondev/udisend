package dht

import (
	"net"
	"testing"
	"time"
)

func mkAddr(t *testing.T, s string) net.Addr {
	t.Helper()
	a, err := net.ResolveUDPAddr("udp", s)
	if err != nil {
		t.Fatal(err)
	}

	return a
}

func TestBucket_SubnetCap(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		ips        []string
		wantInBkt  int
	}{
		{
			name: "different subnets all admitted",
			ips: []string{
				"10.1.1.1:9000", "10.2.2.2:9000", "10.3.3.3:9000",
				"10.4.4.4:9000", "10.5.5.5:9000",
			},
			wantInBkt: 5,
		},
		{
			name: "same /24: at most MaxContactsPerSubnet (=2)",
			ips: []string{
				"10.1.1.1:9000", "10.1.1.2:9000", "10.1.1.3:9000",
				"10.1.1.4:9000", "10.1.1.5:9000",
			},
			wantInBkt: MaxContactsPerSubnet,
		},
		{
			name: "loopback exempt — many same-prefix addresses fit",
			ips: []string{
				"127.0.0.1:9001", "127.0.0.1:9002", "127.0.0.1:9003",
				"127.0.0.1:9004", "127.0.0.1:9005", "127.0.0.1:9006",
			},
			wantInBkt: 6,
		},
		{
			name: "ipv6 same /64 capped",
			ips: []string{
				"[2001:db8::1]:9000", "[2001:db8::2]:9000", "[2001:db8::3]:9000",
				"[2001:db8::4]:9000",
			},
			wantInBkt: MaxContactsPerSubnet,
		},
		{
			name: "ipv6 different /64 admitted",
			ips: []string{
				"[2001:db8:1::1]:9000", "[2001:db8:2::1]:9000",
				"[2001:db8:3::1]:9000", "[2001:db8:4::1]:9000",
			},
			wantInBkt: 4,
		},
		{
			name: "ipv4-mapped-ipv6 collapses with raw ipv4",
			ips: []string{
				"10.1.1.1:9000",
				"[::ffff:10.1.1.2]:9000",
				"[::ffff:10.1.1.3]:9000",
			},
			wantInBkt: MaxContactsPerSubnet,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBucket(20)
			now := time.Now()
			for i, ip := range tc.ips {
				var id NodeID
				id[0] = byte(i + 1)
				b.add(Contact{ID: id, Addr: mkAddr(t, ip)}, now)
			}
			if got := len(b.contacts); got != tc.wantInBkt {
				t.Fatalf("len = %d, want %d", got, tc.wantInBkt)
			}
		})
	}
}

func TestBucket_RefreshIgnoresSubnetCap(t *testing.T) {
	t.Parallel()
	b := newBucket(20)
	now := time.Now()
	var idA, idB NodeID
	idA[0] = 1
	idB[0] = 2
	b.add(Contact{ID: idA, Addr: mkAddr(t, "10.1.1.1:9000")}, now)
	b.add(Contact{ID: idB, Addr: mkAddr(t, "10.1.1.2:9000")}, now)

	// Refresh on idA — same subnet, but it's an existing entry, must succeed.
	_, refreshed := b.add(Contact{ID: idA, Addr: mkAddr(t, "10.1.1.1:9000")}, now)
	if !refreshed {
		t.Fatalf("refresh of existing contact should succeed despite subnet cap")
	}
}
