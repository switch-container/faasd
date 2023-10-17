package provider

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
)

var dplogger = log.With().
	Str("component", "[DepolyPolicy]").
	Logger()

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
	// for switch this is candidate,
	// for reuse this is the container to be reused
	instance *CtrInstance
	// this is containers that should be killed
	killInstances []*CtrInstance
	// NOTE by huang-jl: this field point to ctr pool of lambda
	// that need to be depolyed (mabye not the same as `instance`)
	oldPool    *CtrPool
	targetPool *CtrPool
}

func (d DeployResult) applyMemUsage(m *LambdaManager) {
	switch d.decision {
	case COLD_START:
		m.memBound.AddCtr(d.targetPool.memoryRequirement)
	case REUSE:
	case SWITCH:
		m.memBound.SwitchCtr(d.targetPool.memoryRequirement, d.oldPool.memoryRequirement)
	default:
		dplogger.Error().Str("decision", d.decision.String()).Msg("find weird depoly decision")
	}
}

// Decide whether depoly a new container or not
type DeployPolicy interface {
	Decide(m *LambdaManager, lambdaName string) (DeployResult, error)
}

type NaiveSwitchPolicy struct{}

func (p NaiveSwitchPolicy) Decide(m *LambdaManager, lambdaName string) (res DeployResult, err error) {
	var (
		instance      *CtrInstance
		targetPool    *CtrPool
		oldPool       *CtrPool
		indices       []int
		killInstances []*CtrInstance
	)

	defer func() {
		if err == nil {
			res.applyMemUsage(m)
		}
	}()

	// targetPool is the pool of `lambdaName`
	targetPool, err = m.GetCtrPool(lambdaName)
	if err != nil {
		return
	}
	res.targetPool = targetPool
	instance = targetPool.PopFromFree()
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

		oldPool, err = m.GetCtrPoolWOLock(lambda)
		if err != nil {
			return
		}
		instance = oldPool.PopFromFree()
		if instance != nil {
			if !instance.status.Valid() {
				err = fmt.Errorf("detect invalid status instance: %+v", instance)
				return
			}
			res.decision = SWITCH
			res.instance = instance
			res.oldPool = oldPool
			// TODO(huang-jl) switch also need take memory limitation into consideration !?
			return
		}
	}

cold_start_routine:
	res.decision = COLD_START
	// make sure there is enough memory
	killInstances, err = m.findKillingInstanceFor(targetPool.memoryRequirement)
	if err != nil {
		err = errors.Wrapf(err, "findKillingInstanceFor %s (cold start) failed", lambdaName)
		return
	}
	res.killInstances = killInstances
	return
}

type BaselinePolicy struct{}

func (p BaselinePolicy) Decide(m *LambdaManager, lambdaName string) (res DeployResult, err error) {
	var (
		instance      *CtrInstance
		pool          *CtrPool
		killInstances []*CtrInstance
	)

	defer func() {
		if err == nil {
			res.applyMemUsage(m)
		}
	}()

	// targetPool is the pool of `lambdaName`
	pool, err = m.GetCtrPool(lambdaName)
	if err != nil {
		return
	}
	res.targetPool = pool
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
	killInstances, err = m.findKillingInstanceFor(pool.memoryRequirement)
	if err != nil {
		err = errors.Wrapf(err, "findKillingInstanceFor %s failed", lambdaName)
		return
	}
	res.killInstances = killInstances
	return
}

func (m *LambdaManager) pushBackKillingInstances(arr []*CtrInstance) {
	for _, instance := range arr {
		// this is a trade-off for simplicity
		instance.lastActive = time.Now()
		pool, _ := m.GetCtrPool(instance.LambdaName)
		pool.PushIntoFree(instance)
		// When we push back instance, we need add the memory been used back
		m.memBound.AddCtr(pool.memoryRequirement)
	}
}

// find to be killed instances for `memoryRequirement` amount of memory
// for now it is a random selection
func (m *LambdaManager) findKillingInstanceFor(needed int64) (res []*CtrInstance, err error) {
	defer func() {
		// push back instances if err is non-nil
		if err != nil {
			m.pushBackKillingInstances(res)
		}
	}()
	extraSpace := m.memBound.ExtraSpaceFor(needed)
	if extraSpace <= 0 {
		return
	}

	indices := rand.Perm(len(m.lambdas))
	// we reach here means system experience heavy load, so it is ok to require lock
	m.mu.Lock()
	defer m.mu.Unlock()

	var (
		pool    *CtrPool
		hasFree bool = true
	)
	for hasFree && extraSpace > 0 {
		hasFree = false
		for _, idx := range indices {
			lambda := m.lambdas[idx]

			pool, _ = m.GetCtrPoolWOLock(lambda)
			instance := pool.PopFromFree()
			if instance == nil {
				continue
			}
			// NOTE by huang-jl: we decrement the memory requirement here,
			// instead of the time it was destroy completely
			//
			// To be honest, the mem bound feature is hard to take effect accurately,
			// all the algorithms here is approximate (a trade-off between efficiency
			// and accuracy).
			m.memBound.RemoveCtr(pool.memoryRequirement)
			extraSpace -= pool.memoryRequirement
			res = append(res, instance)

			pool.mu.Lock()
			if len(pool.free) > 0 {
				hasFree = true
			}
			pool.mu.Unlock()
		}
	}

	if extraSpace > 0 {
		err = ErrMemoryNotEnough
	}
	return
}
