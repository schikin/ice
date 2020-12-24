package ice

import (
	"fmt"
	"net"
	"strconv"
	"sync"
)

type foundationGenerator struct {
	known map[string]int
	mux sync.Mutex
}

func (g *foundationGenerator) generate(network string, ip *net.IP, relatedIp *net.IP, candidateType CandidateType) string {
	g.mux.Lock()
	defer g.mux.Unlock()

	var lookupValue string

	if candidateType == CandidateTypeHost {
		lookupValue = fmt.Sprintf("%s/%s", network, ip)
	} else {
		lookupValue = fmt.Sprintf("%s/%s/%s/%s", network, ip, relatedIp, candidateType)
	}

	foundationIdx, found := g.known[lookupValue]

	if !found {
		foundationIdx = len(g.known)
		g.known[lookupValue] = foundationIdx
	}

	return strconv.Itoa(foundationIdx)
}

func newFoundationGenerator() *foundationGenerator {
	return &foundationGenerator{
		known: make(map[string]int),
		mux:   sync.Mutex{},
	}
}
