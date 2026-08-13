// Package ufw provides the agent's ufw firewall integration.
package ufw

import (
	"regexp"
	"strconv"
	"strings"
)

// Rule is one entry of `ufw status numbered`.
type Rule struct {
	Number    int    `json:"number"`
	To        string `json:"to"`
	Action    string `json:"action"` // ALLOW | DENY | REJECT | LIMIT
	From      string `json:"from"`
	Direction string `json:"direction"` // IN | OUT
	Interface string `json:"interface,omitempty"`
}

// Status is the parsed result of `ufw status verbose` + `ufw status numbered`.
type Status struct {
	Enabled    bool   `json:"enabled"`
	DefaultIn  string `json:"default_in"` // allow | deny | reject | limit
	DefaultOut string `json:"default_out"`
	Rules      []Rule `json:"rules"`
}

// ruleRe matches numbered rule rows:
//
//	[ 1] 22/tcp ALLOW IN Anywhere
//	[ 2] 22/tcp (v6) ALLOW IN Anywhere (v6)
//	[ 3] Anywhere REJECT IN 192.168.1.5 on eth0
//
// The "(v6)" marker is folded into To/From for parsing and stripped for display.
var ruleRe = regexp.MustCompile(`^\[\s*(\d+)\]\s+(\S+?)(?:\s*\(v6\))?\s+(ALLOW|DENY|REJECT|LIMIT)\s+(IN|OUT)\s+(.+?)(?:\s+on\s+(\S+))?\s*$`)

func parseNumbered(out string) []Rule {
	var rules []Rule
	for _, line := range strings.Split(out, "\n") {
		m := ruleRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		rules = append(rules, Rule{
			Number:    atoi(m[1]),
			To:        m[2],
			Action:    m[3],
			Direction: m[4],
			From:      strings.TrimSuffix(strings.TrimSpace(m[5]), " (v6)"),
			Interface: m[6],
		})
	}
	return rules
}

// parseVerbose extracts the firewall state and default policies:
//
//	Status: active
//	Default: deny (incoming), allow (outgoing)
func parseVerbose(out string) (enabled bool, defaultIn, defaultOut string) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Status:"):
			enabled = strings.TrimSpace(strings.TrimPrefix(line, "Status:")) == "active"
		case strings.HasPrefix(line, "Default:"):
			rest := strings.TrimSpace(strings.TrimPrefix(line, "Default:"))
			parts := strings.SplitN(rest, ",", 2)
			if len(parts) == 2 {
				defaultIn = firstWord(parts[0])
				defaultOut = firstWord(parts[1])
			}
		}
	}
	return enabled, defaultIn, defaultOut
}

func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i]
	}
	return s
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
