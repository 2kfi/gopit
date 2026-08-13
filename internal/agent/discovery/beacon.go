// Package discovery implements the UDP presence beacon.
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"syscall"

	"gopit/internal/protocol"
)

const (
	DiscoverMsg    = "GOPIT DISCOVER"
	AnnouncePrefix = "ANNOUNCE "
)

// Beacon answers DISCOVER broadcasts with our NodeInfo.
type Beacon struct {
	info  protocol.NodeInfo
	conns []*net.UDPConn
	mu    sync.Mutex
	done  chan struct{}
	wg    sync.WaitGroup
}

// NewBeacon creates the beacon but does not start listening.
func NewBeacon(info protocol.NodeInfo) *Beacon {
	return &Beacon{info: info, done: make(chan struct{})}
}

// reuseAddrV6Only sets SO_REUSEADDR and forces IPV6_V6ONLY so the IPv4 and
// IPv6 wildcard sockets can coexist on the same port.
func reuseAddrV6Only(network, address string, c syscall.RawConn) error {
	var opErr error
	err := c.Control(func(fd uintptr) {
		opErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
		if opErr != nil {
			return
		}
		if network == "udp6" {
			opErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_V6ONLY, 1)
		}
	})
	if err != nil {
		return err
	}
	return opErr
}

// Start binds UDP on the given port for both IPv4 and IPv6-any.
// The v6 socket failure is ignored (some hosts lack v6 support).
func (b *Beacon) Start(port int) error {
	lc := net.ListenConfig{Control: reuseAddrV6Only}
	var bindErrs []error
	for _, network := range []string{"udp4", "udp6"} {
		addr := fmt.Sprintf("[::]:%d", port)
		if network == "udp4" {
			addr = fmt.Sprintf("0.0.0.0:%d", port)
		}
		pc, err := lc.ListenPacket(context.Background(), network, addr)
		if err != nil {
			if network == "udp4" {
				bindErrs = append(bindErrs, err)
				continue
			}
			continue // ponytail: v6 unsupported, v4 socket suffices
		}
		c := pc.(*net.UDPConn)
		b.conns = append(b.conns, c)
		b.wg.Add(1)
		go b.readLoop(c)
	}
	if len(b.conns) == 0 {
		return fmt.Errorf("no UDP sockets bound: %v", bindErrs)
	}
	return nil
}

func (b *Beacon) readLoop(c *net.UDPConn) {
	defer b.wg.Done()
	buf := make([]byte, 1500)
	for {
		n, addr, err := c.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-b.done:
				return
			default:
				continue
			}
		}
		if string(buf[:n]) != DiscoverMsg {
			continue
		}
		body, _ := json.Marshal(b.info)
		reply := append([]byte(AnnouncePrefix), body...)
		b.mu.Lock()
		_, err = c.WriteToUDP(reply, addr)
		b.mu.Unlock()
		_ = err
	}
}

// Close stops all listeners.
func (b *Beacon) Close() error {
	select {
	case <-b.done:
	default:
		close(b.done)
	}
	for _, c := range b.conns {
		c.Close()
	}
	b.wg.Wait()
	return nil
}
