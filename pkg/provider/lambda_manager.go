package provider

import (
	"container/heap"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/containerd/containerd"
	gocni "github.com/containerd/go-cni"
	"github.com/openfaas/faas-provider/types"
	"github.com/openfaas/faasd/pkg"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
)

var lmlogger = log.With().
	Str("component", "[LambdaManager]").
	Logger()

// Note by huang-jl: In our workload,
// after adding a lambda, it will not be deleted
type LambdaManager struct {
	pools   map[string]*CtrPool
	lambdas []string
	mu      sync.RWMutex

	lru   GlobalFreePQ
	lruMu sync.Mutex

	policy    DeployPolicy
	Runtime   CtrRuntime
	terminate bool
	cleanup   []func(*LambdaManager)

	memBound MemoryBound
}

type ContainerStatus string

const (
	INVALID ContainerStatus = ""
	// 1 finished for some time
	IDLE ContainerStatus = "IDLE"
	// 2 has been occupied
	RUNNING ContainerStatus = "RUNNING"
	// 3 finish running
	FINISHED ContainerStatus = "FINISHED"
	// 4 being switching, could not be takeup
	SWITCHING ContainerStatus = "SWITCHING"
)

func (status ContainerStatus) Valid() bool {
	return status != INVALID
}

func (status ContainerStatus) CanSwitch() bool {
	return status == IDLE || status == FINISHED
}

func NewLambdaManager(client *containerd.Client, cni gocni.CNI, policy DeployPolicy) (*LambdaManager, error) {
	rootfsManager, err := NewRootfsManager()
	if err != nil {
		return nil, err
	}
	checkpointCache := NewCheckpointCache()
	m := &LambdaManager{
		pools:  map[string]*CtrPool{},
		policy: policy,
		Runtime: NewCtrRuntime(client, cni, rootfsManager, checkpointCache,
			false, pkg.ColdStartConcurrencyLimit),
		terminate: false,
		cleanup:   []func(*LambdaManager){killAllInstances},
		memBound:  NewMemoryBound(pkg.MemoryBound),
	}
	m.registerCleanup(func(lm *LambdaManager) {
		close(lm.Runtime.reapCh)
		close(lm.Runtime.workerCh)
	})
	return m, nil
}

func (m *LambdaManager) RegisterLambda(req types.FunctionDeployment) error {
	lambdaName := req.Service
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.pools[lambdaName]; !ok {
		pool, err := NewCtrPool(lambdaName, req)
		if err != nil {
			return err
		}
		m.pools[lambdaName] = pool
		m.lambdas = append(m.lambdas, lambdaName)
		lmlogger.Info().Str("lambda name", lambdaName).Int64("memory req", pool.memoryRequirement).
			Msg("registering new lambda")
	}
	return nil
}

func (m *LambdaManager) GetCtrPool(lambdaName string) (*CtrPool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.GetCtrPoolWOLock(lambdaName)
}

func (m *LambdaManager) GetCtrPoolWOLock(lambdaName string) (*CtrPool, error) {
	pool, ok := m.pools[lambdaName]
	if ok {
		return pool, nil
	} else {
		return nil, errors.Wrapf(ErrNotFoundLambda, "lambda %s", lambdaName)
	}
}

func (m *LambdaManager) killInstancesForDepoly(instances []*CtrInstance) {
	if len(instances) == 0 {
		return
	}
	// start kill instances if necessary
	var wg sync.WaitGroup
	// min(4, len(depolyRes.killInstances))
	concurrency := pkg.KillInstancesConcurrencyLimit
	if concurrency > len(instances) {
		concurrency = len(instances)
	}
	killCh := make(chan *CtrInstance, concurrency)
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			for instance := range killCh {
				instanceID := instance.GetInstanceID()
				if err := m.KillInstance(instance); err != nil {
					lmlogger.Error().Err(err).Str("instance id", instanceID).
						Msg("killInstancesForDepoly failed")
				}
			}
		}()
	}
	for _, instance := range instances {
		killCh <- instance
	}
	close(killCh)
	wg.Wait()
}

// A couple of unified entrypoints for operating on containers.
// So that we could record needed information here.
func (m *LambdaManager) ColdStart(depolyRes DeployResult, id uint64) (*CtrInstance, error) {
	pool := depolyRes.targetPool
	instance, err := m.Runtime.ColdStart(pool.requirement, id)
	if err == nil {
		m.killInstancesForDepoly(depolyRes.killInstances)
	} else {
		// if we meet error, we need free memory usage
		m.memBound.RemoveCtr(pool.memoryRequirement)
	}
	return instance, err
}

func (m *LambdaManager) SwitchStart(depolyRes DeployResult, id uint64) (*CtrInstance, error) {
	pool := depolyRes.targetPool
	instance, err := m.Runtime.SwitchStart(pool.requirement, id, depolyRes.instance)
	if err == nil {
		lmlogger.Debug().Int64("memory left", m.memBound.Left()).Str("lambda name", pool.lambdaName).
			Uint64("id", id).Msg("switch ctr succeed")
	}
	return instance, err
}

func (m *LambdaManager) KillInstance(instance *CtrInstance) error {
	if err := m.Runtime.KillInstance(instance); err != nil {
		return errors.Wrapf(err, "KillInstance %s-%d failed", instance.LambdaName, instance.ID)
	}
	return nil
}

// This is the core method in LambdaManager
//
// For a new invocation, we need make a CtrInstance for it.
// Whether this CtrInstance from REUSE, SWITCH or COLD_START
// is the detail hidden by this method.
func (m *LambdaManager) MakeCtrInstanceFor(ctx context.Context, lambdaName string) (*CtrInstance, error) {
	var (
		depolyRes DeployResult
		err       error
		id        uint64
	)
restart:
	depolyRes, err = m.policy.Decide(m, lambdaName)
	if err != nil {
		return nil, err
	}
	if depolyRes.targetPool.lambdaName != lambdaName {
		return nil, fmt.Errorf("Cold start find pool of %s for %s",
			depolyRes.targetPool.lambdaName, lambdaName)
	}

	switch depolyRes.decision {
	case COLD_START:
		if id == 0 {
			id = depolyRes.targetPool.idAllocator.Add(1)
		}
		killIDs := depolyRes.getKillInstanceIDs()
		if len(killIDs) > 0 {
			// TODO(huang-jl) remove this
			lmlogger.Debug().Strs("kill instances", killIDs).Str("lambda name", lambdaName).Uint64("id", id).
				Int64("mem left", m.memBound.Left()).Msg("policy decide to cold start instances and kill for space!")
		} else {
			lmlogger.Debug().Str("lambda name", lambdaName).Uint64("id", id).
				Int64("mem left", m.memBound.Left()).Msg("policy decide to cold start instances w/o kill")
		}
		instance, err := m.ColdStart(depolyRes, id)
		if errors.Is(err, ErrColdStartTooMuch) {
			// NOTE by huang-jl: if we need retry depoly, we have to push killing instance back
			lmlogger.Debug().Strs("kill instances", killIDs).Str("instance id", GetInstanceID(lambdaName, id)).
				Msg("cold start too much, pushing back killing instance and retry")
			m.pushBackKillingInstances(depolyRes.killInstances)
			// NOTE by huang-jl: only COLD_START can retry
			select {
			case <-ctx.Done():
				return nil, errors.Wrapf(ctx.Err(), "timeout MakeCtrInstanceFor %s spent more than 30 seconds", lambdaName)
			case <-time.After(50 * time.Millisecond):
				goto restart
			}
		}
		lmlogger.Debug().Str("lambda", lambdaName).Uint64("id", id).Strs("kill instances", killIDs).
			Int64("memory left", m.memBound.Left()).Msg("cold start succeed!")
		return instance, err
	case REUSE:
		lmlogger.Debug().Str("lambda", lambdaName).Uint64("id", depolyRes.instance.ID).
			Msg("policy decide to reuse ctr")
		depolyRes.instance.depolyDecision = REUSE
		return depolyRes.instance, nil
	case SWITCH:
		if id == 0 {
			id = depolyRes.targetPool.idAllocator.Add(1)
		}
		lmlogger.Debug().Str("new lambda", lambdaName).Uint64("new id", id).
			Str("old lambda", depolyRes.instance.LambdaName).
			Uint64("old id", depolyRes.instance.ID).Msg("policy decide to switch ctr")
		return m.SwitchStart(depolyRes, id)
	}
	return nil, fmt.Errorf("unknown decision: %+v", depolyRes.decision)
}

func (m *LambdaManager) ListInstances() ([]*CtrInstance, error) {
	var res []*CtrInstance
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, pool := range m.pools {
		for _, ctrInstance := range pool.free {
			res = append(res, ctrInstance)
		}
		for _, ctrInstance := range pool.busy {
			res = append(res, ctrInstance)
		}
	}
	return res, nil
}

func (m *LambdaManager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.terminate = true
	for _, f := range m.cleanup {
		f(m)
	}
}

// clean **ALL** running instances if possible
func killAllInstances(m *LambdaManager) {
	lmlogger.Info().Msg("Start shutdown all instances of LambdaManager...")

	for _, pool := range m.pools {
		pool.mu.Lock()
		for _, ctrInstance := range pool.free {
			if ctrInstance.status.Valid() {
				m.memBound.RemoveCtr(pool.memoryRequirement)
				m.KillInstance(ctrInstance)
			}
		}

		for _, ctrInstance := range pool.busy {
			if ctrInstance.status.Valid() {
				m.memBound.RemoveCtr(pool.memoryRequirement)
				m.KillInstance(ctrInstance)
			}
		}
		pool.mu.Unlock()
	}
}

func (m *LambdaManager) registerCleanup(cleanupFn func(*LambdaManager)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanup = append(m.cleanup, cleanupFn)
}

func (m *LambdaManager) PushIntoGlobalLRU(instance *CtrInstance) {
	m.lruMu.Lock()
	defer m.lruMu.Unlock()
	heap.Push(&m.lru, instance)
}

func (m *LambdaManager) PopFromGlobalLRU() *CtrInstance {
	var res *CtrInstance
	m.lruMu.Lock()
	defer m.lruMu.Unlock()
	if m.lru.Len() > 0 {
		res = heap.Pop(&m.lru).(*CtrInstance)
	}
	return res
}

func (m *LambdaManager) RemoveFromGlobalLRU(instance *CtrInstance) error {
	m.lruMu.Lock()
	defer m.lruMu.Unlock()
	if instance.globalPQIndex >= 0 {
		tmp := heap.Remove(&m.lru, instance.globalPQIndex).(*CtrInstance)
		// double check
		if tmp.ID != instance.ID || tmp.LambdaName != instance.LambdaName {
			return fmt.Errorf("corrupt global lru list detect for instance %s", instance.GetInstanceID())
		}
	}
	return nil
}
