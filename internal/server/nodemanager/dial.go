package nodemanager

import (
	"crypto/tls"
	"strconv"
	"time"

	"github.com/gorilla/websocket"

	"gopit/internal/protocol"
	"gopit/internal/server/store"
)

// Dial opens a raw WebSocket connection to a node's agent and authenticates
// with the node's token. The terminal bridge uses it for one connection per
// session so raw binary frames need no session multiplexing and the
// manager's control connection stays untouched.
func Dial(n *store.Node, skipTLS bool) (*websocket.Conn, error) {
	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: skipTLS}, // ponytail: nodes are self-signed by default; pin CA when TLS is mandatory
	}
	ws, err := dialNode(n, dialer)
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(4 << 20) // a buggy/rogue agent must not stream unbounded data
	if err := auth(ws, n.Token); err != nil {
		ws.Close()
		return nil, &protocol.ErrRemote{Msg: "auth rejected"}
	}
	return ws, nil
}

func dialNode(n *store.Node, dialer websocket.Dialer) (*websocket.Conn, error) {
	// Scheme comes from the node: discovery records whether the agent serves
	// wss (tls_cert/tls_key set). Self-signed certs are accepted when the
	// server config says tls_skip_verify (the default).
	scheme := "ws"
	if n.TLS {
		scheme = "wss"
	}
	url := scheme + "://" + joinHost(n.IP, n.Port) + "/ws"
	ws, _, err := dialer.Dial(url, nil)
	return ws, err
}

// auth performs the pre-shared token handshake.
func auth(ws *websocket.Conn, token string) error {
	req := protocol.NewRequest("auth", struct {
		Token string `json:"token"`
	}{Token: token})
	if err := ws.WriteJSON(req); err != nil {
		return err
	}
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	var e protocol.Envelope
	if err := ws.ReadJSON(&e); err != nil {
		return err
	}
	ws.SetReadDeadline(time.Time{})
	if e.Type != protocol.TypeResponse || e.ID != req.ID || e.Error != nil {
		return &protocol.ErrRemote{Msg: "auth rejected"}
	}
	return nil
}

func joinHost(ip string, port int) string {
	return ip + ":" + strconv.Itoa(port)
}
