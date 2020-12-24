// Package ice implements the Interactive Connectivity Establishment (ICE)
// protocol defined in rfc5245.
package ice

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun"
	"github.com/pion/transport/vnet"
)

const (
	// defaultPacing is the pacing limiter that will be used for STUN transactions
	defaultPacing = 50 // 50 ms as per https://tools.ietf.org/html/draft-ietf-ice-rfc5245bis-20#section-14

	// minPacing is the minimum allowed ICE pacing
	minPacing = 50

	// taskLoopInterval is the interval at which the agent performs checks
	defaultTaskLoopInterval = 2 * time.Second

	// keepaliveInterval used to keep candidates alive
	defaultKeepaliveInterval = 10 * time.Second

	// defaultConnectionTimeout used to declare a connection dead
	defaultConnectionTimeout = 30 * time.Second

	// timeout for candidate selection, after this time, the best candidate is used
	defaultCandidateSelectionTimeout = 10 * time.Second

	// wait time before nominating a host candidate
	defaultHostAcceptanceMinWait = 0

	// wait time before nominating a srflx candidate
	defaultSrflxAcceptanceMinWait = 500 * time.Millisecond

	// wait time before nominating a prflx candidate
	defaultPrflxAcceptanceMinWait = 1000 * time.Millisecond

	// wait time before nominating a relay candidate
	defaultRelayAcceptanceMinWait = 2000 * time.Millisecond

	// max binding request before considering a pair failed
	defaultMaxBindingRequests = 7

	// the number of bytes that can be buffered before we start to error
	maxBufferSize = 1000 * 1000 // 1MB

	// gathering timeout
	gatherTimeout = 5 * time.Second

	// the number of outbound binding requests we cache
	// avoid using this for now - it's not required but recommended. not using this can improve performance
	maxPendingBindingRequests = 50

	// by default assume half-trickle for backwards compatibility
	defaultTrickleMode = TrickleModeHalf

	defaultStandard = ICEStandardRFC8445
	
	stunGatherTimeout = 5 * time.Second
)

var (
	defaultCandidateTypes = []CandidateType{CandidateTypeHost, CandidateTypeServerReflexive, CandidateTypeRelay}
)

type bindingRequest struct {
	transactionID  [stun.TransactionIDSize]byte
	destination    net.Addr
	isUseCandidate bool
}

type Credentials struct {
	UFrag string
	Pwd string
}

type LocalSessionRequest struct {
	Streams []LocalStreamRequest
}

type RemoteSessionRequest struct {
	Options            SessionParameters
	SessionCredentials *Credentials
	Streams            []RemoteStreamRequest
}

//compatible with WebRTC states
type ConnectionState int

const (
	// ConnectionStateNew The ICE agent is gathering addresses or is waiting to be given remote candidates through calls to RTCPeerConnection.addIceCandidate() (or both).
	ConnectionStateNew ConnectionState = iota + 1

	// ConnectionStateChecking The ICE agent has been given one or more remote candidates and is checking pairs of local and remote candidates against one another to try to find a compatible match, but has not yet found a pair which will allow the peer connection to be made. It's possible that gathering of candidates is also still underway.
	ConnectionStateChecking

	// ConnectionStateConnected A usable pairing of local and remote candidates has been found for all Components of the connection, and the connection has been established. It's possible that gathering is still underway, and it's also possible that the ICE agent is still checking candidates against one another looking for a better connection to use.
	ConnectionStateConnected

	// ConnectionStateCompleted The ICE agent has finished gathering candidates, has checked all pairs against one another, and has found a connection for all Components.
	ConnectionStateCompleted

	// ConnectionStateFailed The ICE candidate has checked all candidates pairs against one another and has failed to find compatible matches for all Components of the connection. It is, however, possible that the ICE agent did find compatible connections for some Components.
	ConnectionStateFailed

	// ConnectionStateDisconnected Checks to ensure that Components are still connected failed for at least one component of the RTCPeerConnection. This is a less stringent test than "failed" and may trigger intermittently and resolve just as spontaneously on less reliable networks, or during temporary disconnections. When the problem resolves, the connection may return to the "connected" state.
	ConnectionStateDisconnected

	// ConnectionStateClosed The ICE agent for this RTCPeerConnection has shut down and is no longer handling requests.
	ConnectionStateClosed
)

/*
		https://tools.ietf.org/html/rfc5245#section-15.4

		The "ice-pwd" and "ice-ufrag" attributes can appear at either the
	   	session-level or media-level.  When present in both, the value in the
	   	media-level takes precedence.  Thus, the value at the session-level
	   	is effectively a default that applies to all media streams, unless
	   	overridden by a media-level value.  Whether present at the session or
	   	media-level, there MUST be an ice-pwd and ice-ufrag attribute for
	   	each media stream.  If two media streams have identical ice-ufrag's,
	   	they MUST have identical ice-pwd's.
*/

type LocalStreamRequest struct {
	ID	string

	StreamCredentials *Credentials //you can choose to have per-stream credentials too
	TrickleMode *TrickleMode //trickle mode can be set at media-level as well as session-level
	Components	[]LocalComponentRequest
}

type RemoteStreamRequest struct {
	ID	string

	EndOfCandidates bool
	Trickle bool
	StreamCredentials *Credentials
	Components	[]RemoteComponentRequest
}

type RemoteComponentRequest struct {
	ID	uint16
	Candidates		[]Candidate
	RelatedComponent *RemoteComponentRequest
}

type LocalComponentRequest struct {
	ID	uint16
	RelatedComponent *LocalComponentRequest //hack for the case of RTP/RTCP mux disabled
}

type SessionProposal struct {
	NegotiateParams SessionParameters
	Streams          map[string]*StreamProposal //candidates by stream. to find out component id you can use field of candidate
}

type StreamProposal struct {
	ID string
	Candidates []Candidate
	Trickle bool
}

type restartPacerEvent struct {
}

type updatePeerStandard struct {
	Standard ICEStandard
}

type deleteStreamEvent struct {
	Stream *Stream
}

type remoteCandidateEvent struct {
	Component *Component
	Candidate *Candidate
}

func (a *Agent) eventLoop() {
	for {
		select {
		case signalEvt := <- a.signalChannel.Events():
			a.processRemoteSignal(signalEvt)
		case evt := <- a.events:
			switch typedEvt := evt.(type) {
			case poisonPill:
				a.log.Infof("poison pill received - stopping event loop")
				return
			case gathererCandidateEvent:
				typedEvt.Gatherer.Component.processLocalCandidate(typedEvt.Candidate)
				break
			case gathererStateEvent:
				typedEvt.Gatherer.Component.processGatherState(typedEvt.State)
				break
			}
		}
	}
}

func (a *Agent) processGatherState(component *Component, state GatheringState) {
	hasError := false
	hasUnfinished := false

outerLoop:
	for _, c := range a.Streams {
		switch c.gatheringState {
		case GatheringStateNew, GatheringStateGathering:
			hasUnfinished = true
			break outerLoop
		case GatheringStateFailed:
			hasError = true
			break
		}
	}

	if !hasUnfinished {
		if !hasError {
			a.setGatheringState(GatheringStateComplete)
		} else {
			a.setGatheringState(GatheringStateFailed)
		}
	} else {
		a.setGatheringState(GatheringStateGathering)
	}
}

func (a *Agent) start() {
	a.mux.Lock()
	defer a.mux.Unlock()

	a.isStarted = true

	a.stunPacer.start()
	go a.eventLoop()
}

func (a *Agent) close() {
	a.mux.Lock()
	defer a.mux.Unlock()

	for _, stream := range a.Streams {
		stream.close()
	}

	a.isStarted = false
	a.events <- poisonPill{}
	a.stunPacer.close()
}

func (a *Agent) flushRemoteQueue() {
candLoop:
	for {
		select {
		case candSig := <- a.remoteSignalQueue.candidate:
			stream, ok := a.Streams[candSig.StreamId]

			if !ok {
				a.log.Infof("unknown streamId=%s for enqueued candidate", candSig.StreamId)
				continue
			}

			stream.processRemoteCandidate(&candSig.Candidate)
			break
		default:
			a.log.Debugf("remote candidate queue flushed")
			break candLoop
		}
	}

eocLoop:
	for {
		select {
		case eocSig := <- a.remoteSignalQueue.eoc:
			stream, ok := a.Streams[eocSig.StreamId]

			if !ok {
				a.log.Infof("unknown streamId=%s for enqueued eoc", eocSig.StreamId)
				continue
			}

			stream.processRemoteEndOfCandidates()
			break
		default:
			a.log.Debugf("remote eoc queue flushed")
			break eocLoop
		}
	}
}

func (a *Agent) processRemoteSignal(signalEvt interface{}) {
	a.mux.Lock()
	defer a.mux.Unlock()

	state := a.localTrickleState

	if state == TrickleStateFinished {
		a.log.Debugf("received remote signal after trickling is finished - ignoring")
		return
	}

	trickling := state == TrickleStateTrickling

	switch typedSignal := signalEvt.(type) {
	case SignalOffer:
		a.remoteSignalQueue.offer <- &typedSignal
		break
	case SignalCandidate:
		if trickling {
			stream, ok := a.Streams[typedSignal.StreamId]

			if !ok {
				a.log.Infof("received candidate for unknown streamId=%s - ignoring", typedSignal.StreamId)
			}

			stream.processRemoteCandidate(&typedSignal.Candidate)
		} else {
			a.remoteSignalQueue.candidate <- &typedSignal
		}

		break
	case SignalEndOfCandidates:
		if trickling {
			stream, ok := a.Streams[typedSignal.StreamId]

			if !ok {
				a.log.Infof("received EOC for unknown streamId=%s - ignoring", typedSignal.StreamId)
			}

			stream.processRemoteEndOfCandidates()
		} else {
			a.remoteSignalQueue.eoc <- &typedSignal
		}
	}
}

func (a *Agent) processLocalCandidate(stream *Stream, candidate *LocalCandidate) {
	a.mux.Lock()
	defer a.mux.Unlock()

	currState := a.localTrickleState

	sig := SignalCandidate{
		Candidate: candidate.Candidate,
		StreamId:  stream.ID,
	}

	if currState == TrickleStateTrickling {
		//TODO: create additional event loop for side effect serialization - otherwise EOC might be emitted before all candidates are conveyed which is against standard
		go a.signalChannel.SendCandidate(sig)
	} else if currState == TrickleStateFinished {
		a.log.Debugf("received local candidate: %s after trickling is finished - ignoring", candidate.Candidate)
	} else {
		a.localSignalQueue.candidate <- &sig
	}
}

func (a *Agent) setGatheringState(state GatheringState) {
	a.mux.Lock()
	currState := a.localGatherState
	a.localGatherState = state
	a.mux.Unlock()

	if a.onGatherStateCallback != nil && currState != state {
		//side effect - run in goroutine
		go (*a.onGatherStateCallback)(state)
	}
}

func (a *Agent) setConnectionState(state ConnectionState) {
	a.mux.Lock()
	currState := a.connectionState
	a.connectionState = state
	a.mux.Unlock()

	if a.onConnectionStateCallback != nil && currState != state {
		//side effect - run in goroutine
		go (*a.onConnectionStateCallback)(state)
	}
}

func (a *Agent) onPacing() {
	a.mux.Lock()

	localTrickleState := a.localTrickleState
	remoteTrickleState := a.remoteTrickleState
	connState := a.connectionState

	a.mux.Unlock()

	connecting := connState == ConnectionStateChecking || connState == ConnectionStateConnected

	/*
		inside connectivity logic immediate STUN transactions will be generated
		this may lead to double-pacing (sending STUN transactions at double pacing rate)
		this is an incosistency within the trickle standard itself - it says that RFC5245 pacing needs to be followed
		but on the other hand it says that RFC5245 logic for connectivity checks should be followed. this is contradictory:
		in trickle both gathering transactions and connectivity transactions can occur at the same time
		if we try to strictly follow the pacing requirements - it kills the idea behind trickle of reducing the connection setup time -
		if we want to stick with strict queueing and execution of transactions then connectivity checks will never start
		before the gathering transactions are dispatched as gathering transactions are enqueued before the connectivity checks
		this would render ICE TRICKLE to always work in a mode similar to TRICKLE-NONE (checks only start after all candidates have been gathered)

		also, global 5ms pacing restriction is not followed here as rationale for 5ms pacing doesn't really apply to us:
		link to pacing rationale: https://tools.ietf.org/html/rfc8445#appendix-B.1

		1. 	we are building for server-side first and most of the time our interfaces will be facing WAN directly (without NAT)
			so the technical NAT binding time doesn't apply (as most of the times there will be no NAT for us
		2.  5ms restriction is relevant for all transactions (both gathering and connectivity checks) so given we have 500 clients
			per instance (which is more than a reasonable amount) that would limit the amount of connectivity checks we can issue
			to 2 per client per second. that would make performing connectivity checks on such instance impossible


		!thus, we relax the pacing restriction here in order to reduce the connection time

	 */
	if connecting {
		/* https://tools.ietf.org/html/draft-ietf-ice-trickle-21#section-8

		   As specified in [rfc5245bis], whenever timer Ta fires, only check
		   lists in the Running state will be picked when scheduling
		   connectivity checks for candidate pairs.  Therefore, a Trickle ICE
		   agent MUST keep each check list in the Running state as long as it
		   expects candidate pairs to be incrementally added to the check list.
		   After that, the check list state is set according to the procedures
		   in [rfc5245bis].

		 */

		if localTrickleState == TrickleStateFinished && remoteTrickleState == TrickleStateFinished {
			a.rfc8445ConnectivityLogic()
		} else {
			a.trickleConnectivityLogic()
		}
	}
}

func (a *Agent) rfc8445ConnectivityLogic() {

}

func (a *Agent) trickleConnectivityLogic() {
	n := len(a.Streams)

	for i := 0; i <= n; i++ {
		idx := a.streamsLoopCounter

		a.streamsLoopCounter = ( a.streamsLoopCounter + 1 ) % n

		stream := a.streamsOrdered[idx]

		triggeredPair := stream.checklist.popTriggeredQueue()

		if triggeredPair != nil {
			triggeredPair.startCheck()
			return
		}

		var waitList []*CandidatePair
		var frozenList []*CandidatePair

		for _, pair := range stream.checklist.all {
			if pair.getState() == CandidatePairStateFrozen {
				frozenList = append(frozenList, pair)
			} else if pair.getState() == CandidatePairStateWaiting {
				waitList = append(waitList, pair)
			}
		}

		/*
				3.  If there are one or more candidate pairs in the Waiting state,
		       	the agent picks the highest-priority candidate pair (if there are
		       	multiple pairs with the same priority, the pair with the lowest
		       	component ID is picked) in the Waiting state, performs a
		       	connectivity check on that pair, puts the candidate pair state to
		       	In-Progress, and aborts the subsequent steps.
		 */

		if len(waitList) > 0 {
			var maxPriority uint64
			var minComponentId uint16
			var selectedPair *CandidatePair

			for _, pair := range waitList {
				if pair.priority > maxPriority {
					selectedPair = pair
					maxPriority = pair.priority
					minComponentId = pair.local.ComponentId
				} else if pair.priority == maxPriority && pair.local.ComponentId < minComponentId {
					selectedPair = pair
					maxPriority = pair.priority
					minComponentId = pair.local.ComponentId
				}
			}

			if selectedPair == nil {
				a.log.Errorf("something impossible has happened")
				return
			}

			selectedPair.startCheck()
			return
		}

		/*
			   2.  If there is no candidate pair in the Waiting state, and if there
		       are one or more pairs in the Frozen state, the agent checks the
		       foundation associated with each pair in the Frozen state.  For a
		       given foundation, if there is no pair (in any checklist in the
		       checklist set) in the Waiting or In-Progress state, the agent
		       puts the candidate pair state to Waiting and continues with the
		       next step.
		 */

		if len(frozenList) > 0 {
			for _, pair := range frozenList {
				list := a.pairFoundationIdx[pair.foundation]

				if list == nil {
					a.log.Errorf("something went terribly wrong - pair foundation wasn't indexed")
					pair.startCheck()
					return
				}

				isFoundationRunning := false

				for _, otherPair := range list {
					if otherPair.getState() == CandidatePairStateInProgress || otherPair.getState() == CandidatePairStateWaiting {
						isFoundationRunning = true
						break
					}
				}

				if !isFoundationRunning {
					pair.startCheck()
					return
				}
			}
		}
	}
}

// Agent represents the ICE agent
type Agent struct {
	Config  			AgentConfig

	Streams 			map[string]*Stream
	streamsOrdered		map[int]*Stream
	//to control ordered execution of checks
	streamsLoopCounter 	int

	tieBreaker      	uint64
	connectionState 	ConnectionState

	mDNS				*MulticastDNSHelper

	isControlling 		bool
	isStarted			bool

	pairFoundationIdx	map[string][]*CandidatePair

	localCredentials 	Credentials
	remoteCredentials 	*Credentials

	localStandard  		ICEStandard
	remoteStandard 		ICEStandard

	networkTypes 		[]NetworkType

	defaultTrickleMode 	TrickleMode

	loggerFactory 		logging.LoggerFactory
	log           		logging.LeveledLogger

	stunPacer			*stunPacer
	stunConfig 	  		STUNConfig
	turnConfig	  		TURNConfig

	localGatherState 	GatheringState
	remoteGatherState 	GatheringState

	signalChannel		SignalChannel

	//before the initial proposal is generated
	localTrickleState 	TrickleState
	remoteTrickleState	TrickleState

	foundationGenerator *foundationGenerator

	events	  			EventChannel

	localSignalQueue			*signalQueue
	remoteSignalQueue			*signalQueue

	onGatherStateCallback 		*func(state GatheringState)
	onCandidateCallback 		*func(candidate Candidate)
	onConnectionStateCallback 	*func(state ConnectionState)

	//used to report results in dial/accept procedures
	connResultChannel	chan interface{}

	mux					sync.Mutex
	net 				*vnet.Net
}

func (a *Agent) processPair(pair *CandidatePair) {
	a.mux.Lock()
	defer a.mux.Unlock()

	foundation := pair.foundation
	list, ok := a.pairFoundationIdx[foundation]

	if !ok {
		list = []*CandidatePair{}
	}

	a.pairFoundationIdx[foundation] = append(list, pair)
}

func (a* Agent) dispatchEvent(evt Event) {
	a.events <- evt
}

/* https://tools.ietf.org/html/rfc8445#section-7.3.1.1

   resolves role conflict and returns the stun message
*/
func (a* Agent) resolveRoleConflict(remoteControlAttribute AttrControl) (bool, stun.Setter) {
	a.mux.Lock()
	defer a.mux.Unlock()

	remoteTieBreaker := remoteControlAttribute.Tiebreaker

	switch remoteControlAttribute.Role {
	case Controlling:
		if a.isControlling {
			if a.tieBreaker >= remoteTieBreaker {
				a.log.Infof("agent will insist on being controlling due to tiebreaker")
				ret := stun.ErrorCodeAttribute{Code: stun.CodeRoleConflict, Reason: []byte("Agent is in controlling role")}
				return false, ret
			} else {
				a.log.Infof("agent is switching to controlled mode due to tiebreaker")
				a.isControlling = false

				a.recalculatePairPriorities()

				return true, AttrControl {
					Role: Controlled,
					Tiebreaker: a.tieBreaker,
				}
			}
		}
		break
	case Controlled:
		if !a.isControlling {
			if a.tieBreaker >= remoteTieBreaker {
				a.log.Infof("agent is switching to controlling mode due to tiebreaker")
				a.isControlling = true

				a.recalculatePairPriorities()

				return true, AttrControl {
					Role: Controlling,
					Tiebreaker: a.tieBreaker,
				}
			} else {
				a.log.Infof("agent will insist on being controlled due to tiebreaker")
				ret := stun.ErrorCodeAttribute{Code: stun.CodeRoleConflict, Reason: []byte("Agent is in controlled role")}
				return false, ret

			}
		}
		break
	}

	return true, nil
}

//TODO: implement me (this is non-blocking as block is upstream in code)
func (a *Agent) recalculatePairPriorities() {

}

func (a *Agent) negotiatePacing(request *int) int {
	a.mux.Lock()
	defer a.mux.Unlock()

	currentPacing := a.stunPacer.getPacing()

	if request != nil {
		requestedPacing := *request

		if requestedPacing > minPacing {
			a.log.Warnf("remote peer requested pacing >%dms, will try to use local pacing of %dms instead", requestedPacing, currentPacing)
		} else {
			if requestedPacing > currentPacing {
				a.log.Debugf("remote peer proposed greater value of %dms for pacing - using it", requestedPacing)
				a.stunPacer.setPacing(requestedPacing)
				return requestedPacing
			} else {
				a.log.Debugf("remote peer requested pacing that is lower than local, using local value of %dms", currentPacing)
			}
		}
	} else {
		a.log.Debugf("remote peer didn't propose pacing - using local value of %dms", currentPacing)
	}

	return currentPacing
}

func (a *Agent) processRemoteCandidate(candidate *Candidate) {
	a.mux.Lock()
	defer a.mux.Unlock()

	connState := a.connectionState

	switch connState {
	case ConnectionStateNew:
		a.setConnectionState(ConnectionStateChecking)
		break
	case ConnectionStateDisconnected, ConnectionStateClosed, ConnectionStateFailed, ConnectionStateCompleted:
		a.log.Debugf("received remote candidate for connection in state: %s - ignoring", connState)
		break
	}
}

func (a *Agent) negotiateStandard(peerOptions []ICEOption) bool {
	a.mux.Lock()
	defer a.mux.Unlock()

	peerConfirmed8445 := false

	for _, opt := range peerOptions {
		switch opt {
		case ICEOptionICE2:
			a.log.Debugf("peer confirmed RFC8455 support. VERY NICE! GREAT SUCCESS!")
			a.remoteStandard = ICEStandardRFC8445
			peerConfirmed8445 = true
		}
	}

	if !peerConfirmed8445 {
		a.log.Warnf("peer did not confirm RFC8445 support - we will try to run in compatibility mode")
		a.remoteStandard = ICEStandardRFC5245
	}

	return peerConfirmed8445
}

//blocks until the proposal is ready as per the trickle mode
func (a *Agent) startLocalOffer() {
	a.mux.Lock()
	defer a.mux.Unlock()

	a.localTrickleState = TrickleStateWaiting

	for _, str := range a.Streams {
		go str.gather()
	}
}

//this has to be run within blocking context to avoid race conditions with losing/duplicating candidates
func (a *Agent) generateOffer() {
	streamsProps := []RemoteStreamRequest{}

	var iceMode ICEMode
	iceMode = ICEModeFull

	currPacing := a.stunPacer.getPacing()

	opts := SessionParameters{
		Pacing:  &currPacing,
		Mode:    &iceMode,
		Options: []ICEOption{},
	}

	if a.localStandard == ICEStandardRFC8445 {
		opts.Options = append(opts.Options, ICEOptionICE2)
	}

	//flush queues
	offers, candidates, eocs := a.localSignalQueue.flush()

	if offers != nil && len(offers) > 0 {
		a.log.Warnf("offer was already in queue: something has gone wrong")
	}

	for streamId, stream := range a.Streams {
		streamProp := &RemoteStreamRequest{
			ID:                streamId,
			Trickle:           stream.localTrickleMode == TrickleModeFull || stream.localTrickleMode == TrickleModeHalf,
			StreamCredentials: stream.localCredentials,
			EndOfCandidates:   false,
			Components:        []RemoteComponentRequest{},
		}

		for compId, _ := range stream.Components {
			compProp := RemoteComponentRequest{
				ID:               compId,
				Candidates:       nil,
				RelatedComponent: nil,
			}

			for _, compSig := range candidates {
				if compSig.StreamId == streamId && compSig.Candidate.ComponentId == compId {
					compProp.Candidates = append(compProp.Candidates, compSig.Candidate)
				}
			}

			streamProp.Components = append(streamProp.Components, compProp)
		}

		for _, eocSig := range eocs {
			if eocSig.StreamId == streamId {
				streamProp.EndOfCandidates = true
				break
			}
		}

		streamsProps = append(streamsProps, *streamProp);
	}


	res := &RemoteSessionRequest{
		Options:            opts,
		SessionCredentials: &a.localCredentials,
		Streams:            streamsProps,
	}

	a.localTrickleState = TrickleStateOfferReady
	a.localSignalQueue.offer <- &SignalOffer{Request: *res}
}

func (a *Agent) processRemoteTrickleState(stream *Stream, state TrickleState) {
	a.mux.Lock()
	defer a.mux.Unlock()

	finished := true

	if state == TrickleStateFinished {
		for _, stream := range a.Streams {
			if stream.getRemoteTrickleState() != TrickleStateFinished {
				finished = false
				break
			}
		}
	}

	if finished {
		a.remoteTrickleState = TrickleStateFinished
	} else {
		//doesn't matter - let's assume they are trickling
		a.remoteTrickleState = TrickleStateTrickling
	}
}

func (a *Agent) processLocalTrickleState(stream *Stream, state TrickleState) {
	a.mux.Lock()
	defer a.mux.Unlock()

	currState := a.localTrickleState

	if currState == TrickleStateFinished {
		a.log.Warnf("local trickle state change after trickling has finished - something has gone wrong")
		return
	}

	switch state {
	case TrickleStateWaiting, TrickleStateNew:
		if currState == TrickleStateOfferReady || currState == TrickleStateFinished || currState == TrickleStateTrickling {
			a.log.Warnf("trying to decrease trickle status, curr=%s, stream reported=%s - ignoring", currState, state)
			return
		}

		a.localTrickleState = TrickleStateWaiting
		return
	case TrickleStateOfferReady:
		ready := true

		for _, str := range a.Streams {
			if str.getLocalTrickleState() == TrickleStateNew || str.getLocalTrickleState() == TrickleStateWaiting {
				ready = false
				break
			}
		}

		if ready {
			//trickle state is updated inside that function
			a.generateOffer()
		}
		break
	case TrickleStateTrickling:
		trickling := true

		for _, str := range a.Streams {
			if str.getLocalTrickleState() != TrickleStateFinished && str.getLocalTrickleState() != TrickleStateTrickling {
				trickling = false
				break
			}
		}

		if trickling {
			a.localTrickleState = TrickleStateTrickling
		}
		break
	case TrickleStateFinished:
		finished := true

		for _, str := range a.Streams {
			if str.getLocalTrickleState() != TrickleStateFinished {
				finished = false
				break
			}
		}

		if finished {
			a.localTrickleState = TrickleStateFinished
		}
	}
}
