package ufw

import "testing"

func TestParseNumberedRules(t *testing.T) {
	out := `Status: active

To                         Action      From
--                         ------      ----
[ 1] 22/tcp                     ALLOW IN    Anywhere
[ 2] 22/tcp (v6)                ALLOW IN    Anywhere (v6)
[ 3] 443/udp                    DENY OUT    10.0.0.0/8
[ 4] Anywhere                   REJECT IN   192.168.1.5 on eth0
[ 5] 8080/tcp (v6)              LIMIT IN    Anywhere (v6)
[10] 3999/tcp                   ALLOW IN    Anywhere
`
	rules := parseNumbered(out)
	if len(rules) != 6 {
		t.Fatalf("want 6 rules, got %d: %+v", len(rules), rules)
	}
	v4 := rules[0]
	if v4.Number != 1 || v4.To != "22/tcp" || v4.Action != "ALLOW" || v4.Direction != "IN" || v4.From != "Anywhere" || v4.Interface != "" {
		t.Fatalf("v4 row parsed wrong: %+v", v4)
	}
	v6 := rules[1]
	if v6.Number != 2 || v6.To != "22/tcp" || v6.From != "Anywhere" || v6.Direction != "IN" {
		t.Fatalf("v6 row parsed wrong: %+v", v6)
	}
	if iface := rules[3]; iface.Interface != "eth0" || iface.From != "192.168.1.5" || iface.Direction != "IN" {
		t.Fatalf("interface row parsed wrong: %+v", iface)
	}
	if two := rules[4]; two.To != "8080/tcp" || two.Action != "LIMIT" || two.From != "Anywhere" {
		t.Fatalf("limit row parsed wrong: %+v", two)
	}
	if ten := rules[5]; ten.Number != 10 {
		t.Fatalf("multi-digit number parsed wrong: %+v", ten)
	}
}

func TestParseNumberedIgnoresHeader(t *testing.T) {
	out := `Status: active

To                         Action      From
--                         ------      ----
`
	if rules := parseNumbered(out); len(rules) != 0 {
		t.Fatalf("want no rules, got %+v", rules)
	}
}

func TestParseVerboseActive(t *testing.T) {
	out := `Status: active
Logging: on (low)
Default: deny (incoming), allow (outgoing)
New profiles: skip
`
	enabled, defIn, defOut := parseVerbose(out)
	if !enabled || defIn != "deny" || defOut != "allow" {
		t.Fatalf("want active/deny/allow, got %v/%s/%s", enabled, defIn, defOut)
	}
}

func TestParseVerboseInactive(t *testing.T) {
	out := `Status: inactive
Logging: off
Default: allow (incoming), allow (outgoing)
`
	enabled, defIn, defOut := parseVerbose(out)
	if enabled || defIn != "allow" || defOut != "allow" {
		t.Fatalf("want inactive/allow/allow, got %v/%s/%s", enabled, defIn, defOut)
	}
}
