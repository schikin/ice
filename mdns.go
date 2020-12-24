package ice

import (
	"context"
	"crypto/rand"
	"errors"
	"github.com/pion/logging"
	"github.com/pion/mdns"
	"golang.org/x/net/ipv4"
	"net"
)

// MulticastDNSMode represents the different Multicast modes ICE can run in
type MulticastDNSMode byte

// MulticastDNSMode enum
const (
	// MulticastDNSModeDisabled means remote mDNS candidates will be discarded, and local host candidates will use IPs
	MulticastDNSModeDisabled MulticastDNSMode = iota + 1

	// MulticastDNSModeQueryOnly means remote mDNS candidates will be accepted, and local host candidates will use IPs
	MulticastDNSModeQueryOnly

	// MulticastDNSModeQueryAndGather means remote mDNS candidates will be accepted, and local host candidates will use mDNS
	MulticastDNSModeQueryAndGather
)

type MulticastDNSHelper struct {
	conn	*mdns.Conn

	log     logging.LeveledLogger
	Mode	MulticastDNSMode
	Name    string
}

func NewMulticastDNSHelper(parameters AgentConfig, lf logging.LoggerFactory) *MulticastDNSHelper {
	mDNSMode := parameters.MulticastDNS.Mode
	mDNSName := parameters.MulticastDNS.Name

	log := lf.NewLogger("mdns")

	if mDNSName == "" {
		name, err := generateMulticastDNSName()

		if err != nil {
			log.Errorf("Failed to generate mDNS name, disabling: (%s)", err)
			mDNSMode = MulticastDNSModeDisabled
		} else {
			mDNSName = name
		}
	}

	ret := &MulticastDNSHelper{
		conn: nil,

		log: log,
		Mode: mDNSMode,
		Name: mDNSName,
	}

	ret.Start()

	return ret
}

func (m *MulticastDNSHelper) Start() {
	if m.Mode == MulticastDNSModeDisabled {
		return
	}

	addr, mdnsErr := net.ResolveUDPAddr("udp4", mdns.DefaultAddress)
	if mdnsErr != nil {
		m.log.Errorf("Failed to resolve mDNS UDP address, disabling: (%s)", mdnsErr)
		m.Mode = MulticastDNSModeDisabled
		return
	}

	l, mdnsErr := net.ListenUDP("udp4", addr)
	if mdnsErr != nil {
		// If ICE fails to start MulticastDNS server just warn the user and continue
		m.log.Errorf("Failed to enable mDNS, continuing in mDNS disabled mode: (%s)", mdnsErr)
		m.Mode = MulticastDNSModeDisabled
		return
	}

	switch m.Mode {
	case MulticastDNSModeQueryOnly:
		conn, err := mdns.Server(ipv4.NewPacketConn(l), &mdns.Config{})

		if err != nil {
			m.log.Errorf("Failed to start mDNS server in resolve-only mode, continuing in mDNS disabled mode: (%s)", mdnsErr)
			m.Mode = MulticastDNSModeDisabled
			return
		}

		m.conn = conn
		return

	case MulticastDNSModeQueryAndGather:
		conn, err := mdns.Server(ipv4.NewPacketConn(l), &mdns.Config{
			LocalNames: []string{m.Name},
		})

		if err != nil {
			m.log.Errorf("Failed to start mDNS server in resolve and serve mode, continuing in mDNS disabled mode: (%s)", mdnsErr)
			m.Mode = MulticastDNSModeDisabled
			return
		}

		m.conn = conn
		return
	default:
		m.log.Warnf("unexpected mDNS mode. disabling: (%s)", m.Mode)
		m.Mode = MulticastDNSModeDisabled
		return
	}
}

func (m *MulticastDNSHelper) ResolvePeer(name string) (net.Addr, error) {
	if m.Mode == MulticastDNSModeDisabled {
		return nil, errors.New("mDNS is disabled - cannot resovle peer")
	}

	_, addr, err := m.conn.Query(context.Background(), name)

	if err != nil {
		return nil, err
	}

	return addr, nil
}

func (m *MulticastDNSHelper) GetLocalName() (string, error) {
	if m.Mode == MulticastDNSModeDisabled {
		return "", errors.New("mDNS is disabled - cannot resolve peer")
	}

	return m.Name, nil
}

func generateMulticastDNSName() (string, error) {
	b := make([]byte, 16)
	_, err := rand.Read(b) //nolint

	if err != nil {
		return "", err
	}

	return generateRandString("", ".local")
}
