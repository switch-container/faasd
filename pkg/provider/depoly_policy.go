package provider

import (
	"fmt"

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

func (d DeployResult) getKillInstanceIDs() []string {
	var killIDs []string
	for _, instance := range d.killInstances {
		killIDs = append(killIDs, instance.GetInstanceID())
	}
	return killIDs
}

// Decide whether depoly a new container or not
type DeployPolicy interface {
	// NOTE by huang-jl: Decide() will occupy memory even if it may failed by CtrRuntime
	// or other component. The Caller need to free memory bound if necessary.
	Decide(m *LambdaManager, lambdaName string) (DeployResult, error)
}

type NaiveSwitchPolicy struct{}

func (p NaiveSwitchPolicy) Decide(m *LambdaManager, lambdaName string) (res DeployResult, err error) {
	var (
		instance      *CtrInstance
		targetPool    *CtrPool
		oldPool       *CtrPool
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
		if err = m.RemoveFromGlobalLRU(instance); err != nil {
			return
		}
		res.decision = REUSE
		res.instance = instance
		return
	}
	// make sure we can checkpoint
	if !m.Runtime.checkpointCache.Lookup(lambdaName) {
		dplogger.Warn().Str("lambda name", lambdaName).Msg("could not find checkpoint image")
		goto cold_start_routine
	}

	// try to switch from other containers
	// TODO(huang-jl) It is possible that instance here (poped from global lru)
	// maybe a reuse case for lambdaName
	instance = m.PopFromGlobalLRU()
	if instance != nil {
		if !instance.status.Valid() {
			err = fmt.Errorf("detect invalid status instance: %+v", instance)
			return
		}
		// TODO(huang-jl) it is possible that localPQIndex maybe non-zero
		// (e.g., a container has been push back to local pq)
		if instance.localPQIndex != 0 {
			dplogger.Warn().Str("instance id", instance.GetInstanceID()).
				Int("local PQ Index", instance.localPQIndex).Msg("find weird instance when pop from global lru list")
		}
		oldPool, err = m.GetCtrPool(instance.LambdaName)
		if err != nil {
			return
		}
		oldPool.RemoveFromFree(instance)
		res.decision = SWITCH
		res.instance = instance
		res.oldPool = oldPool
		return
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
		if err = m.RemoveFromGlobalLRU(instance); err != nil {
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
		pool, _ := m.GetCtrPool(instance.LambdaName)
		pool.PushIntoFree(instance)
		m.PushIntoGlobalLRU(instance)
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

	var pool *CtrPool
	for extraSpace > 0 {
		instance := m.PopFromGlobalLRU()
		if instance == nil {
			break
		}
		pool, err = m.GetCtrPool(instance.LambdaName) // ignore error
		if err != nil {
			return
		}
		pool.RemoveFromFree(instance)
		m.memBound.RemoveCtr(pool.memoryRequirement)
		extraSpace -= pool.memoryRequirement
		res = append(res, instance)
	}

	if extraSpace > 0 {
		err = ErrMemoryNotEnough
	}
	return
}
