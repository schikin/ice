package ice

import (
	"container/heap"
	"sync"
)

// https://tools.ietf.org/html/rfc8445#section-6.1.2.1

// ChecklistState represent the checklist state
type ChecklistState int

const (
	// ChecklistStateRunning The checklist is neither Completed nor Failed yet. Checklists are initially set to the Running state.
	ChecklistStateRunning ChecklistState = iota + 1

	// ChecklistStateCompleted The checklist contains a nominated pair for each component of the data stream.
	ChecklistStateCompleted

	// ChecklistStateFailed The checklist does not have a valid pair for each component
	//      of the data stream, and all of the candidate pairs in the
	//      checklist are in either the Failed or the Succeeded state.  In
	//      other words, at least one component of the checklist has candidate
	//      pairs that are all in the Failed state, which means the component
	//      has failed, which means the checklist has failed.
	ChecklistStateFailed
)

func (c ChecklistState) String() string {
	switch c {
	case ChecklistStateRunning:
		return "running"
	case ChecklistStateCompleted:
		return "completed"
	case ChecklistStateFailed:
		return "failed"
	}
	return "Unknown checklist state"
}

type candidatePairPriorityQueue struct {
	data []*CandidatePair
	idx map[*CandidatePair]int
}

func (pq candidatePairPriorityQueue) Len() int { return len(pq.data) }

func (pq candidatePairPriorityQueue) Less(i, j int) bool {
	// We want Pop to give us the highest, not lowest, priority so we use greater than here.
	return pq.data[i].priority > pq.data[j].priority
}

func (pq candidatePairPriorityQueue) Swap(i, j int) {
	pq.data[i], pq.data[j] = pq.data[j], pq.data[i]

	pq.idx[pq.data[i]] = i
	pq.idx[pq.data[j]] = j
}

func (pq *candidatePairPriorityQueue) Push(x interface{}) {
	n := len(pq.data)
	item := x.(*CandidatePair)
	pq.idx[item] = n
	pq.data = append(pq.data, item)
}

func (pq *candidatePairPriorityQueue) Pop() interface{} {
	old := pq.data
	n := len(old)
	item := old[n-1]
	old[n-1] = nil  // avoid memory leak
	pq.idx[item] = -1
	pq.data = old[0 : n-1]
	return item
}

// update modifies the priority and value of an Item in the queue.
//func (pq *candidatePairPriorityQueue) update(item *CandidatePair, value string, priority int) {
//	item.value = value
//	item.priority = priority
//	heap.Fix(pq, item.index)
//}
func (pq *candidatePairPriorityQueue) Remove(cp *CandidatePair) {
	heap.Remove(pq, pq.idx[cp])
}

type checklist struct {
	localCandidates []*LocalCandidate
	remoteCandidates []*Candidate

	stream *Stream
	all []*CandidatePair
	triggeredCheckQueue []*CandidatePair

	state ChecklistState
	mux sync.Mutex
}

func (c *checklist) getState() ChecklistState {
	c.mux.Lock()
	defer c.mux.Unlock()

	return c.state
}

func (c *checklist) popTriggeredQueue() *CandidatePair {
	c.mux.Lock()
	defer c.mux.Unlock()

	if len(c.triggeredCheckQueue) > 0 {
		ret := c.triggeredCheckQueue[0]
		c.triggeredCheckQueue = c.triggeredCheckQueue[1:]

		return ret
	} else {
		return nil
	}
}

//returns true if no action was performed and thus next checklist need to be picked
func (c *checklist) performConnectivityTrickle() bool {
	c.mux.Lock()
	defer c.mux.Unlock()

	if len(c.all) < 1 {
		return false
	}

}

func (c *checklist) processLocalCandidate(candidate *LocalCandidate) {

}

func (c *checklist) processRemoteCandidate(candidate *Candidate) {
	c.mux.Lock()
	defer c.mux.Unlock()

	c.remoteCandidates = append(c.remoteCandidates, candidate)

	//do pairing

	for _, local := range c.localCandidates {
		pair := newCandidatePair(local, candidate)

		c.all = append(c.all, pair)
	}
}