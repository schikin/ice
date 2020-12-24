package ice

import (
	"fmt"
	"sync"
)
import "github.com/pion/logging"

//https://tools.ietf.org/html/rfc8445#section-4

type Event interface {}
type EventChannel chan Event

type streamEvent struct {
	stream *Stream
}

type streamSignalFinished struct {
	stream *Stream
}

type Stream struct {
	ID					string
	Agent	    		*Agent

	Components 			map[uint16]*Component

	localCredentials  	*Credentials
	remoteCredentials 	*Credentials

	localTrickleMode 	TrickleMode
	remoteTrickleMode 	TrickleMode

	localTrickleState 	TrickleState
	remoteTrickleState	TrickleState
	trickleStateIdx   	map[uint16]*trickleStrategy

	connectionState		ConnectionState
	gatheringState 		GatheringState

	checklist	   		*checklist

	log           		logging.LeveledLogger
	mux			  		sync.Mutex
}

func (s *Stream) acceptResponse(sessionRes RemoteSessionRequest, streamRes RemoteStreamRequest) error {
	s.mux.Lock()
	defer s.mux.Unlock()

	var creds *Credentials

	if streamRes.StreamCredentials == nil {
		creds = sessionRes.SessionCredentials
	}

	if creds == nil {
		return fmt.Errorf("neither session-level nor stream-level credentials found")
	}

	s.remoteCredentials = creds

	// this doesn't make sense in this case - we are initiating anyway so our proposal is sent already
	if streamRes.Trickle {
		s.remoteTrickleMode = TrickleModeFull
	} else {
		s.remoteTrickleMode = TrickleModeNone
	}

	for _, componentRes := range streamRes.Components {
		id := componentRes.ID
		comp, ok := s.Components[id]

		if !ok {
			return fmt.Errorf("received response for non-existent component '%d'", id)
		}

		err := comp.acceptResponse(componentRes)

		if err != nil {
			return fmt.Errorf("component '%d': %v", id, err)
		}
	}

	s.Agent.dispatchEvent(streamSignalFinished{stream:s})

	return nil
}

func (s *Stream) generateProposal() *RemoteStreamRequest {
	trickleFlag := s.localTrickleMode == TrickleModeHalf || s.localTrickleMode == TrickleModeFull

	compRequests := []RemoteComponentRequest{}

	for _, comp := range s.Components {
		candidates := []Candidate{}

		for _, cand := range comp.localCandidates {
			candidates = append(candidates, cand.Candidate)
		}

		compRequest := RemoteComponentRequest{
			ID:               comp.ID,
			Candidates:       candidates,
			RelatedComponent: nil,
		}

		compRequests = append(compRequests, compRequest)
	}

	proposal := &RemoteStreamRequest{
		ID:         s.ID,
		Components: compRequests,
		Trickle:    trickleFlag,
	}

	return proposal
}

func (s *Stream) processLocalCandidate(candidate *LocalCandidate) {
	//first propagate upwards to avoid double emission
	s.Agent.processLocalCandidate(s, candidate)

	componentId := candidate.ComponentId

	trickleState := s.trickleStateIdx[componentId]
	trickleReady := trickleState.isReady(s.localTrickleMode)

	if !trickleReady {
		switch candidate.Type {
		case CandidateTypeHost:
			trickleState.hasHost = true
			break
		case CandidateTypeServerReflexive:
			trickleState.hasSrflx = true
			break
		case CandidateTypeRelay:
			trickleState.hasRelay = true
			break
		}

		if trickleState.isReady(s.localTrickleMode) {
			s.processComponentProposalReady()
		}
	}


	//do pairing here also?
	s.log.Warnf("PAIRING BEGINS")
}

func (s *Stream) processComponentProposalReady() {
	s.mux.Lock()

	for _, ts := range s.trickleStateIdx {
		if !ts.isReady(s.localTrickleMode) {
			s.localTrickleState = TrickleStateWaiting
			return
		}
	}

	s.log.Debugf("stream '%s': trickle ready", s.ID)
	//trickle state changed - generate proposal and dispatch upwards
	s.localTrickleState = TrickleStateOfferReady
	s.mux.Unlock()

	s.Agent.processLocalTrickleState(s, TrickleStateOfferReady)
}

func (s *Stream) processGatherState(component *Component, state GatheringState) {
	hasError := false
	hasUnfinished := false

	outerLoop:
	for _, c := range s.Components {
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
			s.gatheringState = GatheringStateComplete
		} else {
			s.gatheringState = GatheringStateFailed
		}
	} else {
		s.gatheringState = GatheringStateGathering
	}

	if state == GatheringStateComplete {
		trickleState := s.trickleStateIdx[component.ID]
		trickleReady := trickleState.isReady(s.localTrickleMode)

		if !trickleReady {
			trickleState.isGatheringComplete = true

			if trickleState.isReady(s.localTrickleMode) {
				//trickle state changed - generate proposal and dispatch upwards
				s.mux.Lock()
				if s.localTrickleState == TrickleStateNew || s.localTrickleState == TrickleStateWaiting {
					s.localTrickleState = TrickleStateOfferReady
				} else {
					s.log.Infof("trickle offer status flipped to ready but trickle state is already = %s", s.localTrickleState)
				}
				s.mux.Unlock()
				s.Agent.processLocalTrickleState(s, TrickleStateOfferReady)
			}
		}
	}

	s.Agent.processGatherState(component, state)
}

func (s *Stream) getRemoteTrickleState() TrickleState {
	s.mux.Lock()
	defer s.mux.Unlock()

	return s.remoteTrickleState
}

func (s *Stream) getLocalTrickleState() TrickleState {
	s.mux.Lock()
	defer s.mux.Unlock()

	return s.localTrickleState
}

func (s *Stream) processRemoteCandidate(candidate *Candidate) {
	s.mux.Lock()
	defer s.mux.Unlock()

	s.checklist.processRemoteCandidate(candidate)

	s.Agent.processRemoteCandidate(candidate)
}

func (s *Stream) processRemoteEndOfCandidates() {
	s.mux.Lock()
	defer s.mux.Unlock()

	//double locking here (is it really that bad though?)
	s.Agent.processRemoteTrickleState(s, s.remoteTrickleState)
}

func (s *Stream) gather() {
	for _, comp := range s.Components {
		go comp.gather()
	}
}

func (s *Stream) close() {
	for _, comp := range s.Components {
		comp.close()
	}
}

func (s *Stream) dispatchEvent(evt interface{}) {
	s.Agent.dispatchEvent(evt)
}

func (s *Stream) getRemoteCredentials() *Credentials {
	if s.remoteCredentials == nil {
		return s.Agent.remoteCredentials
	} else {
		return s.remoteCredentials
	}
}


func (s *Stream) getLocalCredentials() *Credentials {
	if s.localCredentials == nil {
		return &s.Agent.localCredentials
	} else {
		return s.localCredentials
	}
}

func newStreamLocal(agent *Agent, request LocalStreamRequest) (*Stream, error) {
	trickleMode := agent.defaultTrickleMode

	if request.TrickleMode != nil {
		trickleMode = *request.TrickleMode
	}

	ret := &Stream{
		ID:         request.ID,
		Agent:      agent,
		Components: make(map[uint16]*Component),

		remoteCredentials: nil,
		localCredentials:  request.StreamCredentials,

		trickleStateIdx:   make(map[uint16]*trickleStrategy),
		localTrickleState: TrickleStateNew,
		remoteTrickleState: TrickleStateNew,

		localTrickleMode:  trickleMode,
		remoteTrickleMode: 0,

		log:               agent.log,
	}

	for _, compReq := range request.Components {
		comp, err := newComponentLocal(ret, compReq)

		if err != nil {
			return nil, fmt.Errorf("component '%d': %v", compReq.ID, err)
		}

		ret.Components[compReq.ID] = comp
		ret.trickleStateIdx[compReq.ID] = newTrickleStrategy(agent.Config.GatheringTypes)
	}

	return ret, nil
}

func newStreamRemote(agent *Agent, request RemoteStreamRequest) (*Stream, error) {
	log := agent.log
	localTrickleMode := agent.defaultTrickleMode
	remoteTrickleMode := agent.defaultTrickleMode
	remoteTrickleState := TrickleStateTrickling

	if request.Trickle {
		log.Debugf("remote requested trickle - enabling full trickle on local end")
		localTrickleMode = TrickleModeFull
		remoteTrickleMode = TrickleModeFull
	} else {
		log.Debugf("remote didn't request trickle - sticking with half trickle")
		localTrickleMode = TrickleModeHalf
		remoteTrickleMode = TrickleModeNone
		remoteTrickleState = TrickleStateFinished
	}


	ret := &Stream{
		ID:         request.ID,
		Agent:      agent,
		Components: make(map[uint16]*Component),

		remoteCredentials: request.StreamCredentials,
		localCredentials:  nil, //by default we stick with session-level credentials

		trickleStateIdx:   make(map[uint16]*trickleStrategy),
		localTrickleState: TrickleStateNew,
		remoteTrickleState: remoteTrickleState,

		localTrickleMode:  localTrickleMode,
		remoteTrickleMode: remoteTrickleMode,

		log:               log,
	}

	for _, compReq := range request.Components {
		comp, err := newComponentRemote(ret, compReq)

		if err != nil {
			return nil, fmt.Errorf("component '%d': %v", compReq.ID, err)
		}

		ret.Components[compReq.ID] = comp
		ret.trickleStateIdx[compReq.ID] = newTrickleStrategy(agent.Config.GatheringTypes)
	}

	return ret, nil
}