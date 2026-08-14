package ufw

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCallValidationRejectsBadPayloads proves every malformed or unsafe
// request payload fails before any sudo/ufw invocation happens.
func TestCallValidationRejectsBadPayloads(t *testing.T) {
	a := New("/definitely/not/ufw", true) // never executed on validation failure
	cases := []json.RawMessage{
		json.RawMessage(`{"protocol":"tcp","port":0,"action":"allow"}`),
		json.RawMessage(`{"protocol":"tcp","port":70000,"action":"allow"}`),
		json.RawMessage(`{"protocol":"icmp","port":22,"action":"allow"}`),
		json.RawMessage(`{"protocol":"tcp","port":22,"action":"punch"}`),
		json.RawMessage(`{"protocol":"tcp","port":22,"action":"allow","from":"999.1.1.1"}`),
		json.RawMessage(`{"protocol":"tcp","port":22,"action":"allow","interface":"this-ifname-is-way-too-long"}`),
		json.RawMessage(`{"protocol":"tcp","port":22,"action":"allow","interface":"eth 0"}`),
		json.RawMessage(`{"protocol":"tcp","port":22,"action":"allow","interface":"-evil"}`), // must not parse as a ufw flag
		json.RawMessage(`{"protocol":"tcp","port":22,"action":"allow","from":"-x"}`),         // leading dash must be rejected
	}
	for _, c := range cases {
		if _, err := a.Call("ufw.rule.add", c); err == nil {
			t.Fatalf("payload %s must be rejected", c)
		}
	}
	if _, err := a.Call("ufw.rule.delete", json.RawMessage(`{"number":0}`)); err == nil {
		t.Fatal("delete rule 0 must be rejected")
	}
	if _, err := a.Call("ufw.rule.preview", json.RawMessage(`{"protocol":"tcp","port":70000,"action":"allow"}`)); err == nil {
		t.Fatal("preview with an invalid port must be rejected")
	}
}

func TestToggleDisabledByConfig(t *testing.T) {
	a := New("/definitely/not/ufw", false)
	if _, err := a.Call("ufw.toggle", json.RawMessage(`{"enabled":true}`)); err == nil {
		t.Fatal("toggle must be refused when allow_toggle is off")
	}
}

// TestRuleMutationsAreSerialized drives add/delete through a fake sudo + ufw
// pair and verifies the API's mutex keeps every invocation atomic and
// complete. The global lock is the TOCTOU defense: ufw delete-by-number is
// only safe when no other mutation can interleave, so concurrent calls must
// never produce an interleaved command line.
func TestRuleMutationsAreSerialized(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "ufw.log")

	// sudo -n <bin> <args...> -> exec the bin directly
	sudo := filepath.Join(dir, "sudo")
	os.WriteFile(sudo, []byte("#!/bin/sh\nbin=\"$2\"\nshift 2\nexec \"$bin\" \"$@\"\n"), 0o755)
	// ufw logs its full command line atomically (one line per invocation)
	ufw := filepath.Join(dir, "ufw")
	os.WriteFile(ufw, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+logPath+"\n"), 0o755)

	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	a := New(ufw, true)

	const n = 10
	done := make(chan error, 2*n)
	for i := 0; i < n; i++ {
		go func(i int) {
			_, err := a.Call("ufw.rule.add", json.RawMessage(`{"protocol":"tcp","port":8000,"action":"allow"}`))
			done <- err
		}(i)
		go func(i int) {
			_, err := a.Call("ufw.rule.delete", json.RawMessage(`{"number":1}`))
			done <- err
		}(i)
	}
	for i := 0; i < 2*n; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent mutation failed: %v", err)
		}
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2*n {
		t.Fatalf("expected %d atomic ufw invocations, got %d", 2*n, len(lines))
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "allow ") && !strings.HasPrefix(l, "--force delete 1") {
			t.Fatalf("corrupted ufw invocation: %q", l)
		}
	}
}

// TestPreviewSimulatesWithoutMutating drives ufw.rule.preview through fake
// sudo + ufw: the new rule is simulated on top of the current status and no
// mutation command (allow/deny/reject/delete/toggle) is ever invoked.
func TestPreviewSimulatesWithoutMutating(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "ufw.log")
	sudo := filepath.Join(dir, "sudo")
	os.WriteFile(sudo, []byte("#!/bin/sh\nbin=\"$2\"\nshift 2\nexec \"$bin\" \"$@\"\n"), 0o755)
	ufw := filepath.Join(dir, "ufw")
	os.WriteFile(ufw, []byte("#!/bin/sh\ncase \"$*\" in\n  \"status verbose\") printf 'Status: active\\nDefault: deny (incoming), allow (outgoing)\\n' ;;\n  \"status numbered\") printf '[ 1] 22/tcp ALLOW IN Anywhere\\n' ;;\n  *) printf '%s\\n' \"$*\" >> "+logPath+" ;;\nesac\n"), 0o755)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	st, err := New(ufw, true).Call("ufw.rule.preview", json.RawMessage(`{"protocol":"tcp","port":8080,"action":"allow"}`))
	if err != nil {
		t.Fatalf("preview failed: %v", err)
	}
	rules := st.(*Status).Rules
	if len(rules) != 2 || rules[1].To != "8080/tcp" || rules[1].Action != "ALLOW" || rules[1].From != "Anywhere" {
		t.Fatalf("preview must append the simulated rule, got %+v", rules)
	}
	if b, _ := os.ReadFile(logPath); len(b) != 0 {
		t.Fatalf("preview must not run mutations, invoked: %s", b)
	}
}

// TestStatusViaRealSudo goes through the actual sudoers path (NOPASSWD or
// skip). It only runs where the host grants passwordless sudo + ufw.
func TestStatusViaRealSudo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root skips sudo")
	}
	bin, err := exec.LookPath("ufw")
	if err != nil {
		t.Skip("ufw not installed")
	}
	st, err := New(bin, false).Status()
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "password is required") || strings.Contains(msg, "command not found") || strings.Contains(msg, "not available") {
			t.Skipf("passwordless sudo/ufw unavailable: %s", msg)
		}
		t.Fatalf("ufw status: %v", err)
	}
	if st == nil {
		t.Fatal("status must not be nil")
	}
}
