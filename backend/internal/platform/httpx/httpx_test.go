package httpx

import "testing"

func TestIPBucketGroupsIPv6By64(t *testing.T) {
	a := IPBucket("2001:db8:1:2:aaaa::1")
	b := IPBucket("2001:db8:1:2:ffff::9")
	if a != b || a != "2001:db8:1:2::/64" {
		t.Fatalf("same /64 must share a bucket: %q %q", a, b)
	}
	if IPBucket("2001:db8:1:3::1") == a {
		t.Fatal("different /64 must not share a bucket")
	}
	if IPBucket("203.0.113.7") != "203.0.113.7" {
		t.Fatal("IPv4 stays as is")
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(60, 2)
	ok1, _ := l.Allow("k")
	ok2, _ := l.Allow("k")
	ok3, wait := l.Allow("k")
	if !ok1 || !ok2 || ok3 || wait <= 0 {
		t.Fatalf("burst of 2 then refuse: %v %v %v %v", ok1, ok2, ok3, wait)
	}
}
