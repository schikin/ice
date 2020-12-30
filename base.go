package ice

import (
	"fmt"
	"github.com/pion/stun"
	"github.com/pion/transport/vnet"
	"net"
)

type dataReceivedEvent struct {
	base Base
	data []byte
	remote net.Addr
}

type baseClosedEvent struct {
	base Base
}

type Base interface {
	NetworkType() NetworkType
	IP() *net.IP
	Address() string
	Port() int
	Component() *Component
	Connection() net.PacketConn
	LocalAddr() net.Addr
	Equals(other Base) bool

	start() error
	close() error

	write(data []byte, dst net.Addr) (int, error)
}

type UdpBase struct {
	addr *net.UDPAddr
	ip *net.IP
	netType NetworkType
	component *Component

	conn vnet.UDPPacketConn
}

func (u *UdpBase) Equals(other Base) bool {
	return u.LocalAddr().String() == other.LocalAddr().String() && u.NetworkType().String() == other.NetworkType().String()
}

func (u *UdpBase) LocalAddr() net.Addr {
	return u.addr
}

func (u *UdpBase) NetworkType() NetworkType {
	return u.netType
}

func (u *UdpBase) IP() *net.IP {
	return u.ip
}

func (u *UdpBase) Address() string {
	return u.addr.IP.String()
}

func (u *UdpBase) Port() int {
	return u.addr.Port
}

func (u *UdpBase) Component() *Component {
	return u.component
}

func (u *UdpBase) Connection() net.PacketConn {
	return u.conn
}

func (u *UdpBase) start() error {
	addr := fmt.Sprintf("%s:0", u.ip.String())

	vnet := u.component.Stream.Agent.net

	req, err := vnet.ResolveUDPAddr(udp, addr)

	if err != nil {
		return err
	}

	sock, err := vnet.ListenUDP(udp, req)

	if err != nil {
		return err
	}

	udpAddr, ok := sock.LocalAddr().(*net.UDPAddr)

	if !ok {
		return fmt.Errorf("internal error - bound socket did not return UDPAddr")
	}

	u.addr = udpAddr
	u.conn = sock

	go u.recvLoop()

	return nil
}

func demultiplexSTUN(buffer []byte) (*stun.Message, error) {
	if stun.IsMessage(buffer) {
		m := &stun.Message{
			Raw: make([]byte, len(buffer)),
		}
		// Explicitly copy raw buffer so Response can own the memory.
		copy(m.Raw, buffer)
		if err := m.Decode(); err != nil {
			return nil, err
		}

		return m, nil
	}

	return nil, nil
}

func (u *UdpBase) recvLoop() {
	//defer u.conn.Close()

	log := u.Component().log
	buffer := make([]byte, receiveMTU)
	for {
		n, srcAddr, err := u.conn.ReadFrom(buffer)
		if err != nil {
			event := baseClosedEvent{
				base: u,
			}

			log.Errorf("failed to recv, local=%s: %v", u.addr, err)

			u.component.dispatchEvent(event)
			return
		}

		data := buffer[:n]
		stunMsg, err := demultiplexSTUN(data)

		if err != nil {
			log.Warnf("failed to decode STUN from local=%s,remote=%s: %v", u.addr, srcAddr, err)
		}

		if stunMsg != nil {
			u.component.Stream.Agent.stunPacer.onInboundStun(u, stunMsg, srcAddr)
		} else {
			u.component.recvData(u, srcAddr, data)
		}
	}
}

func (u *UdpBase) close() error {
	return u.conn.Close() //this will trigger the recv loop close due to recv error
}

func (u *UdpBase) write(data []byte, dst net.Addr) (int, error) {
	return u.conn.WriteTo(data, dst)
}

func createUdpBase(addr *net.IP, component *Component) *UdpBase {
	nettype := NetworkTypeUDP4

	if addr.To4() == nil {
		nettype = NetworkTypeUDP6
	}

	return &UdpBase{
		addr:    nil,
		ip:      addr,
		netType: nettype,
		component: component,
	}
}

func CreateBase(network string, ip *net.IP, component *Component) (Base, error) {
	switch network {
	case udp:
		base := createUdpBase(ip, component)

		err := base.start()

		if err != nil {
			return nil, fmt.Errorf("could not bind UDP base: %s: %v", ip, err)
		}

		return base, nil
	case tcp:
		return nil, fmt.Errorf("TCP base requested but TCP is not supported")
	default:
		return nil, fmt.Errorf("requested unknown network type: %s", network)
	}
}
