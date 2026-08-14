// Package system collects host metrics via gopsutil.
package system

import (
	"net"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	psnet "github.com/shirou/gopsutil/v3/net"

	"gopit/internal/protocol"
)

// AgentVersion is stamped at build time via -ldflags.
var AgentVersion = "dev"

// Collector samples host metrics; it is safe for concurrent use.
type Collector struct {
	mu      sync.Mutex
	lastNet psnet.IOCountersStat
	lastAt  time.Time
}

// NewCollector returns a ready collector. lastAt stays zero until the first
// Stats call so the first delta is skipped (see Stats).
func NewCollector() *Collector { return &Collector{} }

// Info returns static node information. Port is filled by the caller.
func (c *Collector) Info(port int) protocol.NodeInfo {
	uptime, _ := host.Uptime()
	h, _ := host.Info()
	hostname := ""
	if h != nil {
		hostname = h.Hostname
	}
	return protocol.NodeInfo{
		Hostname:     hostname,
		IP:           firstOutboundIP(),
		Port:         port,
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		AgentVersion: AgentVersion,
		Uptime:       uptime,
	}
}

// Stats returns a fresh system snapshot with per-second network deltas.
func (c *Collector) Stats() protocol.SystemStats {
	now := time.Now()
	per, _ := cpu.Percent(0, false)
	percent := 0.0
	if len(per) > 0 {
		percent = per[0]
	}

	var memStats protocol.MemStats
	if m, err := mem.VirtualMemory(); err == nil {
		memStats = protocol.MemStats{Total: m.Total, Used: m.Used, Free: m.Free, Percent: m.UsedPercent}
	}

	var disks []protocol.DiskStats
	if parts, err := disk.Partitions(false); err == nil {
		for _, p := range parts {
			if p.Mountpoint == "" {
				continue
			}
			u, err := disk.Usage(p.Mountpoint)
			if err != nil {
				continue
			}
			disks = append(disks, protocol.DiskStats{
				Mount: p.Mountpoint, Total: u.Total, Used: u.Used, Free: u.Free, Percent: u.UsedPercent,
			})
		}
	}

	var rx, tx, rxPerSec, txPerSec uint64
	if counters, err := psnet.IOCounters(false); err == nil && len(counters) > 0 {
		rx, tx = counters[0].BytesRecv, counters[0].BytesSent
		c.mu.Lock()
		dt := now.Sub(c.lastAt).Seconds()
		// lastAt is zero until the first sample: the first delta is skipped,
		// otherwise (since-boot counters / tiny dt) reads as a huge spike.
		if !c.lastAt.IsZero() && dt > 0 {
			// monotonic guard: an interface reset makes the counter go
			// backwards; report 0 for that sample instead of a phantom rate.
			prev := c.lastNet
			if rx >= prev.BytesRecv {
				rxPerSec = uint64(float64(rx-prev.BytesRecv) / dt)
			}
			if tx >= prev.BytesSent {
				txPerSec = uint64(float64(tx-prev.BytesSent) / dt)
			}
		}
		c.lastNet, c.lastAt = counters[0], now
		c.mu.Unlock()
	}

	return protocol.SystemStats{
		CPU:  protocol.CPUStats{Percent: percent, Cores: runtime.NumCPU()},
		Mem:  memStats,
		Disk: disks,
		Net:  protocol.NetStats{RxBytes: rx, TxBytes: tx, RxPerSec: rxPerSec, TxPerSec: txPerSec},
	}
}

// firstOutboundIP returns the first non-loopback IPv4 of this host, or "".
// Virtual bridge/container interfaces (docker0, br-*, veth*, ...) are skipped
// so the reported IP is the host's real address; if none match, any
// non-loopback IPv4 is accepted as a fallback.
func firstOutboundIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, i := range ifaces {
		if isVirtualIface(i.Name) {
			continue
		}
		if ip := ifaceIPv4(i); ip != "" {
			return ip
		}
	}
	// Fallback: no physical-looking interface found; take any non-loopback.
	for _, i := range ifaces {
		if ip := ifaceIPv4(i); ip != "" {
			return ip
		}
	}
	return ""
}

func ifaceIPv4(i net.Interface) string {
	addrs, err := i.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ip, _, err := net.ParseCIDR(a.String())
		if err != nil || ip.IsLoopback() || ip.To4() == nil {
			continue
		}
		return ip.String()
	}
	return ""
}

// isVirtualIface reports whether a network interface is a container/bridge
// construct rather than a physical or primary host interface.
func isVirtualIface(name string) bool {
	for _, prefix := range []string{"docker", "br-", "veth", "virbr", "lxc", "lxd"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
