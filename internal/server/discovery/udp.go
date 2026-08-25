// Package discovery performs UDP discovery of agent nodes.
package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopit/internal/protocol"
	"gopit/internal/server/store"
)

// Client sends DISCOVER broadcasts and collects ANNOUNCE replies.
type Client struct {
	BroadcastAddr string
	Port          int
	Timeout       time.Duration
}

// Reply is one discovered node.
type Reply struct {
	Info protocol.NodeInfo `json:"info"`
	From string            `json:"from"`
}

// Discover broadcasts on broadcast addr and loopback, returning replies.
func (c *Client) Discover() ([]Reply, error) {
	if c.BroadcastAddr == "" {
		c.BroadcastAddr = "255.255.255.255"
	}
	if c.Port == 0 {
		c.Port = 1221
	}
	if c.Timeout == 0 {
		c.Timeout = 500 * time.Millisecond
	}
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var opErr error
		err := c.Control(func(fd uintptr) {
			opErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
		})
		if err != nil {
			return err
		}
		return opErr
	}}
	conn, err := lc.ListenPacket(context.Background(), "udp4", ":0")
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(c.Timeout))
	targets := []string{
		net.JoinHostPort(c.BroadcastAddr, strconv.Itoa(c.Port)),
		net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Port)), // localhost testing
	}
	for _, t := range targets {
		addr, err := net.ResolveUDPAddr("udp4", t)
		if err != nil {
			continue
		}
		if _, err := conn.WriteTo([]byte("GOPIT DISCOVER"), addr); err != nil {
			slog.Warn("discover write failed", "addr", t, "err", err)
		}
	}
	var replies []Reply
	buf := make([]byte, 4096)
	seen := map[string]bool{}
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				break
			}
			continue
		}
		info, ok := ParseAnnounce(string(buf[:n]))
		if !ok {
			continue
		}
		key := info.UUID
		if key == "" {
			key = from.String()
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		replies = append(replies, Reply{Info: info, From: from.String()})
	}
	return replies, nil
}

// ParseAnnounce parses a raw UDP payload into NodeInfo.
func ParseAnnounce(payload string) (protocol.NodeInfo, bool) {
	const prefix = "ANNOUNCE "
	if !strings.HasPrefix(payload, prefix) {
		return protocol.NodeInfo{}, false
	}
	var info protocol.NodeInfo
	if err := json.Unmarshal([]byte(strings.TrimPrefix(payload, prefix)), &info); err != nil {
		return protocol.NodeInfo{}, false
	}
	return info, info.UUID != ""
}

// UpsertReplies writes replies into the store as pending nodes.
// New rows carry NO token: the fleet token is never handed to an unauthenticated
// announcer. Approval requires the operator to supply the agent's token first
// (POST /nodes/{uuid}/token); existing rows keep whatever token they have.
// The stored IP is pinned to the packet source so a forged announce cannot
// claim a victim's address.
func UpsertReplies(s *store.Store, replies []Reply) []store.Node {
	now := time.Now().UTC().Format(time.RFC3339)
	out := make([]store.Node, 0, len(replies))
	for _, r := range replies {
		i := r.Info
		ip := i.IP
		if host, _, err := net.SplitHostPort(r.From); err == nil {
			ip = host
		}
		n := &store.Node{
			ID: i.UUID, Hostname: i.Hostname, IP: ip, Port: i.Port,
			Status: store.StatusPending, OS: i.OS, Arch: i.Arch, AgentVersion: i.AgentVersion,
			FirstSeen: now, LastSeen: now, TLS: i.TLS,
		}
		if err := s.UpsertNode(n); err != nil {
			slog.Warn("upsert discovered node failed", "id", i.UUID, "err", err)
			continue
		}
		// Read back: an existing row kept its approved status/token; report
		// the stored reality, not the always-pending row we just built.
		stored, err := s.GetNode(i.UUID)
		if err != nil {
			out = append(out, *n) // vanished between upsert and read; best effort
			continue
		}
		out = append(out, *stored)
	}
	return out
}
