package provider

import (
	"container/heap"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openfaas/faas-provider/types"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/api/resource"
)

// This is a pool for each kind of Lambda function
type CtrPool struct {
	// two pools: free and busy
	free        CtrFreePQ
	busy        map[uint64]*CtrInstance
	mu          sync.Mutex
	idAllocator atomic.Uint64
	lambdaName  string

	requirement types.FunctionDeployment
	// How many bytes does this type of function needed
	memoryRequirement int64
}

func NewCtrPool(lambdaName string, req types.FunctionDeployment) (*CtrPool, error) {
	var memoryLimit string = "1G"
	if req.Limits != nil && len(req.Limits.Memory) > 0 {
		memoryLimit = req.Limits.Memory
	}

	qty, err := resource.ParseQuantity(memoryLimit)
	if err != nil {
		return nil, errors.Wrapf(err, "parse memory limit %s failed", memoryLimit)
	}

	return &CtrPool{
		lambdaName:        lambdaName,
		requirement:       req,
		free:              NewCtrFreePQ(),
		busy:              make(map[uint64]*CtrInstance),
		memoryRequirement: qty.Value(),
	}, nil
}

// how many free containers this pool has
func (pool *CtrPool) FreeNum() int {
	pool.mu.Lock()
	res := pool.free.Len()
	pool.mu.Unlock()
	return res
}

func (pool *CtrPool) PopFromFreeWOLock() *CtrInstance {
	var res *CtrInstance
	if pool.free.Len() > 0 {
		res = heap.Pop(&pool.free).(*CtrInstance)
	}
	return res
}

// return nil when do not find instance in free queue
func (pool *CtrPool) PopFromFree() *CtrInstance {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return pool.PopFromFreeWOLock()
}

func (pool *CtrPool) PushIntoFreeWOLock(instance *CtrInstance) {
	heap.Push(&pool.free, instance)
}

func (pool *CtrPool) PushIntoFree(instance *CtrInstance) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.PushIntoFreeWOLock(instance)
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
		pool.PushIntoFreeWOLock(instance)
	}
	return instance
}

func (pool *CtrPool) RemoveFromFree(instance *CtrInstance) error {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if instance.localPQIndex >= 0 {
		tmp := heap.Remove(&pool.free, instance.localPQIndex).(*CtrInstance)
    // double check
		if tmp.ID != instance.ID || tmp.LambdaName != instance.LambdaName {
			return fmt.Errorf("corrupt ctr free pool detect for instance %s", instance.GetInstanceID())
		}
	}
	return nil
}

type CtrInstance struct {
	LambdaName     string // key in LambdaManager
	ID             uint64 // key in CtrPool
	Pid            int    // init process id in Container
	status         ContainerStatus
	IpAddress      string
	rootfs         *OverlayInfo // do not need contains lowerdirs since it is large but useless for now
	cniID          string       // which used for removing network resources
	appOverlay     *OverlayInfo
	lastActive     time.Time
	depolyDecision DeployDecision // the depoly decision that created these instance
	originalCtrID  string         // the original container ID
	// e.g., pyase-5 which switch from hello-world-5, the original container ID is hello-world-5
	// the originalCtrID is used for removing resources in containerd 

	// This is used for global priority queue
	globalPQIndex int
	// This is used for ctr pool's local priority queue
	localPQIndex int
}

func (i *CtrInstance) GetInstanceID() string {
	return fmt.Sprintf("%s-%d", i.LambdaName, i.ID)
}

func GetInstanceID(lambdaName string, id uint64) string {
	return fmt.Sprintf("%s-%d", lambdaName, id)
}
