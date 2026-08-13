// Package system collects host metrics via gopsutil.
package system

import (
	"net"
	"runtime"
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

// NewCollector returns a ready collector.
func NewCollector() *Collector { return &Collector{lastAt: time.Now()} }

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
		dt := now.Sub(c.lastAt).Seconds()
		c.mu.Lock()
		if dt > 0 && !c.lastAt.IsZero() {
			rxPerSec = uint64(float64(rx-c.lastNet.BytesRecv) / dt)
			txPerSec = uint64(float64(tx-c.lastNet.BytesSent) / dt)
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
func firstOutboundIP() string {
	addrs, err := net.InterfaceAddrs()
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
