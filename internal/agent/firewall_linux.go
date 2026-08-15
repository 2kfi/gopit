//go:build linux

package agent

import (
	"gopit/internal/agent/config"
	"gopit/internal/agent/nftfw"
)

// nftfwNew builds the nftfw (direct netlink) firewall backend. Linux only.
func nftfwNew(cfg *config.Config) fwBackend {
	return nftfw.New(cfg.Ufw.AllowToggle)
}
