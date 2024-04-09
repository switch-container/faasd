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
	case CR_START:
		return "criu"
	case CR_LAZY_START:
		return "lazy"
	case REUSE:
		return "reuse"
	case SWITCH:
		return "switch"
	case FAASNAP_START:
		return "faasnap"
  case REAP_START:
		return "reap"
	default:
		return "invalid decision"
	}
}

const (
	COLD_START DeployDecision = iota
	CR_START
	CR_LAZY_START
	FAASNAP_START
	REAP_START
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
	// NOTE by huang-jl: this field point to ctr pool of service
	// that need to be depolyed (mabye not the same as `instance`)
	oldPool    *CtrPool
	targetPool *CtrPool
}

func (d DeployResult) applyMemUsage(m *LambdaManager) {
	switch d.decision {
	case COLD_START, CR_LAZY_START, CR_START, FAASNAP_START, REAP_START:
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
	Decide(m *LambdaManager, serviceName string) (DeployResult, error)
}

type NaiveSwitchPolicy struct {
	noReuse bool
}

func NewNaiveSwitchPolicy(noReuse bool) NaiveSwitchPolicy {
	return NaiveSwitchPolicy{noReuse}
}

// Note by huang-jl: Note that enable noReuse for switch policy may produce some bugs.
// For example, we might switch from Instance A to Instance B, while they are of same
// function type.
// So please make sure you know what you are doing before enable NoReuse option.
func (p NaiveSwitchPolicy) Decide(m *LambdaManager, serviceName string) (res DeployResult, err error) {
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

	targetPool, err = m.GetCtrPool(serviceName)
	if err != nil {
		return
	}
	res.targetPool = targetPool
	if p.noReuse {
		goto skip_reuse
	}

	// first try to fetch from function pool (i.e., reuse)
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

skip_reuse:
	// make sure we can checkpoint
	if !m.Runtime.checkpointCache.Lookup(serviceName) {
		dplogger.Warn().Str("lambda name", serviceName).Msg("could not find checkpoint image")
		goto cold_start_routine
	}

	// try to switch from other containers
	// TODO(huang-jl) It is possible that instance here (poped from global lru)
	// maybe a reuse case for serviceName
	instance = m.PopFromGlobalLRU()
	if instance != nil {
		if !instance.status.Valid() {
			err = fmt.Errorf("detect invalid status instance: %+v", instance)
			return
		}
		// TODO(huang-jl) it is possible that localPQIndex maybe non-zero
		// (e.g., another container just finish invocation and been push back to local pq)
		if instance.localPQIndex != 0 {
			dplogger.Warn().Str("instance id", instance.GetInstanceID()).
				Int("local PQ Index", instance.localPQIndex).Msg("find weird instance when pop from global lru list")
		}
		oldPool, err = m.GetCtrPool(instance.ServiceName)
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
		err = errors.Wrapf(err, "findKillingInstanceFor %s (cold start) failed", serviceName)
		return
	}
	res.killInstances = killInstances
	return
}

type BaselinePolicy struct {
	defaultDecision DeployDecision
	noReuse         bool
}

func NewBaselinePolicy(defaultStartMethod string, noReuse bool) (BaselinePolicy, error) {
	var decision DeployDecision
	switch defaultStartMethod {
	case "cold":
		decision = COLD_START
	case "criu":
		decision = CR_START
	case "lazy":
		decision = CR_LAZY_START
	case "faasnap":
		decision = FAASNAP_START
	case "reap":
		decision = REAP_START
	default:
		return BaselinePolicy{}, errors.Errorf("invalid default start method for baseline: %v", defaultStartMethod)
	}
	return BaselinePolicy{decision, noReuse}, nil
}

func (p BaselinePolicy) Decide(m *LambdaManager, serviceName string) (res DeployResult, err error) {
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

	pool, err = m.GetCtrPool(serviceName)
	if err != nil {
		return
	}
	res.targetPool = pool

	if p.noReuse {
		goto skip_reuse
	}
	// first try to fetch from function pool (i.e., reuse)
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
skip_reuse:
	// if we cannot reuse, then start new container
	res.decision = p.defaultDecision
	killInstances, err = m.findKillingInstanceFor(pool.memoryRequirement)
	if err != nil {
		err = errors.Wrapf(err, "findKillingInstanceFor %s failed", serviceName)
		return
	}
	res.killInstances = killInstances
	return
}

func (m *LambdaManager) pushBackKillingInstances(arr []*CtrInstance) {
	for _, instance := range arr {
		pool, _ := m.GetCtrPool(instance.ServiceName)
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
		pool, err = m.GetCtrPool(instance.ServiceName) // ignore error
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
