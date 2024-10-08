package provider

import (
	"container/heap"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/containerd/containerd"
	gocni "github.com/containerd/go-cni"
	"github.com/openfaas/faas-provider/types"
	"github.com/openfaas/faasd/pkg"
	"github.com/openfaas/faasd/pkg/provider/config"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
)

var lmlogger = log.With().
	Str("component", "[LambdaManager]").
	Logger()

// Note by huang-jl: In our workload,
// after adding a lambda, it will not be deleted
type LambdaManager struct {
	pools    map[string]*CtrPool
	services []string
	mu       sync.RWMutex

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

// memBound: the memory bound in bytes
func NewLambdaManager(client *containerd.Client, cni gocni.CNI, policy DeployPolicy,
	config *config.ProviderConfig) (*LambdaManager, error) {
	rootfsManager, err := NewRootfsManager()
	if err != nil {
		return nil, err
	}
	checkpointCache := NewCheckpointCache()
	m := &LambdaManager{
		pools:     map[string]*CtrPool{},
		policy:    policy,
		Runtime:   NewCtrRuntime(client, cni, rootfsManager, checkpointCache, config),
		terminate: false,
		cleanup:   []func(*LambdaManager){killAllInstances},
		memBound:  NewMemoryBound(config.MemBound),
		lru:       NewGloablFreePQ(),
	}
	m.registerCleanup(func(lm *LambdaManager) {
		close(lm.Runtime.reapCh)
		close(lm.Runtime.workerCh)
		close(lm.Runtime.killCh)
	})
	// make sure the following dir exist
	for _, dir := range []string{
		pkg.FaasdCheckpointDirPrefix,
		pkg.FaasdCRIUCheckpointWorkPrefix,
		pkg.FaasdCRIUResotreWorkPrefix,
		pkg.FaasdPackageDirPrefix,
		pkg.FaasdAppWorkDirPrefix,
		pkg.FaasdAppUpperDirPrefix,
		pkg.FaasdAppMergeDirPrefix,
	} {
		if err := ensureDirExist(dir); err != nil {
			return m, nil
		}
	}
	return m, nil
}

func (m *LambdaManager) RegisterService(req types.FunctionDeployment) error {
	serviceName := req.Service

	if p, ok := m.policy.(BaselinePolicy); ok && (p.defaultDecision == FAASNAP_START || p.defaultDecision == REAP_START) {
		// load snapshotids from local file
		var filePath string
		if p.defaultDecision == FAASNAP_START {
			filePath = pkg.FaasnapSnapshotIdFile
		} else {
			filePath = pkg.ReapSnapshotIdFile
		}
		file, err := os.Open(filePath)
		if err != nil {
			return errors.Wrap(err, "failed to open snapshot file")
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			return errors.Wrap(err, "failed to read snapshot file")
		}
		var jsonData map[string]interface{}
		err = json.Unmarshal(data, &jsonData)
		if err != nil {
			return errors.Wrap(err, "failed to unmarshal snapshot file")
		}
		snapshotFuncName := strings.SplitN(serviceName, "_", 2)[0]
		snapshotId, ok := jsonData[snapshotFuncName]
		if !ok {
			return errors.Errorf("snapshot not found for service %s (%s)", serviceName, snapshotFuncName)
		}
		req.SnapshotIds = []string{snapshotId.(string)}
	}

	// first register for app overlay cache
	if err := m.Runtime.rootfsManager.RegisterService(serviceName); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.pools[serviceName]; !ok {
		pool, err := NewCtrPool(serviceName, req)
		if err != nil {
			return err
		}
		m.pools[serviceName] = pool
		m.services = append(m.services, serviceName)
		lmlogger.Info().Str("service name", serviceName).Int64("memory req", pool.memoryRequirement).
			Msg("registering new service")
	}
	return nil
}

func (m *LambdaManager) GetCtrPool(serviceName string) (*CtrPool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.GetCtrPoolWOLock(serviceName)
}

func (m *LambdaManager) GetCtrPoolWOLock(serviceName string) (*CtrPool, error) {
	pool, ok := m.pools[serviceName]
	if ok {
		return pool, nil
	} else {
		return nil, errors.Wrapf(ErrNotFoundService, "service %s", serviceName)
	}
}

func (m *LambdaManager) killInstancesForDepoly(instances []*CtrInstance) {
	for _, instance := range instances {
		m.Runtime.KillInstanceAsync(instance, true)
	}
}

// A couple of unified entrypoints for operating on containers.
// So that we could record needed information here.
func (m *LambdaManager) StartNewCtr(depolyRes DeployResult, id uint64) (*CtrInstance, error) {
	pool := depolyRes.targetPool
	instance, err := m.Runtime.StartNewCtr(pool.requirement, id, depolyRes.decision)
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
		lmlogger.Debug().Int64("mem left", m.memBound.Left()).Str("service name", pool.serviceName).
			Uint64("id", id).Msg("switch ctr succeed")
	}
	return instance, err
}

// This is the core method in LambdaManager
//
// For a new invocation, we need make a CtrInstance for it.
// Whether this CtrInstance from REUSE, SWITCH or COLD_START
// is the detail hidden by this method.
func (m *LambdaManager) MakeCtrInstanceFor(ctx context.Context, serviceName string) (*CtrInstance, error) {
	var (
		depolyRes DeployResult
		err       error
		id        uint64
	)
restart:
	depolyRes, err = m.policy.Decide(m, serviceName)
	if err != nil {
		return nil, err
	}
	if depolyRes.targetPool.serviceName != serviceName {
		return nil, fmt.Errorf("Cold start find pool of %s for %s",
			depolyRes.targetPool.serviceName, serviceName)
	}

	switch depolyRes.decision {
	case REUSE:
		lmlogger.Debug().Str("service", serviceName).Uint64("id", depolyRes.instance.ID).
			Msg("policy decide to reuse ctr")
		depolyRes.instance.depolyDecision = REUSE
		return depolyRes.instance, nil
	case SWITCH:
		if id == 0 {
			id = depolyRes.targetPool.idAllocator.Add(1)
		}
		lmlogger.Debug().Str("new service", serviceName).Uint64("new id", id).
			Str("old service", depolyRes.instance.ServiceName).
			Uint64("old id", depolyRes.instance.ID).Msg("policy decide to switch ctr")
		return m.SwitchStart(depolyRes, id)
	default: // COLD_START, CR_START, CR_LAZY_START, FAASNAP_START, REAP_START
		if id == 0 {
			id = depolyRes.targetPool.idAllocator.Add(1)
		}
		killIDs := depolyRes.getKillInstanceIDs()
		lmlogger.Debug().Strs("kill instances", killIDs).Str("service name", serviceName).Uint64("id", id).
			Int64("mem left", m.memBound.Left()).Str("decision", depolyRes.decision.String()).
			Msg("policy decide to start new instances")
		instance, err := m.StartNewCtr(depolyRes, id)
		if errors.Is(err, ErrColdStartTooMuch) {
			// NOTE by huang-jl: if we need retry depoly, we have to push killing instance back
			lmlogger.Debug().Strs("kill instances", killIDs).Str("service name", serviceName).Uint64("id", id).
				Msg("cold start too much, pushing back killing instances and retry")
			m.pushBackKillingInstances(depolyRes.killInstances)
			// NOTE by huang-jl: only COLD_START can retry
			select {
			case <-ctx.Done():
				return nil, errors.Wrapf(ctx.Err(), "timeout MakeCtrInstanceFor %s spent more than 30 seconds", serviceName)
			case <-time.After(50 * time.Millisecond):
				goto restart
			}
		}
		if err == nil {
			lmlogger.Debug().Str("service", serviceName).Uint64("id", id).Strs("kill instances", killIDs).
				Int64("mem left", m.memBound.Left()).Msg("start new instance succeed!")
		}
		return instance, err
	}
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
	lmlogger.Warn().Msg("Start killing all instances of LambdaManager...")

	for _, pool := range m.pools {
		pool.mu.Lock()
		for _, ctrInstance := range pool.free {
			if ctrInstance.status.Valid() {
				m.memBound.RemoveCtr(pool.memoryRequirement)
				m.Runtime.KillInstanceSync(ctrInstance)
			}
		}
		pool.free = NewCtrFreePQ()

		for _, ctrInstance := range pool.busy {
			if ctrInstance.status.Valid() {
				m.memBound.RemoveCtr(pool.memoryRequirement)
				m.Runtime.KillInstanceSync(ctrInstance)
			}
		}
		pool.busy = make(map[uint64]*CtrInstance)
		pool.mu.Unlock()
	}
	m.lruMu.Lock()
	m.lru = NewGloablFreePQ()
	m.lruMu.Unlock()
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
		if tmp.ID != instance.ID || tmp.ServiceName != instance.ServiceName {
			return fmt.Errorf("corrupt global lru list detect for instance %s", instance.GetInstanceID())
		}
	}
	return nil
}
func ensureDirExist(folder string) error {
	if _, err := os.Stat(folder); err != nil {
		err = os.MkdirAll(folder, 0644)
		if err != nil {
			return err
		}
	}
	return nil
}
