//go:build linux

package nftfw

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/nftables"
)

func TestRenderConfRoundTrip(t *testing.T) {
	rules := []lrule{
		{proto: "tcp", port: 22, action: "allow"},
		{proto: "both", port: 8080, action: "allow", from: "192.168.1.5", iface: "eth0"},
		{proto: "udp", port: 53, action: "reject"},
		{proto: "tcp", port: 25, action: "reject"},
		{proto: "udp", port: 111, action: "deny", from: "::1"},
	}
	for _, enabled := range []bool{true, false} {
		text := renderConf(enabled, rules)
		got, gotRules, err := loadConf(t.TempDir() + "/x")
		if err != nil {
			t.Fatalf("loadConf missing file: %v", err)
		}
		if got != false || gotRules != nil {
			t.Fatalf("missing file should be inactive/empty, got %v %v", got, gotRules)
		}
		path := filepath.Join(t.TempDir(), "nftables.conf")
		if err := writeConf(path, enabled, rules); err != nil {
			t.Fatalf("writeConf: %v", err)
		}
		got, gotRules, err = loadConf(path)
		if err != nil {
			t.Fatalf("loadConf: %v", err)
		}
		if got != enabled {
			t.Errorf("enabled: got %v want %v", got, enabled)
		}
		if !reflect.DeepEqual(gotRules, rules) {
			t.Errorf("rules round trip:\n got %+v\nwant %+v", gotRules, rules)
		}
		// render must be stable: parse -> render -> parse gives the same result
		text2 := renderConf(enabled, gotRules)
		if text2 != text {
			t.Errorf("conf not stable under round trip:\n---\n%s\n---\n%s", text2, text)
		}
	}
}

func TestEmitDecodeRoundTrip(t *testing.T) {
	rules := []lrule{
		{proto: "tcp", port: 22, action: "allow"},
		{proto: "udp", port: 53, action: "reject"},
		{proto: "tcp", port: 25, action: "reject"},
		{proto: "both", port: 8080, action: "allow", from: "192.168.1.5"},
		{proto: "both", port: 443, action: "deny", iface: "eth0"},
		{proto: "both", port: 25, action: "reject"},
		{proto: "tcp", port: 8081, action: "allow", from: "2001:db8::1"},
	}
	var exprs []*nftables.Rule
	for _, r := range rules {
		for _, ex := range emitRule(r) {
			exprs = append(exprs, &nftables.Rule{Exprs: ex})
		}
	}
	got := kernelRulesToLrules(exprs)
	if !reflect.DeepEqual(got, rules) {
		t.Errorf("emit/decode round trip:\n got %+v\nwant %+v", got, rules)
	}
}

func TestKernelRulesToLrulesSkipsGuards(t *testing.T) {
	exprs := []*nftables.Rule{
		{Exprs: ctEstablishedAccept()},
		{Exprs: loAccept()},
		{Exprs: emitRule(lrule{proto: "tcp", port: 22, action: "allow"})[0]},
	}
	got := kernelRulesToLrules(exprs)
	want := []lrule{{proto: "tcp", port: 22, action: "allow"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("guards must be dropped: got %+v want %+v", got, want)
	}
}

func TestConfForeign(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c")
	os.WriteFile(p, []byte("# comment only\n"), 0o644)
	if confForeign(p) {
		t.Error("comment-only file must not be foreign")
	}
	os.WriteFile(p, []byte("table inet filter { }\n"), 0o644)
	if !confForeign(p) {
		t.Error("unmarked content must be foreign")
	}
	if err := writeConf(p, true, nil); err != nil {
		t.Fatal(err)
	}
	if confForeign(p) {
		t.Error("gopit-marked file must not be foreign")
	}
}

func TestTogglePreservesRules(t *testing.T) {
	rules := []lrule{{proto: "both", port: 8080, action: "allow", from: "192.168.1.5", iface: "eth0"}}
	path := filepath.Join(t.TempDir(), "nftables.conf")
	if err := writeConf(path, true, rules); err != nil {
		t.Fatal(err)
	}
	enabled, got, err := loadConf(path)
	if err != nil || !enabled {
		t.Fatalf("load enabled: %v %v", enabled, err)
	}
	if err := writeConf(path, false, got); err != nil {
		t.Fatal(err)
	}
	enabled, got2, err := loadConf(path)
	if err != nil || enabled || !reflect.DeepEqual(got2, got) {
		t.Fatalf("disable must preserve rules: enabled=%v got=%+v want=%+v err=%v", enabled, got2, got, err)
	}
}

func TestRenderConfValidNftSyntax(t *testing.T) {
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("nft binary not installed")
	}
	if os.Geteuid() != 0 {
		t.Skip("nft -c needs root (netlink cache init)")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "c")
	rules := []lrule{
		{proto: "both", port: 8080, action: "allow", from: "192.168.1.5", iface: "eth0"},
		{proto: "udp", port: 53, action: "reject"},
		{proto: "tcp", port: 25, action: "reject"},
		{proto: "tcp", port: 22, action: "allow"},
	}
	if err := writeConf(path, true, rules); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("nft", "-c", "-f", path).CombinedOutput(); err != nil {
		t.Fatalf("nft -c rejected rendered conf: %v\n%s", err, out)
	}
}
