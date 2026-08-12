package runtime

import (
	"net"
	"sync"
)

type connectionGate struct {
	mu       sync.Mutex
	active   int
	byIP     map[string]int
	maxTotal int
	maxPerIP int
}

func newConnectionLimitedListener(listener net.Listener, maxTotal int, maxPerIP int) net.Listener {
	return &connectionLimitedListener{
		Listener: listener,
		gate: &connectionGate{
			byIP:     make(map[string]int),
			maxTotal: maxTotal,
			maxPerIP: maxPerIP,
		},
	}
}

type connectionLimitedListener struct {
	net.Listener
	gate *connectionGate
}

func (listener *connectionLimitedListener) Accept() (net.Conn, error) {
	for {
		connection, err := listener.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ip := remoteIP(connection.RemoteAddr())
		if listener.gate.acquire(ip) {
			return &limitedConnection{Conn: connection, gate: listener.gate, ip: ip}, nil
		}
		_ = connection.Close()
	}
}

func (gate *connectionGate) acquire(ip string) bool {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.active >= gate.maxTotal || gate.byIP[ip] >= gate.maxPerIP {
		return false
	}
	gate.active++
	gate.byIP[ip]++
	return true
}

func (gate *connectionGate) release(ip string) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	gate.active--
	gate.byIP[ip]--
	if gate.byIP[ip] == 0 {
		delete(gate.byIP, ip)
	}
}

type limitedConnection struct {
	net.Conn
	gate        *connectionGate
	ip          string
	releaseOnce sync.Once
}

func (connection *limitedConnection) Close() error {
	err := connection.Conn.Close()
	connection.releaseOnce.Do(func() { connection.gate.release(connection.ip) })
	return err
}

func remoteIP(address net.Addr) string {
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		return address.String()
	}
	return host
}
