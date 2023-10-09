package provider

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	gocni "github.com/containerd/go-cni"
	"github.com/openfaas/faas-provider/types"
	"github.com/pkg/errors"
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
}

func GetInstanceID(lambdaName string, id uint64) string {
	return fmt.Sprintf("%s-%d", lambdaName, id)
}

func KillInstance(ctrInstance *CtrInstance, cni gocni.CNI) error {
	pid := ctrInstance.Pid
	// remove cni network first (it needs network ns)
	err := cni.Remove(context.Background(), ctrInstance.cniID,
		fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		return errors.Wrapf(err, "remove cni network for %s failed\n", ctrInstance.cniID)
	}
	// kill process
	err = syscall.Kill(pid, syscall.SIGKILL)
	if err != nil {
		return errors.Wrapf(err, "kill process %d failed\n", pid)
	}
	ctrInstance.status = INVALID
	return nil
}
