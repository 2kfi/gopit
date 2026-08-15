//go:build linux

package nftfw

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/nftables"
)

// TestAPIUnavailableWithoutCapNetAdmin proves the EPERM degradation path:
// a non-root agent (no CAP_NET_ADMIN) gets a clean "firewall unavailable"
// error, never a crash.
func TestAPIUnavailableWithoutCapNetAdmin(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs non-root to exercise the EPERM path")
	}
	a := New(false)
	a.confPath = filepath.Join(t.TempDir(), "nftables.conf")
	_, err := a.Status()
	if err == nil || !strings.Contains(err.Error(), "firewall unavailable") {
		t.Fatalf("expected clean firewall-unavailable error, got %v", err)
	}
}

// newTestAPI builds an API rooted at a temp conf, skipping when the host
// kernel state cannot be managed (no root/CAP_NET_ADMIN) or a live gopit
// table is already in use.
func newTestAPI(t *testing.T) *API {
	t.Helper()
	c, err := nftables.New()
	if err != nil {
		t.Skipf("netlink unavailable: %v", err)
	}
	tables, err := c.ListTables()
	if err != nil {
		t.Skipf("no CAP_NET_ADMIN: %v", err)
	}
	if hasTable(tables, tableName) {
		t.Skip("live gopit kernel table present; refusing to clobber it")
	}
	a := New(true)
	a.confPath = filepath.Join(t.TempDir(), "nftables.conf")
	t.Cleanup(func() {
		c2, err := nftables.New()
		if err == nil {
			c2.DelTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: tableName})
			c2.Flush()
		}
	})
	return a
}

// TestAPICycleAndBootRecovery runs the full root-only cycle against the real
// kernel: apply, add, delete, toggle, and recovery from the persisted conf
// when the kernel table vanishes.
func TestAPICycleAndBootRecovery(t *testing.T) {
	a := newTestAPI(t)

	if err := a.toggle(false); err != nil { // disabled, empty table
		t.Fatalf("toggle(false): %v", err)
	}
	st, err := a.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Enabled || len(st.Rules) != 0 {
		t.Fatalf("fresh state: %+v", st)
	}
	if err := a.addRule(RuleAddReq{Protocol: "tcp", Port: 22, Action: "allow"}); err != nil {
		t.Fatalf("add 22: %v", err)
	}
	if err := a.addRule(RuleAddReq{Protocol: "both", Port: 8080, Action: "reject", From: "192.168.1.5"}); err != nil {
		t.Fatalf("add 8080: %v", err)
	}
	st, _ = a.Status()
	if len(st.Rules) != 2 || st.Rules[0].To != "22/tcp" || st.Rules[1].To != "8080" || st.Rules[1].Action != "REJECT" {
		t.Fatalf("rules after adds: %+v", st.Rules)
	}
	if err := a.deleteRule(1); err != nil {
		t.Fatalf("delete 1: %v", err)
	}
	st, _ = a.Status()
	if len(st.Rules) != 1 || st.Rules[0].To != "8080" {
		t.Fatalf("rules after delete: %+v", st.Rules)
	}
	if err := a.toggle(true); err != nil {
		t.Fatalf("toggle(true): %v", err)
	}
	if st, _ := a.Status(); !st.Enabled {
		t.Fatal("toggle(true) must flip the base policy to drop")
	}

	// boot recovery: the kernel table vanishes (nft flush), the agent must
	// rebuild it from the persisted conf and report the same state.
	c, err := nftables.New()
	if err != nil {
		t.Fatal(err)
	}
	c.DelTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: tableName})
	if err := c.Flush(); err != nil {
		t.Fatal(err)
	}
	st, err = a.Status()
	if err != nil {
		t.Fatalf("recovered status: %v", err)
	}
	if !st.Enabled || len(st.Rules) != 1 || st.Rules[0].To != "8080" {
		t.Fatalf("recovered state: %+v", st)
	}

	// boot recovery must also persist back: the conf file now carries the
	// recovered state (gopit marker round trip).
	b, err := os.ReadFile(a.confPath)
	if err != nil {
		t.Fatalf("conf after recovery: %v", err)
	}
	if !strings.Contains(string(b), confMarker) || !strings.Contains(string(b), "8080") {
		t.Fatalf("persisted conf missing recovered rules:\n%s", b)
	}
}

// TestConflictScanRejectsShadowingChains proves the foreign-chain guard: a
// pre-existing input chain with rules outside the gopit table must refuse to
// manage the firewall.
func TestConflictScanRejectsShadowingChains(t *testing.T) {
	a := newTestAPI(t)
	c, err := nftables.New()
	if err != nil {
		t.Skip(err)
	}
	foreign := &nftables.Table{Family: nftables.TableFamilyIPv4, Name: "gopit-test-foreign"}
	c.AddTable(foreign)
	policy := nftables.ChainPolicyAccept
	c.AddChain(&nftables.Chain{Table: foreign, Name: "input", Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookInput, Priority: nftables.ChainPriorityFilter, Policy: &policy})
	c.AddRule(&nftables.Rule{Table: foreign, Chain: &nftables.Chain{Name: "input", Table: foreign}, Exprs: ctEstablishedAccept()})
	if err := c.Flush(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		c2, _ := nftables.New()
		c2.DelTable(foreign)
		c2.Flush()
	}()

	_, err = a.Status()
	if err == nil || !strings.Contains(err.Error(), "would shadow") {
		t.Fatalf("conflict scan must refuse shadowing chains, got %v", err)
	}
}
