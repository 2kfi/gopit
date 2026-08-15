//go:build !linux

package agent

import (
	"encoding/json"
	"errors"
	"fmt"

	"gopit/internal/agent/config"
)

// nftfwNew returns a backend that reports the feature as unavailable on
// non-Linux platforms: the nftables (netlink) backend requires Linux.
func nftfwNew(cfg *config.Config) fwBackend {
	return unavailableFW{}
}

type unavailableFW struct{}

func (unavailableFW) Call(method string, payload json.RawMessage) (any, error) {
	return nil, fmt.Errorf("firewall %s: %w", method, errors.New("the nftfw backend is only available on Linux"))
}
