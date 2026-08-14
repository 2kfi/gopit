// Package nftfw manages the host firewall directly via the kernel's nftables
// subsystem (netlink). No ufw, no iptables, no sudo: the agent only needs
// CAP_NET_ADMIN (set by its systemd unit).
//
// Table layout (family inet, dual-stack):
//
//	gopit/input       base chain (hook input, priority filter): policy
//	                  accept|drop, exactly one rule when enabled: jump gopit-input
//	gopit/gopit-input managed rules: ct state established,related accept,
//	                  iifname "lo" accept, then one rule per logical rule
//
// The kernel is the runtime source of truth: status, numbering and the
// enabled flag are read back from netfilter (like ufw does). /etc/nftables.conf
// (see conf.go) is the boot truth, loaded by the nftables service and
// re-applied by the agent when the table is missing at startup. gopit owns
// both exclusively.
package nftfw

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

const (
	tableName  = "gopit"
	inputChain = "input"       // base chain: hook input
	rulesChain = "gopit-input" // managed rules, jumped from input
	confPath   = "/etc/nftables.conf"

	protoTCP = unix.IPPROTO_TCP
	protoUDP = unix.IPPROTO_UDP

	// nft reject types (linux/netfilter/nf_tables.h): ICMPX_UNREACH in an
	// inet table lets the kernel pick the right icmp/icmp6 code per family;
	// TCP rules use tcp reset instead (ufw-compatible behavior).
	rejectTCPReset     = 1 // NFT_REJECT_TCP_RST
	rejectIcmpXUnreach = 2 // NFT_REJECT_ICMPX_UNREACH (type)
	rejectIcmpXPortUnr = 1 // NFT_REJECT_ICMPX_PORT_UNREACH (code)
)

// Rule is one logical firewall rule (wire contract with the server; action is
// uppercase ALLOW|DENY|REJECT, matching the ufw listing UX).
type Rule struct {
	Number    int    `json:"number"`
	To        string `json:"to"`
	Action    string `json:"action"`
	From      string `json:"from"`
	Direction string `json:"direction"`
	Interface string `json:"interface,omitempty"`
}

// Status is the firewall state (wire contract with the server).
type Status struct {
	Enabled    bool   `json:"enabled"`
	DefaultIn  string `json:"default_in"`
	DefaultOut string `json:"default_out"`
	Rules      []Rule `json:"rules"`
}

// lrule is the internal logical form of a rule.
type lrule struct {
	proto  string // tcp | udp | both
	port   int
	action string // allow | deny | reject
	from   string // "" = anywhere
	iface  string
}

// API manages the firewall for the agent's ufw.* WS methods.
type API struct {
	allowToggle bool
	confPath    string // test override

	// all netlink use goes through one conn under mu (the library's Conn is
	// not safe for concurrent round trips).
	mu   sync.Mutex
	conn *nftables.Conn
}

// New builds the API. Construction never fails: availability is probed on the
// first call so a missing capability degrades to a clean error, never a crash.
func New(allowToggle bool) *API {
	return &API{allowToggle: allowToggle, confPath: confPath}
}

// Request payloads (keys are the wire contract with the server).
type RuleAddReq struct {
	Protocol  string `json:"protocol"` // tcp | udp | both
	Port      int    `json:"port"`
	Action    string `json:"action"` // allow | deny | reject
	From      string `json:"from"`   // default "any"
	Interface string `json:"interface"`
}

type RuleDeleteReq struct {
	Number int `json:"number"` // 1-based rule number as shown by Status
}

type ToggleReq struct {
	Enabled bool `json:"enabled"`
}

// Call executes one firewall method with the raw request payload.
func (a *API) Call(method string, payload json.RawMessage) (any, error) {
	switch method {
	case "ufw.status":
		return a.Status()
	case "ufw.rule.add":
		var req RuleAddReq
		if err := json.Unmarshal(payload, &req); err != nil ||
			req.Port < 1 || req.Port > 65535 || !oneOf(req.Protocol, "tcp", "udp", "both") || !oneOf(req.Action, "allow", "deny", "reject") {
			return nil, errors.New("protocol (tcp|udp|both), port (1-65535) and action (allow|deny|reject) required")
		}
		return map[string]string{"status": "ok"}, a.addRule(req)
	case "ufw.rule.delete":
		var req RuleDeleteReq
		if err := json.Unmarshal(payload, &req); err != nil || req.Number < 1 {
			return nil, errors.New("number required")
		}
		return map[string]string{"status": "ok"}, a.deleteRule(req.Number)
	case "ufw.toggle":
		if !a.allowToggle {
			return nil, errors.New("toggle disabled by config")
		}
		var req ToggleReq
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, errors.New("enabled required")
		}
		return map[string]string{"status": "ok"}, a.toggle(req.Enabled)
	case "ufw.rule.preview":
		var req RuleAddReq
		if err := json.Unmarshal(payload, &req); err != nil ||
			req.Port < 1 || req.Port > 65535 || !oneOf(req.Protocol, "tcp", "udp", "both") || !oneOf(req.Action, "allow", "deny", "reject") {
			return nil, errors.New("protocol (tcp|udp|both), port (1-65535) and action (allow|deny|reject) required")
		}
		return a.previewAdd(req)
	default:
		return nil, fmt.Errorf("unknown firewall method: %s", method)
	}
}

// Status returns the firewall state: enabled flag from the base policy and
// the rule list reconstructed from the managed chain.
func (a *API) Status() (*Status, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, err := a.connLocked()
	if err != nil {
		return nil, err
	}
	if err := a.ensureInitialized(c); err != nil {
		return nil, err
	}
	enabled, lrs, err := a.logicalState(c)
	if err != nil {
		return nil, err
	}
	return a.render(enabled, lrs), nil
}

// render maps the enabled flag + logical rules onto the wire Status.
func (a *API) render(enabled bool, lrs []lrule) *Status {
	st := &Status{DefaultOut: "allow"}
	if enabled {
		st.Enabled, st.DefaultIn = true, "deny"
	} else {
		st.DefaultIn = "allow"
	}
	for i, r := range lrs {
		from := r.from
		if from == "" {
			from = "Anywhere"
		}
		st.Rules = append(st.Rules, Rule{
			Number:    i + 1,
			To:        portDisplay(r.proto, r.port),
			Action:    strings.ToUpper(r.action),
			From:      from,
			Direction: "IN",
			Interface: r.iface,
		})
	}
	return st
}

// previewAdd returns the Status as it would look after adding the rule,
// without touching the kernel: it reads the live state, appends the
// simulated rule and renders.
func (a *API) previewAdd(req RuleAddReq) (*Status, error) {
	from := strings.TrimSpace(req.From)
	if from == "any" || from == "Anywhere" {
		from = ""
	}
	if from != "" && net.ParseIP(from) == nil {
		return nil, errors.New("invalid 'from' address")
	}
	iface := strings.TrimSpace(req.Interface)
	if iface != "" && (len(iface) > 15 || strings.ContainsAny(iface, `"\\ `+"\t")) {
		return nil, errors.New("invalid 'interface' name")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	c, err := a.connLocked()
	if err != nil {
		return nil, err
	}
	if err := a.ensureInitialized(c); err != nil {
		return nil, err
	}
	enabled, rules, err := a.logicalState(c)
	if err != nil {
		return nil, err
	}
	rules = append(rules, lrule{proto: req.Protocol, port: req.Port, action: req.Action, from: from, iface: iface})
	return a.render(enabled, rules), nil
}

func (a *API) addRule(req RuleAddReq) error {
	from := strings.TrimSpace(req.From)
	if from == "any" || from == "Anywhere" {
		from = ""
	}
	if from != "" && net.ParseIP(from) == nil {
		return errors.New("invalid 'from' address")
	}
	iface := strings.TrimSpace(req.Interface)
	if iface != "" && (len(iface) > 15 || strings.ContainsAny(iface, `"\\ `+"\t")) {
		// kernel ifname is IFNAMSIZ (16) null-padded; quotes/backslash break the conf round trip
		return errors.New("invalid 'interface' name")
	}
	return a.mutate(func(enabled bool, rules []lrule) (bool, []lrule, error) {
		return enabled, append(rules, lrule{proto: req.Protocol, port: req.Port, action: req.Action, from: from, iface: iface}), nil
	})
}

func (a *API) deleteRule(number int) error {
	return a.mutate(func(enabled bool, rules []lrule) (bool, []lrule, error) {
		if number < 1 || number > len(rules) {
			return false, nil, errors.New("rule number out of range")
		}
		return enabled, append(rules[:number-1], rules[number:]...), nil
	})
}

func (a *API) toggle(enabled bool) error {
	return a.mutate(func(_ bool, rules []lrule) (bool, []lrule, error) {
		return enabled, rules, nil
	})
}

// mutate applies a change to the logical rule list: re-reads the kernel
// state, checks for conflicting foreign rules, rebuilds the table and
// persists the boot config. Serialized by mu.
func (a *API) mutate(f func(enabled bool, rules []lrule) (bool, []lrule, error)) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, err := a.connLocked()
	if err != nil {
		return err
	}
	if err := a.ensureInitialized(c); err != nil {
		return err
	}
	if err := a.conflictScan(c); err != nil {
		return err
	}
	enabled, rules, err := a.logicalState(c)
	if err != nil {
		return err
	}
	enabled, rules, err = f(enabled, rules)
	if err != nil {
		return err
	}
	if err := a.apply(c, enabled, rules); err != nil {
		return err
	}
	a.persistConf(enabled, rules)
	return nil
}

// connLocked returns the netlink connection, creating it on first use.
// Callers hold a.mu.
func (a *API) connLocked() (*nftables.Conn, error) {
	if a.conn == nil {
		c, err := nftables.New()
		if err != nil {
			return nil, fwUnavail(err)
		}
		a.conn = c
	}
	return a.conn, nil
}

// ensureInitialized makes sure our table exists. Missing table = recovered
// from the persisted boot config, or a fresh install (inactive, no rules).
// Run on every call: the kernel is the source of truth, and a vanished table
// (`nft flush ruleset`, service reload) is recovered instead of erroring.
// Callers hold a.mu.
func (a *API) ensureInitialized(c *nftables.Conn) error {
	tables, err := c.ListTables()
	if err != nil {
		return fwUnavail(err)
	}
	if hasTable(tables, tableName) {
		return nil
	}
	enabled, rules, err := loadConf(a.confPath)
	if err != nil {
		return err
	}
	if err := a.conflictScan(c); err != nil {
		return err
	}
	if err := a.apply(c, enabled, rules); err != nil {
		return err
	}
	if confForeign(a.confPath) {
		// not persisted: the file pre-exists without our marker and will
		// come back at boot without the gopit table — runtime only.
		errLogf(nil, "not persisting firewall: %s is not gopit-managed", a.confPath)
	} else {
		a.persistConf(enabled, rules)
	}
	return nil
}

// conflictScan refuses to manage when other input chains (outside our table)
// could shadow our rules — any rules or a non-accept policy in a foreign
// input base chain at filter priority or above. Empty/accept-policy foreign
// chains are inert and tolerated. Callers hold a.mu.
func (a *API) conflictScan(c *nftables.Conn) error {
	chains, err := c.ListChains()
	if err != nil {
		return fwUnavail(err)
	}
	var foreign []string
	for _, ch := range chains {
		if ch.Table == nil || (ch.Table.Name == tableName && ch.Table.Family == nftables.TableFamilyINet) ||
			ch.Hooknum == nil || *ch.Hooknum != *nftables.ChainHookInput ||
			ch.Priority == nil || *ch.Priority > *nftables.ChainPriorityFilter {
			continue
		}
		rules, err := c.GetRules(ch.Table, ch)
		if err != nil {
			return fwUnavail(err)
		}
		shadow := len(rules) > 0 || (ch.Policy != nil && *ch.Policy == nftables.ChainPolicyDrop)
		if shadow {
			foreign = append(foreign, fmt.Sprintf("%s/%s/%s (%d rules, policy %s)",
				familyName(ch.Table.Family), ch.Table.Name, ch.Name, len(rules), policyName(ch.Policy)))
		}
	}
	if len(foreign) > 0 {
		return fmt.Errorf("cannot manage firewall: existing rules would shadow gopit: %s. Remove or migrate them (e.g. disable ufw) first",
			strings.Join(foreign, ", "))
	}
	return nil
}

// logicalState reads the enabled flag and logical rules back from the kernel.
// Callers hold a.mu.
func (a *API) logicalState(c *nftables.Conn) (bool, []lrule, error) {
	t := &nftables.Table{Family: nftables.TableFamilyINet, Name: tableName}
	base, err := c.ListChain(t, inputChain)
	if err != nil {
		return false, nil, fwUnavail(err)
	}
	rules, err := c.GetRules(t, &nftables.Chain{Name: rulesChain, Table: t})
	if err != nil {
		return false, nil, fwUnavail(err)
	}
	return base.Policy != nil && *base.Policy == nftables.ChainPolicyDrop, kernelRulesToLrules(rules), nil
}

// apply rebuilds the whole gopit table in one netlink batch (atomic): the
// base chain with the current policy, the managed chain, the guard rules and
// one rule per logical rule. Idempotent; safe to run at any time.
func (a *API) apply(c *nftables.Conn, enabled bool, rules []lrule) error {
	t := &nftables.Table{Family: nftables.TableFamilyINet, Name: tableName}
	tables, err := c.ListTables()
	if err != nil {
		return fwUnavail(err)
	}
	if hasTable(tables, tableName) {
		c.DelTable(t)
	}
	c.AddTable(t)
	policy := nftables.ChainPolicyAccept
	if enabled {
		policy = nftables.ChainPolicyDrop
	}
	c.AddChain(&nftables.Chain{Table: t, Name: inputChain, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookInput, Priority: nftables.ChainPriorityFilter,
		Policy: &policy,
	})
	c.AddChain(&nftables.Chain{Table: t, Name: rulesChain})
	if enabled {
		c.AddRule(&nftables.Rule{Table: t, Chain: &nftables.Chain{Name: inputChain, Table: t}, Exprs: []expr.Any{
			&expr.Verdict{Kind: expr.VerdictJump, Chain: rulesChain},
		}})
	}
	// The rules chain is populated regardless of 'enabled' so rules survive a
	// disable/enable cycle (only the base policy and jump toggle).
	tc := &nftables.Chain{Name: rulesChain, Table: t}
	c.AddRule(&nftables.Rule{Table: t, Chain: tc, Exprs: ctEstablishedAccept()})
	c.AddRule(&nftables.Rule{Table: t, Chain: tc, Exprs: loAccept()})
	for _, r := range rules {
		for _, ex := range emitRule(r) {
			c.AddRule(&nftables.Rule{Table: t, Chain: tc, Exprs: ex})
		}
	}
	if err := c.Flush(); err != nil {
		return fwUnavail(err)
	}
	return nil
}

// persistConf writes the boot config (log-only on failure: the live kernel
// state is already applied and a failed write is surfaced via logs).
func (a *API) persistConf(enabled bool, rules []lrule) {
	if confForeign(a.confPath) {
		errLogf(nil, "not persisting firewall: %s is not gopit-managed", a.confPath)
		return
	}
	if err := writeConf(a.confPath, enabled, rules); err != nil {
		errLogf(err, "persisting firewall config to %s", a.confPath)
	}
}

// --- rule encoding ---------------------------------------------------------

// ctEstablishedAccept is `ct state established,related accept`: without it a
// drop default policy would kill every established connection.
func ctEstablishedAccept() []expr.Any {
	return []expr.Any{
		&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
		&expr.Bitwise{
			SourceRegister: 1, DestRegister: 1, Len: 4,
			Mask: binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED),
			Xor:  binaryutil.NativeEndian.PutUint32(0),
		},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0, 0, 0, 0}},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}
}

// loAccept is `iifname "lo" accept`: loopback must not depend on configured
// rules.
func loAccept() []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ifnameData("lo")},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}
}

// emitRule expands one logical rule into kernel rule expression lists: one
// per protocol (tcp+udp for "both"), with an saddr match only when a source
// is set (the rule then applies to that address family only).
func emitRule(r lrule) [][]expr.Any {
	var out [][]expr.Any
	for _, p := range protosOf(r.proto) {
		var exs []expr.Any
		if r.iface != "" {
			exs = append(exs,
				&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ifnameData(r.iface)})
		}
		if r.from != "" {
			ip := net.ParseIP(r.from)
			if ip4 := ip.To4(); ip4 != nil {
				exs = append(exs,
					&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4},
					&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ip4})
			} else {
				exs = append(exs,
					&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 8, Len: 16},
					&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ip.To16()})
			}
		}
		exs = append(exs,
			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{byte(p)}},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(uint16(r.port))})
		switch r.action {
		case "allow":
			exs = append(exs, &expr.Verdict{Kind: expr.VerdictAccept})
		case "deny":
			exs = append(exs, &expr.Verdict{Kind: expr.VerdictDrop})
		default: // reject
			if p == protoTCP {
				exs = append(exs, &expr.Reject{Type: rejectTCPReset})
			} else {
				exs = append(exs, &expr.Reject{Type: rejectIcmpXUnreach, Code: rejectIcmpXPortUnr})
			}
		}
		out = append(out, exs)
	}
	return out
}

// --- rule decoding ---------------------------------------------------------

// kernelRule is one decoded rule of gopit-input.
type kernelRule struct {
	iface    string
	from     net.IP
	proto    uint8 // 0 = no protocol match (only the guard rules)
	port     uint16
	verdict  string // accept | drop | reject
	tcpReset bool
}

// decodeKernelRule reads the expressions of one kernel rule back into a
// kernelRule; ctGuard reports the established/related guard rule (skipped by
// callers); ok is false for anything we never emit (hand-edited rules).
func decodeKernelRule(exs []expr.Any) (kr kernelRule, ctGuard, ok bool) {
	i := 0
	next := func() expr.Any {
		i++
		if i < len(exs) {
			return exs[i]
		}
		return nil
	}
	for i < len(exs) {
		switch e := exs[i].(type) {
		case *expr.Meta:
			switch e.Key {
			case expr.MetaKeyIIFNAME:
				cmp, cok := next().(*expr.Cmp)
				if !cok || cmp.Op != expr.CmpOpEq {
					return kr, false, false
				}
				kr.iface = trimZero(cmp.Data)
			case expr.MetaKeyL4PROTO:
				cmp, cok := next().(*expr.Cmp)
				if !cok || len(cmp.Data) != 1 {
					return kr, false, false
				}
				kr.proto = cmp.Data[0]
			default:
				return kr, false, false
			}
		case *expr.Payload:
			var cmp *expr.Cmp
			switch {
			case e.Base == expr.PayloadBaseNetworkHeader && e.Offset == 12 && e.Len == 4: // ip saddr
				cmp, _ = next().(*expr.Cmp)
				if cmp == nil || len(cmp.Data) != 4 {
					return kr, false, false
				}
				kr.from = net.IP(cmp.Data)
			case e.Base == expr.PayloadBaseNetworkHeader && e.Offset == 8 && e.Len == 16: // ip6 saddr
				cmp, _ = next().(*expr.Cmp)
				if cmp == nil || len(cmp.Data) != 16 {
					return kr, false, false
				}
				kr.from = net.IP(cmp.Data)
			case e.Base == expr.PayloadBaseTransportHeader && e.Offset == 2 && e.Len == 2: // dport
				cmp, _ = next().(*expr.Cmp)
				if cmp == nil || len(cmp.Data) != 2 {
					return kr, false, false
				}
				kr.port = binary.BigEndian.Uint16(cmp.Data)
			default:
				return kr, false, false
			}
		case *expr.Ct:
			return kr, true, true // the ct state guard rule
		case *expr.Cmp, *expr.Bitwise:
			return kr, false, false // unexpected shape
		case *expr.Verdict:
			switch e.Kind {
			case expr.VerdictAccept:
				kr.verdict = "accept"
			case expr.VerdictDrop:
				kr.verdict = "drop"
			default:
				return kr, false, false
			}
		case *expr.Reject:
			kr.verdict = "reject"
			kr.tcpReset = e.Type == rejectTCPReset
		case *expr.Immediate:
			return kr, false, false
		default:
			return kr, false, false
		}
		i++
	}
	return kr, false, kr.verdict != ""
}

// kernelRulesToLrules decodes and groups the gopit-input chain back into
// logical rules: adjacent tcp+udp twins (same port/from/iface/verdict) merge
// into one "both" rule; the guard rules are dropped.
func kernelRulesToLrules(rules []*nftables.Rule) []lrule {
	var out []lrule
	for i := 0; i < len(rules); {
		kr, ctGuard, ok := decodeKernelRule(rules[i].Exprs)
		if !ok || ctGuard {
			i++
			continue
		}
		// the lo guard rule has no port and no verdict payload besides accept
		if kr.iface == "lo" && kr.port == 0 && kr.verdict == "accept" {
			i++
			continue
		}
		proto := "tcp"
		if kr.proto == protoUDP {
			proto = "udp"
		}
		if i+1 < len(rules) {
			nx, nxGuard, nxok := decodeKernelRule(rules[i+1].Exprs)
			if nxok && !nxGuard && !(nx.iface == "lo" && nx.port == 0) &&
				kr.proto == protoTCP && nx.proto == protoUDP && sameKey(kr, nx) {
				proto = "both"
				i++ // consume the udp twin
			}
		}
		action := strings.ToLower(kr.verdict)
		switch action {
		case "accept":
			action = "allow"
		case "drop":
			action = "deny"
		}
		from := ""
		if kr.from != nil {
			from = kr.from.String()
		}
		out = append(out, lrule{proto: proto, port: int(kr.port), action: action, from: from, iface: string(kr.iface)})
		i++
	}
	return out
}

func sameKey(a, b kernelRule) bool {
	return a.iface == b.iface && a.from.Equal(b.from) && a.port == b.port && a.verdict == b.verdict
}

// --- helpers ---------------------------------------------------------------

func protosOf(proto string) []uint8 {
	switch proto {
	case "udp":
		return []uint8{protoUDP}
	case "both":
		return []uint8{protoTCP, protoUDP}
	default:
		return []uint8{protoTCP}
	}
}

func portDisplay(proto string, port int) string {
	if proto == "both" {
		return strconv.Itoa(port)
	}
	return strconv.Itoa(port) + "/" + proto
}

func ifnameData(name string) []byte {
	b := make([]byte, 16) // IFNAMSIZ, null-padded
	copy(b, name)
	return b
}

func trimZero(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func hasTable(tables []*nftables.Table, name string) bool {
	for _, t := range tables {
		if t.Name == name && t.Family == nftables.TableFamilyINet {
			return true
		}
	}
	return false
}

func familyName(f nftables.TableFamily) string {
	switch f {
	case nftables.TableFamilyINet:
		return "inet"
	case nftables.TableFamilyIPv4:
		return "ip"
	case nftables.TableFamilyIPv6:
		return "ip6"
	case nftables.TableFamilyBridge:
		return "bridge"
	case nftables.TableFamilyNetdev:
		return "netdev"
	default:
		return "family"
	}
}

func policyName(p *nftables.ChainPolicy) string {
	if p != nil && *p == nftables.ChainPolicyDrop {
		return "drop"
	}
	return "accept"
}

// fwUnavail maps low-level netlink failures onto operator-facing messages.
func fwUnavail(err error) error {
	if errors.Is(err, unix.EPERM) {
		return errors.New("firewall unavailable: operation not permitted — the agent needs CAP_NET_ADMIN (set systemd AmbientCapabilities=CAP_NET_ADMIN on gopitd.service)")
	}
	return fmt.Errorf("firewall unavailable: %w", err)
}

func oneOf(s string, opts ...string) bool {
	for _, o := range opts {
		if s == o {
			return true
		}
	}
	return false
}
