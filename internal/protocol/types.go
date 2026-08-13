package protocol

// NodeInfo describes a managed node as reported by the agent.
type NodeInfo struct {
	UUID         string `json:"uuid"`
	Hostname     string `json:"hostname"`
	IP           string `json:"ip"`
	Port         int    `json:"port"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	AgentVersion string `json:"agent_version"`
	Uptime       uint64 `json:"uptime"`
	TLS          bool   `json:"tls"` // agent serves wss (tls_cert/tls_key set)
}

// SystemStats is the periodic snapshot pushed by the agent.
type SystemStats struct {
	CPU  CPUStats    `json:"cpu"`
	Mem  MemStats    `json:"mem"`
	Disk []DiskStats `json:"disk"`
	Net  NetStats    `json:"net"`
}

type CPUStats struct {
	Percent float64 `json:"percent"`
	Cores   int     `json:"cores"`
}

type MemStats struct {
	Total   uint64  `json:"total"`
	Used    uint64  `json:"used"`
	Free    uint64  `json:"free"`
	Percent float64 `json:"percent"`
}

type DiskStats struct {
	Mount   string  `json:"mount"`
	Total   uint64  `json:"total"`
	Used    uint64  `json:"used"`
	Free    uint64  `json:"free"`
	Percent float64 `json:"percent"`
}

type NetStats struct {
	RxBytes  uint64 `json:"rx_bytes"`
	TxBytes  uint64 `json:"tx_bytes"`
	RxPerSec uint64 `json:"rx_per_sec"`
	TxPerSec uint64 `json:"tx_per_sec"`
}
