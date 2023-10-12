package provider

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openfaas/faas-provider/types"
)

// This is a pool for each kind of Lambda function
type CtrPool struct {
	// two pools: free and busy
	free        []*CtrInstance
	busy        map[uint64]*CtrInstance
	mu          sync.Mutex
	idAllocator atomic.Uint64
	lambdaName  string

	requirement types.FunctionDeployment
}

func NewCtrPool(lambdaName string, req types.FunctionDeployment) *CtrPool {
	return &CtrPool{
		lambdaName:  lambdaName,
		requirement: req,
		free:        []*CtrInstance{},
		busy:        make(map[uint64]*CtrInstance),
	}
}

// return nil when do not find instance in free queue
func (pool *CtrPool) PopFromFree() *CtrInstance {
	var res *CtrInstance
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if len(pool.free) > 0 {
		res = pool.free[0]
		pool.free = pool.free[1:]
	}
	return res
}

func (pool *CtrPool) PushIntoBusy(instance *CtrInstance) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.busy[instance.ID] = instance
}

func (pool *CtrPool) PopFromBusy(id uint64) *CtrInstance {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	instance := pool.busy[id]
	delete(pool.busy, id)
	return instance
}

func (pool *CtrPool) MoveFromBusyToFree(id uint64) *CtrInstance {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	instance, ok := pool.busy[id]
	if ok {
		delete(pool.busy, id)
		pool.free = append(pool.free, instance)
	}
	return instance
}

type CtrInstance struct {
	LambdaName     string // key in LambdaManager
	ID             uint64 // key in CtrPool
	Pid            int    // init process id in Container
	status         ContainerStatus
	IpAddress      string
	rootfs         *OverlayInfo // do not need contains lowerdirs since it is large but useless for now
	cniID          string       // which used to remove network resources
	appOverlay     *OverlayInfo
	lastActive     time.Time
	depolyDecision DeployDecision // the depoly decision that created these instance
	originalCtrID  string         // the original container ID
	// e.g., pyase-5 which switch from hello-world-5, the original container ID is hello-world-5
	// the originalCtrID is used for cleanup
}

func GetInstanceID(lambdaName string, id uint64) string {
	return fmt.Sprintf("%s-%d", lambdaName, id)
}

