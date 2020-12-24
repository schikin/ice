package ice

//Implements ICE trickling as per https://tools.ietf.org/html/draft-ietf-ice-trickle-21

type TrickleMode int

const (
	// TrickleModeNone indicates ICE trickling disabled. Use this mode if peer agent explicitly specified it doesn't support trickling
	TrickleModeNone TrickleMode = iota + 1

	// TrickleModeHalf indicates half trickling as per RFC draft. Use half trickling when it's not possible to determine if peer agent supports trickle or not
	TrickleModeHalf

	// TrickleModeFull indicates full trickling as per RFC draft. In this mode agent expects peer to be fully supporting trickling
	TrickleModeFull
)

func (m TrickleMode) String() string {
	switch m {
	case TrickleModeFull:
		return "full-trickle"
	case TrickleModeHalf:
		return "half-trickle"
	case TrickleModeNone:
		return "none"
	default:
		return "unknown"
	}
}

type TrickleState int

const (
	// TrickleStateNew indicates that trickling wasn't started
	TrickleStateNew TrickleState = iota + 1

	// TrickleStateWaiting indicates that agent is waiting for trickle strategy to report the ready state
	TrickleStateWaiting

	// TrickleStateOfferReady indicates that agent has an offer and its signalling it
	TrickleStateOfferReady

	// TrickleStateTrickling indicates that agent is actively trickling candidates
	TrickleStateTrickling

	// TrickleStateFinished indicates that trickling has commenced for a stream (end-of-candidates has been conveyed between peers)
	TrickleStateFinished
)

func (m TrickleState) String() string {
	switch m {
	case TrickleStateNew:
		return "new"
	case TrickleStateWaiting:
		return "waiting"
	case TrickleStateTrickling:
		return "trickling"
	case TrickleStateOfferReady:
		return "ready"
	case TrickleStateFinished:
		return "failed"
	default:
		return "unknown"
	}
}

// SignalState describes the state of trickle signalling
type SignalState int

const (
	// SignalStateNew indicates signalling is not yet started
	SignalStateNew SignalState = iota + 1

	// SignalStateRunning indicates that trickle signalling is ongoing
	SignalStateRunning

	// SignalStateComplete indicates that trickling is finished
	SignalStateComplete
)

func (m SignalState) String() string {
	switch m {
	case SignalStateNew:
		return "new"
	case SignalStateRunning:
		return "running"
	case SignalStateComplete:
		return "complete"
	default:
		return "unknown"
	}
}

type trickleStrategy struct {
	requireHost bool
	requireSrflx bool
	requireRelay bool

	hasHost bool
	hasSrflx bool
	hasRelay bool

	isGatheringComplete bool
}

func (t *trickleStrategy) isReady(mode TrickleMode) bool {
	switch mode {
	case TrickleModeNone:
		return t.isGatheringComplete
	case TrickleModeHalf:
		if t.requireHost && !t.hasHost {
			return false
		}

		if t.requireRelay && !t.hasRelay {
			return false
		}

		if t.requireSrflx && !t.hasSrflx {
			return false
		}

		return true
	case TrickleModeFull:
		return t.hasHost
	}

	return false //should never be reached
}

func newTrickleStrategy(requiredTypes []GatheringType) *trickleStrategy {
	ret := &trickleStrategy{
		requireHost:         true,
		requireSrflx:        false,
		requireRelay:        false,
		hasHost:             false,
		hasSrflx:            false,
		hasRelay:            false,
		isGatheringComplete: false,
	}

	for _, typ := range requiredTypes {
		switch typ {
		case GatheringTypeSrflx:
			ret.requireSrflx = true
			break
		case GatheringTypeRelay:
			ret.requireRelay = true
			break
		}
	}

	return ret
}


//NOTE: Trickle mode is a negotiatable parameter - it _can_ change during the ICE negotiation. everything here needs to be built with that in mind