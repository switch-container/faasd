package provider

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/containerd/containerd"
	gocni "github.com/containerd/go-cni"
	"github.com/openfaas/faas-provider/types"
	"github.com/pkg/errors"
)

// Note by huang-jl: In our workload,
// after adding a lambda, it will not be deleted
type LambdaManager struct {
	pools     map[string]*CtrPool
	lambdas   []string
	mu        sync.RWMutex
	policy    DeployPolicy
	Runtime   CtrRuntime
	terminate bool
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

var ErrNotFoundLambda = errors.New("[LambdaManager] do not found")

func NewLambdaManager(client *containerd.Client, cni gocni.CNI, policy DeployPolicy) (*LambdaManager, error) {
	rootfsManager, err := NewRootfsManager()
	if err != nil {
		return nil, err
	}
	checkpointCache := NewCheckpointCache()
	m := &LambdaManager{
		pools:  map[string]*CtrPool{},
		policy: policy,
		// TODO(huang-jl) adjust the concurrency of cold start
		Runtime:   NewCtrRuntime(client, cni, rootfsManager, checkpointCache, false, 10),
		terminate: false,
	}
	return m, nil
}

func (m *LambdaManager) RegisterLambda(req types.FunctionDeployment) {
	lambdaName := req.Service
	log.Printf("[LambdaManager] registering lambda %s\n", lambdaName)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.pools[lambdaName]; !ok {
		m.pools[lambdaName] = NewCtrPool(lambdaName, req)
		m.lambdas = append(m.lambdas, lambdaName)
	}
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
	if depolyRes.pool.lambdaName != lambdaName {
		return nil, fmt.Errorf("Cold start find pool of %s for %s", depolyRes.pool.lambdaName, lambdaName)
	}

	req := depolyRes.pool.requirement
	switch depolyRes.decision {
	case COLD_START:
		if id == 0 {
			id = depolyRes.pool.idAllocator.Add(1)
		}
		instance, err := m.Runtime.ColdStart(req, id)
		if errors.Is(err, ColdStartTooMuchError) {
			// NOTE by huang-jl: only COLD_START can retry
			// since it does not modify (i.e. no side-effect) CtrPool's free or busy pool
			select {
			case <-ctx.Done():
				return nil, errors.Wrapf(ctx.Err(), "timeout MakeCtrInstanceFor %s spent more than 30 seconds", lambdaName)
			case <-time.After(50 * time.Millisecond):
				goto restart
			}
		}
		log.Printf("cold start for %s-%d", lambdaName, id)
		return instance, err
	case REUSE:
		log.Printf("reuse container %s-%d", lambdaName, depolyRes.instance.ID)
		depolyRes.instance.depolyDecision = REUSE
		return depolyRes.instance, nil
	case SWITCH:
		if id == 0 {
			id = depolyRes.pool.idAllocator.Add(1)
		}
		log.Printf("switch from old %s-%d for new %s-%d", depolyRes.instance.LambdaName, depolyRes.instance.ID, lambdaName, id)
		return m.Runtime.SwitchStart(req, id, depolyRes.instance)
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
	m.killAllInstances()
	close(m.Runtime.workerCh)
}

// clean **ALL** running instances if possible
func (m *LambdaManager) killAllInstances() {
	log.Print("Start shutdown all instances of LambdaManager...\n")

	for _, pool := range m.pools {
		for _, ctrInstance := range pool.free {
			if ctrInstance.status.Valid() {
				m.Runtime.KillInstance(ctrInstance)
			}
		}

		for _, ctrInstance := range pool.busy {
			if ctrInstance.status.Valid() {
				m.Runtime.KillInstance(ctrInstance)
			}
		}
	}
}
