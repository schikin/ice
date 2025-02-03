package ice

import (
	"fmt"
	"net"
	"sync"
)

type localPortStatus int

const (
	portStatusNew localPortStatus = iota + 1
	portStatusActive
	portStatusClosed
)

type localPortDataReceiver func([]byte, net.Addr)

type localPortRequest struct {
	networkType NetworkType
	ip          net.IP
	//TODO: port range specification
	handler localPortDataReceiver
}

type localPort struct {
	networkType NetworkType
	address     net.Addr
	handler     localPortDataReceiver
	conn        net.PacketConn

	status localPortStatus

	lock sync.Mutex
}

func createLocalPort(request localPortRequest) (error, *localPort) {
	switch request.networkType {
	case NetworkTypeUDP4, NetworkTypeUDP6:
		addr := net.UDPAddr{
			IP: request.ip,
		}

		conn, err := net.ListenUDP(request.networkType.String(), &addr)

		if err != nil {
			return err, nil
		}

		ret := &localPort{
			networkType: request.networkType,
			address:     conn.LocalAddr(),
			handler:     request.handler,
			conn:        conn,
			status:      portStatusNew,
			lock:        sync.Mutex{},
		}

		go ret.receiveLoop()

		return nil, ret
	default:
		return fmt.Errorf("unsupported network type - %s", request.networkType.String()), nil
	}
}

func (l *localPort) receiveLoop() {
	var buf [receiveMTU]byte

	switch l.networkType {
	case NetworkTypeUDP4, NetworkTypeUDP6:
		udpConn := l.conn.(*net.UDPConn)
		l.status = portStatusActive

		for {
			bytesLen, remote, err := udpConn.ReadFromUDP(buf[:])

			if err != nil {
				return
			}

			l.handler(buf[:bytesLen], remote)
		}

	default:
		panic("unsupported network type")
	}
}

func (l *localPort) close() {
	l.status = portStatusClosed

	_ = l.conn.Close()
}
