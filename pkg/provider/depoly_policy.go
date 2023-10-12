package provider

import (
	"fmt"
	"math/rand"
)

type DeployDecision int

func (d DeployDecision) String() string {
	switch d {
	case COLD_START:
		return "cold start"
	case REUSE:
		return "reuse"
	case SWITCH:
		return "switch"
	default:
		return "invalid decision"
	}
}

const (
	COLD_START DeployDecision = iota
	REUSE
	SWITCH
)

type DeployResult struct {
	decision DeployDecision
	instance *CtrInstance
	// NOTE by huang-jl: this field point to ctr pool of lambda
	// that need to be depolyed (mabye not the same as `instance`)
	pool *CtrPool
}

// Decide whether depoly a new container or not
type DeployPolicy interface {
	Decide(m *LambdaManager, lambdaName string) (DeployResult, error)
}

type NaiveSwitchPolicy struct{}

func (p NaiveSwitchPolicy) Decide(m *LambdaManager, lambdaName string) (res DeployResult, err error) {
	var (
		instance *CtrInstance
		pool     *CtrPool
		indices  []int
	)

	// targetPool is the pool of `lambdaName`
	pool, err = m.GetCtrPool(lambdaName)
	if err != nil {
		return
	}
	res.pool = pool
	instance = pool.PopFromFree()
	if instance != nil {
		if !instance.status.Valid() {
			err = fmt.Errorf("detect invalid status instance: %+v", instance)
			return
		}
		res.decision = REUSE
		res.instance = instance
		return
	}
	// make sure we can checkpoint
	if !m.Runtime.checkpointCache.Lookup(lambdaName) {
		goto cold_start_routine
	}
	// find a candidate randomly from other pools
	indices = rand.Perm(len(m.lambdas))
	// TODO(huang-jl) remove this read lock
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, idx := range indices {
		lambda := m.lambdas[idx]
		if lambda == lambdaName {
			continue
		}

		pool, err = m.GetCtrPoolWOLock(lambda)
		if err != nil {
			return
		}
		instance = pool.PopFromFree()
		if instance != nil {
			if !instance.status.Valid() {
				err = fmt.Errorf("detect invalid status instance: %+v", instance)
				return
			}
			res.decision = SWITCH
			res.instance = instance
			return
		}
	}

cold_start_routine:
	res.decision = COLD_START
	return
}

type BaselinePolicy struct{}

func (p BaselinePolicy) Decide(m *LambdaManager, lambdaName string) (res DeployResult, err error) {
	var (
		instance *CtrInstance
		pool     *CtrPool
	)

	// targetPool is the pool of `lambdaName`
	pool, err = m.GetCtrPool(lambdaName)
	if err != nil {
		return
	}
	res.pool = pool
	instance = pool.PopFromFree()
	if instance != nil {
		if !instance.status.Valid() {
			err = fmt.Errorf("detect invalid status instance: %+v", instance)
			return
		}
		res.decision = REUSE
		res.instance = instance
		return
	}

	// if we cannot reuse, then cold-start
	res.decision = COLD_START
	return
}
