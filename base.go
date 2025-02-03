package ice

import (
	"fmt"
	"github.com/pion/transport/vnet"
	"net"
)

type base interface {
	NetworkType() NetworkType
	Component() *Component
	Connection() net.PacketConn

	Equals(other base) bool
}

type udpBase struct {
	addr      *net.UDPAddr
	ip        *net.IP
	netType   NetworkType
	component *Component

	virtualNet *vnet.Net

	conn vnet.UDPPacketConn
}

func createUdpBase(ip *net.IP, networkType NetworkType, virtualNet *vnet.Net, component *Component) (*udpBase, error) {
	addr := fmt.Sprintf("%s:0", ip.String())

	req, err := virtualNet.ResolveUDPAddr(udp, addr)

	if err != nil {
		return nil, err
	}

	conn, err := net.ListenUDP(udp, req)

	if err != nil {
		return nil, err
	}

	udpAddr, ok := conn.LocalAddr().(*net.UDPAddr)

	if !ok {
		return nil, fmt.Errorf("internal error - bound socket did not return UDPAddr")
	}

	ret := &udpBase{
		addr:       udpAddr,
		ip:         ip,
		netType:    networkType,
		component:  component,
		virtualNet: virtualNet,
		conn:       conn,
	}

	return ret, nil
}

func (u *udpBase) Equals(other base) bool {
	return u.Connection().LocalAddr().String() == other.Connection().LocalAddr().String() && u.NetworkType().String() == other.NetworkType().String()
}

func (u *udpBase) Connection() net.PacketConn {
	return u.conn
}

func (u *udpBase) NetworkType() NetworkType {
	return u.netType
}

func (u *udpBase) Component() *Component {
	return u.component
}

func (u *udpBase) Close() error {
	return u.conn.Close() //this will trigger the recv loop Close due to recv error
}

func createBase(ip *net.IP, networkType NetworkType, virtualNet *vnet.Net, component *Component) (base, error) {
	switch networkType {
	case NetworkTypeUDP4, NetworkTypeUDP6:
		return createUdpBase(ip, networkType, virtualNet, component)
	case NetworkTypeTCP4, NetworkTypeTCP6:
		return nil, fmt.Errorf("ICE-TCP is not supported")
	default:
		return nil, fmt.Errorf("requested unknown network type: %s", networkType.String())
	}
}
