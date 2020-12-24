package ice

import (
	"fmt"
	"github.com/pion/logging"
	"github.com/pion/transport/vnet"
	"math/rand"
	"sync"
	"time"
)

type AgentConfig struct {
	// MulticastDNSMode controls mDNS behavior for the ICE agent
	MulticastDNS MulticastDNSParams

	// ConnectionTimeout defaults to 30 seconds when this property is nil.
	// If the duration is 0, we will never timeout this connection.
	ConnectionTimeout *time.Duration
	// KeepaliveInterval determines how often should we send ICE
	// keepalives (should be less then connectiontimeout above)
	// when this is nil, it defaults to 10 seconds.
	// A keepalive interval of 0 means we never send keepalive packets
	KeepaliveInterval *time.Duration

	// NetworkTypes is an optional configuration for disabling or enabling
	// support for specific network types.
	NetworkTypes []NetworkType

	// CandidateTypes is an optional configuration for disabling or enabling
	// support for specific candidate types.
	GatheringTypes []GatheringType

	LoggerFactory logging.LoggerFactory

	// MaxBindingRequests is the max amount of binding requests the agent will send
	// over a candidate pair for validation or nomination, if after MaxBindingRequests
	// the candidate is yet to answer a binding request or a nomination we set the pair as failed
	MaxBindingRequests *uint16

	// CandidatesSelectionTimeout specify a timeout for selecting candidates, if no nomination has happen
	// before this timeout, once hit we will nominate the best valid candidate available,
	// or mark the connection as failed if no valid candidate is available
	CandidateSelectionTimeout *time.Duration

	// HostAcceptanceMinWait specify a minimum wait time before selecting host candidates
	HostAcceptanceMinWait *time.Duration
	// HostAcceptanceMinWait specify a minimum wait time before selecting srflx candidates
	SrflxAcceptanceMinWait *time.Duration
	// HostAcceptanceMinWait specify a minimum wait time before selecting prflx candidates
	PrflxAcceptanceMinWait *time.Duration
	// HostAcceptanceMinWait specify a minimum wait time before selecting relay candidates
	RelayAcceptanceMinWait *time.Duration

	// Net is the our abstracted network interface for internal development purpose only
	// (see github.com/pion/transport/vnet)
	Net *vnet.Net

	TrickleMode *TrickleMode

	Standard *ICEStandard

	STUNConfig *STUNConfig
	TURNConfig *TURNConfig

	Pacing *int
	Mode *ICEMode
}


// NewAgent creates a new Agent
func NewAgent(config AgentConfig, signalChannel SignalChannel) (*Agent, error) {
	loggerFactory := config.LoggerFactory
	if loggerFactory == nil {
		loggerFactory = logging.NewDefaultLoggerFactory()
	}
	log := loggerFactory.NewLogger("ice")

	mDNS := NewMulticastDNSHelper(config, loggerFactory)

	var pacing int

	//validate pacing options
	if config.Pacing != nil {
		pacing = *config.Pacing

		if pacing > minPacing {
			return nil, fmt.Errorf("requested pacing of %dms but minimum pacing is %dms per standard", pacing, minPacing)
		}

		log.Debugf("configuring pacing at %dms as requested", pacing)
	} else {
		pacing = defaultPacing
		log.Debugf("configuring pacing at %dms per default", pacing)
	}

	a := &Agent{
		Config: 				config,
		Streams: 				make(map[string]*Stream),
		streamsOrdered:			make(map[int]*Stream),
		streamsLoopCounter:     0,

		events:           		make(EventChannel),

		tieBreaker:             rand.New(rand.NewSource(time.Now().UnixNano())).Uint64(),

		networkTypes:           config.NetworkTypes,
		mDNS: 					mDNS,

		localCredentials:		Credentials{
			UFrag: randSeq(16),
			Pwd:   randSeq(32),
		},

		foundationGenerator: 	newFoundationGenerator(),

		signalChannel:			signalChannel,
		connResultChannel:      make(chan interface{}, 1),

		localGatherState:	 	GatheringStateNew,
		remoteGatherState:	 	GatheringStateNew,

		localSignalQueue:		newSignalQueue(),
		remoteSignalQueue:		newSignalQueue(),

		loggerFactory: 			loggerFactory,
		log:           			log,

		mux:					sync.Mutex{},
	}

	a.stunPacer = newStunPacer(a, pacing)

	if config.Net == nil {
		log.Debugf("no VNet passed - will use physical network")
		a.net = vnet.NewNet(nil)
	} else {
		a.net = config.Net
	}

	if config.TrickleMode == nil {
		log.Debugf("configuring default trickle mode: %s", defaultTrickleMode)
		a.defaultTrickleMode = defaultTrickleMode
	} else {
		log.Debugf("configuring trickle mode as per requested by configuration: %s", *config.TrickleMode)
		a.defaultTrickleMode = *config.TrickleMode
	}

	if config.Standard == nil {
		log.Debugf("configuring ICE standard %s as per default", defaultStandard)
		a.localStandard = defaultStandard
	} else {
		log.Debugf("configuring ICE standard %s as per request", *config.Standard)
		a.localStandard = *config.Standard
	}

	if config.STUNConfig == nil {
		log.Infof("no STUN config provided - srflx gathering will not be possible")
		a.stunConfig = STUNConfig{Servers: []*URL{}}
	} else {
		a.stunConfig = *config.STUNConfig
	}

	if config.TURNConfig == nil {
		log.Infof("no TURN config provided - relay gathering will not be possible")
		a.turnConfig = TURNConfig{Servers: []*URL{}}
	}

	go a.eventLoop()

	return a, nil
}

//this is blocking (potentially forever)
func (a *Agent) Dial(request LocalSessionRequest) error {
	for _, strReq := range request.Streams {
		stream, err := newStreamLocal(a, strReq)

		if err != nil {
			return fmt.Errorf("stream '%s': %v", strReq.ID, err)
		}

		//could go through event loop also
		a.Streams[stream.ID] = stream
	}

	a.localGatherState = GatheringStateGathering

	a.startLocalOffer()

	localOfferSig := <- a.localSignalQueue.offer

	if localOfferSig == nil {
		return fmt.Errorf("failed to get local proposal - no offer has been emitted")
	}

	err := a.signalChannel.SendDescription(*localOfferSig)

	if err != nil {
		return fmt.Errorf("failed to send proposal to peer over signal channel: %v", err)
	}

	a.mux.Lock()
	a.localTrickleState = TrickleStateTrickling
	go a.flushRemoteQueue()
	a.mux.Unlock()

	res := <- a.connResultChannel

	switch typedRes := res.(type) {
	case error:
		return fmt.Errorf("dial error: %v", typedRes)
	default:
		return nil
	}
}

func (a *Agent) AcceptSession(request RemoteSessionRequest) (*RemoteSessionRequest, error) {
	pacing := a.negotiatePacing(request.Options.Pacing)
	peerIce2 := a.negotiateStandard(request.Options.Options)

	a.remoteCredentials = request.SessionCredentials

	if peerIce2 && a.localStandard != ICEStandardRFC8445 {
		return nil, fmt.Errorf("remote peer requested RFC8445 session but local configuration forces RFC5245")
	}

	var mode ICEMode

	if request.Options.Mode != nil {
		mode = *request.Options.Mode

		if mode == ICEModeLite {
			return nil, fmt.Errorf("remote peer requested ICE-Lite but it's not supported at the moment")
		}
	} else {
		a.log.Debugf("remote peer didn't specify ICE mode - assuming full")
	}

	sessionLevel := SessionParameters {
		Pacing: &pacing,
		Mode: &mode,
		Options: []ICEOption{},
	}

	if a.localStandard == ICEStandardRFC8445 {
		sessionLevel.Options = append(sessionLevel.Options, ICEOptionICE2)
	}

	ret := &RemoteSessionRequest{
		Options:		 sessionLevel,
		SessionCredentials: &a.localCredentials,
		Streams:         []RemoteStreamRequest{},
	}

	for _, strReq := range request.Streams {
		stream, err := newStreamRemote(a, strReq)

		if err != nil {
			return nil, fmt.Errorf("stream '%s': %v", strReq.ID, err)
		}

		//could go through event loop also
		a.Streams[stream.ID] = stream
	}

	streamsProp, err := a.getProposal()

	if err != nil {
		return nil, err
	}

	for _, prop := range streamsProp {
		ret.Streams = append(ret.Streams, *prop)
	}

	return ret, nil
}

// https://tools.ietf.org/html/draft-ietf-ice-rfc5245bis-20#section-14
func (a *Agent) AcceptResponse(response RemoteSessionRequest) error {
	haveStarted := a.haveStarted.Load().(bool)

	if !haveStarted {
		return fmt.Errorf("agent is not started - not able to accept response at this point")
	}

	a.negotiatePacing(response.Options.Pacing)
	a.negotiateStandard(response.Options.Options)
	a.remoteCredentials = response.SessionCredentials

	for _, streamReq := range response.Streams {
		stream, ok := a.Streams[streamReq.ID]

		if !ok {
			return fmt.Errorf("stream '%s' not found", streamReq.ID)
		}

		err := stream.acceptResponse(response, streamReq)

		if err != nil {
			return fmt.Errorf("stream '%s': %v", streamReq.ID, err)
		}
	}

	for id, str := range a.Streams {
		found := false

		for _, strRes := range response.Streams {
			if strRes.ID == id {
				found = true
				break
			}
		}

		if !found {
			a.log.Warnf("stream '%s' - no response received, closing", id)
			a.dispatchEvent(deleteStreamEvent{Stream: str})
		}
	}

	return nil
}

func (a *Agent) RemoteCandidate(streamId string, candidate *Candidate) error {
	stream, ok := a.Streams[streamId]

	if !ok {
		return fmt.Errorf("stream '%s' not found", streamId)
	}

	component, ok := stream.Components[candidate.ComponentId]

	if !ok {
		return fmt.Errorf("component '%d' not found for stream '%s'", candidate.ComponentId, streamId)
	}

	a.dispatchEvent(remoteCandidateEvent{
		Component: component,
		Candidate: candidate,
	})

	return nil
}

func (a *Agent) RemoteGatheringFinished() error {
	a.mux.Lock()
	defer a.mux.Unlock()

	a.remoteGatherState = GatheringStateComplete

	return nil
}

func (a *Agent) GetGatheringState() GatheringState {
	a.mux.Lock()
	defer a.mux.Unlock()

	return a.localGatherState
}

func (a *Agent) GetConnectionState() ConnectionState {
	a.mux.Lock()
	defer a.mux.Unlock()

	return a.connectionState
}
