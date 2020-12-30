package ice

import (
	"errors"
	"fmt"
	"github.com/pion/logging"
	"github.com/pion/transport/vnet"
	"github.com/pion/turn"
	"net"
	"sync"
)

// gatheringState describes the state of the candidate gathering process
type GatheringState int

const (
	// GatheringStateNew indicates candidate gatering is not yet started
	GatheringStateNew GatheringState = iota + 1

	// GatheringStateGathering indicates candidate gathering is ongoing
	GatheringStateGathering

	// GatheringStateFailed indicates candidate gathering has failed
	GatheringStateFailed

	// GatheringStateComplete indicates candidate gathering has been completed
	GatheringStateComplete
)

// GatheringType describes the type of gatherer
type GatheringType int

const (
	// GatheringTypeLocal gather host candidates
	GatheringTypeHost GatheringType = iota + 1

	// GatheringSrflx gather server reflexive (STUN) candidates
	GatheringTypeSrflx

	// GatheringRelay gather relay (TURN) candidates
	GatheringTypeRelay
)

func (t GatheringState) String() string {
	switch t {
	case GatheringStateNew:
		return "new"
	case GatheringStateGathering:
		return "gathering"
	case GatheringStateFailed:
		return "failed"
	case GatheringStateComplete:
		return "complete"
	default:
		return ErrUnknownType.Error()
	}
}

type Gatherer struct {
	Config GathererConfig
	State GatheringState
	Candidates []*LocalCandidate

	NetworkTypes []NetworkType
	Component *Component

	isHost bool
	isSrflx bool
	isRelay bool

	net			  *vnet.Net
	log           logging.LeveledLogger
	ips			  []*net.IP

	wg	sync.WaitGroup
	mux sync.Mutex
}

type STUNConfig struct {
	Servers	[]*URL
}

type TURNConfig struct {
	Servers []*URL
}

type GathererConfig struct {
	Component	*Component

	VNet		*vnet.Net
	Logger		logging.LeveledLogger
	LoggerFactory	logging.LoggerFactory

	STUN	STUNConfig
	TURN	TURNConfig

	NetworkTypes []NetworkType
	GatheringTypes []GatheringType
}

type gathererEvent struct {
	Gatherer *Gatherer
}

type gathererStateEvent struct {
	gathererEvent
	State	GatheringState
}

type gathererCandidateEvent struct {
	gathererEvent
	Candidate	*LocalCandidate
}

func (g *Gatherer) GetConfig() AgentConfig {
	return g.Component.Stream.Agent.Config;
}

func (g *Gatherer) emitCandidate(candidate *LocalCandidate) {
	g.log.Debugf("gathered candidate %s", candidate)

	g.Candidates = append(g.Candidates, candidate)

	//dispatch upstream
	g.Component.dispatchEvent(gathererCandidateEvent{gathererEvent{Gatherer:g}, candidate})
}

func NewGatherer(config GathererConfig) (*Gatherer, error) {
	if config.Logger == nil && config.LoggerFactory == nil {
		return nil, errors.New("either logger or logger factory need to be provided");
	}

	if config.Logger == nil {
		config.Logger = config.LoggerFactory.NewLogger("gather")
	}

	if config.VNet == nil {
		config.VNet = vnet.NewNet(nil) //use physical network in case none other provided
	}

	if config.NetworkTypes == nil || len(config.NetworkTypes) == 0 {
		config.NetworkTypes = []NetworkType{NetworkTypeUDP4} //UDPv4 by default
	}

	if config.GatheringTypes == nil || len(config.GatheringTypes) == 0 {
		config.GatheringTypes = []GatheringType{GatheringTypeHost}; //by default host only
	}


	ret := &Gatherer{
		State:      GatheringStateNew,
		Candidates: []*LocalCandidate{},
		NetworkTypes: config.NetworkTypes,
		Component:  config.Component,

		isHost: false,
		isRelay: false,
		isSrflx: false,

		log: config.Logger,
		net: config.VNet,
		ips: []*net.IP{},

	}

	turnServersProvided := config.TURN.Servers != nil && len(config.TURN.Servers) != 0
	stunServersProvided := config.STUN.Servers != nil && len(config.STUN.Servers) != 0

	for _, gatherType := range config.GatheringTypes {
		switch gatherType {
		case GatheringTypeHost:
			ret.isHost = true
		case GatheringTypeSrflx:
			if !stunServersProvided {
				ret.log.Warn("Server reflexive gathering requested but no STUN servers provided - disabling")
				continue
			}

			for _, url := range config.STUN.Servers {
				if url.Scheme != SchemeTypeSTUN {
					return nil, fmt.Errorf("url %s is not STUN scheme", url)
				}
			}

			ret.isSrflx = true
		case GatheringTypeRelay:
			if !turnServersProvided {
				ret.log.Warn("Relay gathering requested but no TURN servers provided - disabling")
				continue
			}

			for _, url := range config.TURN.Servers {
				if url.Scheme != SchemeTypeTURN {
					return nil, fmt.Errorf("url %s is not TURN scheme", url)
				}
			}

			ret.isRelay = true
		}
	}

	if !ret.isRelay && !ret.isHost && !ret.isSrflx {
		return nil, fmt.Errorf("at least one gathering type required but none has been configured")
	}

	ret.Config = config

	return ret, nil
}

func (g *Gatherer) setState(state GatheringState) {
	g.log.Debugf("gatherer state: %s -> %s", g.State, state);

	g.State = state
	g.Component.dispatchEvent(gathererStateEvent{
		gathererEvent: gathererEvent{g},
		State:         state,
	})
}

func calculatePriority(ctype CandidateType, lpref uint16, componentId uint16) uint32 {
	return (1<<24)*uint32(ctype.Preference()) +
		(1<<8)*uint32(lpref) +
		uint32(256-componentId)
}

func (g *Gatherer) gatherSingleSrflx(base Base, url *URL, localPreference uint16) {
	defer g.wg.Done()

	network := base.NetworkType().NetworkShort()
	mdns := g.Component.Stream.Agent.mDNS
	vnet := g.Component.Stream.Agent.net

	hostPort := fmt.Sprintf("%s:%d", url.Host, url.Port)
	addr, err := vnet.ResolveUDPAddr(network, hostPort)

	if err != nil {
		g.log.Errorf("failed to resolve STUN server at %s: %v", hostPort, err)
		return
	}

	/*
		https://tools.ietf.org/html/rfc8445#section-5.1.1.2

		The gathering process is controlled using a timer, Ta.  Every time Ta
		expires, the agent can generate another new STUN or TURN transaction.
		This transaction can be either a retry of a previous transaction that
		failed with a recoverable error (such as authentication failure) or a
		transaction for a new host candidate and STUN or TURN server pair.
		The agent SHOULD NOT generate transactions more frequently than once
		per each ta expiration.  See Section 14 for guidance on how to set Ta
		and the STUN retransmit timer, RTO
	 */

	stunAddr, err := sendGatheringBind(base, addr, stunGatherTimeout)

	if err != nil {
		g.log.Errorf("could not get server reflexive address %s %s: %v\n", network, url, err)
		return
	}

	//relAddr, err := net.ResolveIPAddr("", stunAddr.IP.String())
	//
	//if err != nil {
	//	g.log.Errorf("could not resolve: %v\n", err)
	//	return
	//}

	address := base.Address()

	if mdns.Mode == MulticastDNSModeQueryAndGather {
		address = mdns.Name
	}

	port := base.Port()

	cb := Candidate{
		Foundation:    g.Component.Stream.Agent.foundationGenerator.generate(network, base.IP(), &addr.IP, CandidateTypeServerReflexive),
		ComponentId:   g.Component.ID,
		Transport:     network,
		TransportHost: stunAddr.IP.String(),
		TransportPort: stunAddr.Port,
		Priority:      calculatePriority(CandidateTypeServerReflexive, localPreference, g.Component.ID),
		Type:          CandidateTypeServerReflexive,
		RelatedHost:   address,
		RelatedPort:   port,
	}

	candidate := &LocalCandidate{
		Candidate: cb,
		base:      base,
	}

	g.emitCandidate(candidate)
}

//if both host and srflx gathering is used - reuse the host base for srflx candidate
func (g *Gatherer) gatherSrflx(hostCandidate *LocalCandidate) {
	defer g.wg.Done()

	for _, url := range g.Config.STUN.Servers {
		g.wg.Add(1)
		go g.gatherSingleSrflx(hostCandidate.base, url, 65535) //fixme later - localpref should be somehow smartly calculated
	}
}

func (g *Gatherer) gatherSingleHost(network string, ip *net.IP, localPreference uint16) {
	defer g.wg.Done()

	mdns := g.Component.Stream.Agent.mDNS

	base, err := CreateBase(network, ip, g.Component)

	if err != nil {
		g.log.Errorf("could not create base: %v", err)
		return
	}

	address := ip.String()

	if mdns.Mode == MulticastDNSModeQueryAndGather {
		address = mdns.Name
	}

	port := base.Port()

	cb := Candidate{
		Foundation:    g.Component.Stream.Agent.foundationGenerator.generate(network, ip, nil, CandidateTypeHost),
		ComponentId:   g.Component.ID,
		Transport:     network,
		TransportHost: address,
		TransportPort: port,
		Priority:      calculatePriority(CandidateTypeHost, localPreference, g.Component.ID),
		Type:          CandidateTypeHost,
		RelatedHost:   "",
		RelatedPort:   0,
	}

	candidate := &LocalCandidate{
		Candidate: cb,
		base:      base,
	}

	if g.isSrflx {
		g.wg.Add(1)
		go g.gatherSrflx(candidate)
	}

	if g.isRelay {
		g.wg.Add(1)
		go g.gatherRelay(candidate)
	}

	g.emitCandidate(candidate)
}

func (g *Gatherer) gatherHost() {
	defer g.wg.Done()

	g.wg.Add(len(g.ips) * len(supportedNetworks))
	for i, ip := range g.ips {
		for _, network := range supportedNetworks {
			var localPreference uint16

			if len(g.ips) == 1 {
				localPreference = 65535
			} else {
				localPreference = uint16(i)
			}

			go g.gatherSingleHost(network, ip, localPreference)
		}
	}
}

func (g *Gatherer) gatherRelaySingle(hostCandidate *LocalCandidate, url *URL) error {
	switch {
	case url.Scheme != SchemeTypeTURN:
		return errors.New("not a TURN url")
	case url.Username == "":
		return ErrUsernameEmpty
	case url.Password == "":
		return ErrPasswordEmpty
	}

	locConn := hostCandidate.base.Connection()

	client, err := turn.NewClient(&turn.ClientConfig{
		TURNServerAddr: fmt.Sprintf("%s:%d", url.Host, url.Port),
		Conn:           locConn,
		Username:       url.Username,
		Password:       url.Password,
		LoggerFactory:  g.Component.Stream.Agent.loggerFactory,
		Net:            g.net,
	})

	if err != nil {
		return err
	}

	err = client.Listen()
	if err != nil {
		return err
	}

	relayConn, err := client.Allocate()
	if err != nil {
		return err
	}

	g.log.Debugf("allocated %s", relayConn)
	return nil

	//laddr := locConn.LocalAddr().(*net.UDPAddr)
	//raddr := relayConn.LocalAddr().(*net.UDPAddr)
	//
	//relayConfig := CandidateRelayConfig{
	//	Network:   network,
	//	Component: g.Gatherer.Component,
	//	Address:   raddr.IP.String(),
	//	Port:      raddr.Port,
	//	RelAddr:   laddr.IP.String(),
	//	RelPort:   laddr.Port,
	//	OnClose: func() error {
	//		client.Close()
	//		return locConn.Close()
	//	},
	//}
	//candidate, err := NewCandidateRelay(&relayConfig)
	//if err != nil {
	//	log.Warnf("Failed to create relay candidate: %s %s: %v\n",
	//		network, raddr.String(), err)
	//	return err
	//}
	//
	//candidate.start(relayConn)
	//g.emitCandidate(candidate)
}

func (g *Gatherer) gatherRelay(hostCandidate *LocalCandidate) {
	defer g.wg.Done()

	//TODO: add relay here also
}

//TODO: error handling
func (g *Gatherer) Start() {
	g.log.Debugf("starting ICE candidates gathering");

	g.setState(GatheringStateGathering)
	g.readLocalInterfaces()

	g.wg.Add(1)
	go g.gatherHost()

	g.wg.Wait()
	g.setState(GatheringStateComplete)
}

func (g *Gatherer) readLocalInterfaces() {
	//networkTypes := g.NetworkTypes
	vnet := g.Component.Stream.Agent.net
	ips := []*net.IP{}
	ifaces, err := vnet.Interfaces()
	if err != nil {
		g.log.Errorf("failed to read interfaces: %s", err)
		g.setState(GatheringStateFailed)
	}

	//var IPv4Requested, IPv6Requested bool
	//for _, typ := range networkTypes {
	//	if typ.IsIPv4() {
	//		IPv4Requested = true
	//	}
	//
	//	if typ.IsIPv6() {
	//		IPv6Requested = true
	//	}
	//}

	for _, iface := range ifaces {

		if iface.Flags&net.FlagUp == 0 {
			continue // interface down
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue // loopback interface
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			var ip *net.IP

			switch addr := addr.(type) {
			case *net.IPNet:
				ip = &addr.IP
			case *net.IPAddr:
				ip = &addr.IP
				break
			default:

			}
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}

			ips = append(ips, ip)
		}
	}

	g.mux.Lock()
	defer g.mux.Unlock();

	g.ips = ips;
}