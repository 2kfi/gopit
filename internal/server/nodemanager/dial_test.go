package nodemanager

import "testing"

func TestJoinHost(t *testing.T) {
	for _, c := range []struct{ ip string; port int; want string }{
		{"10.0.0.5", 1221, "10.0.0.5:1221"},
		{"node.local", 1221, "node.local:1221"},
		{"::1", 1221, "[::1]:1221"},
		{"2001:db8::1", 443, "[2001:db8::1]:443"},
	} {
		if got := joinHost(c.ip, c.port); got != c.want {
			t.Errorf("joinHost(%q, %d) = %q, want %q", c.ip, c.port, got, c.want)
		}
	}
}
