package provider

import (
	"container/heap"
	"container/list"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/go-cni"
	"github.com/openfaas/faas-provider/types"
	"github.com/openfaas/faasd/pkg"
	"github.com/openfaas/faasd/pkg/metrics"
	"github.com/openfaas/faasd/pkg/provider/switcher"
	"github.com/openfaas/faasd/pkg/service"
	"github.com/pkg/errors"
	"github.com/switch-container/faasd/pkg/provider/faasnap"
	"github.com/switch-container/faasd/pkg/provider/faasnap/api/swagger"
	"k8s.io/apimachinery/pkg/api/resource"
)

// This is a pool for each kind of Lambda function
type CtrPool struct {
	// two pools: free and busy
	free        CtrFreePQ
	busy        map[uint64]*CtrInstance
	mu          sync.Mutex
	idAllocator atomic.Uint64
	serviceName string

	requirement types.FunctionDeployment
	// How many bytes does this type of function needed
	memoryRequirement int64
}

func NewCtrPool(serviceName string, req types.FunctionDeployment) (*CtrPool, error) {
	var memoryLimit string = "1G"
	if req.Limits != nil && len(req.Limits.Memory) > 0 {
		memoryLimit = req.Limits.Memory
	}

	qty, err := resource.ParseQuantity(memoryLimit)
	if err != nil {
		return nil, errors.Wrapf(err, "parse memory limit %s failed", memoryLimit)
	}

	return &CtrPool{
		serviceName:       serviceName,
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
		if tmp.ID != instance.ID || tmp.ServiceName != instance.ServiceName {
			return fmt.Errorf("corrupt ctr free pool detect for instance %s", instance.GetInstanceID())
		}
	}
	return nil
}

type Ctr interface {
	GetPid() int
	GetIpAddress() string
	Kill() error
	Switch(config switcher.SwitcherConfig) error
}

type ContainerdCtr struct {
	Pid           int // init process id in Container
	IpAddress     string
	rootfs        *OverlayInfo    // do not need contains lowerdirs since it is large but useless for now
	cniID         string          // which used for removing network resources
	appOverlay    *AppOverlayInfo // only switched container have appOverlay
	originalCtrID string          // the original container ID
	// e.g., pyase-5 which switch from hello-world-5, the original container ID is hello-world-5
	// the originalCtrID is used for removing resources in containerd

	fsManager *RootfsManager
	cni       cni.CNI
	client    *containerd.Client
}

func (ctr *ContainerdCtr) GetPid() int {
	return ctr.Pid
}

func (ctr *ContainerdCtr) GetIpAddress() string {
	return ctr.IpAddress
}

func (ctr *ContainerdCtr) Kill() error {
	pid := ctr.Pid
	// remove cni network first (it needs network ns)
	err := ctr.cni.Remove(context.Background(), ctr.cniID,
		fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		return errors.Wrapf(err, "remove cni network for %s failed\n", ctr.cniID)
	}
	// kill process
	err = syscall.Kill(pid, syscall.SIGKILL)
	if err != nil {
		return errors.Wrapf(err, "kill process %d failed\n", pid)
	}
	// umount app overlay (so that remove ctr from container will succeed)
	ctr.fsManager.recyleAppOverlay(ctr)
	// remove from containerd
	ctx := namespaces.WithNamespace(context.Background(), pkg.DefaultFunctionNamespace)
	if err = service.Remove(ctx, ctr.client, ctr.originalCtrID); err != nil {
		return err
	}
	return nil
}

func (ctr *ContainerdCtr) Switch(config switcher.SwitcherConfig) error {
	var (
		err        error
		appOverlay *AppOverlayInfo
	)
	start := time.Now()
	appOverlay, err = ctr.fsManager.PrepareSwitchRootfs(config.TargetServiceName, ctr)
	if err != nil {
		return err
	}
	if err = metrics.GetMetricLogger().Emit(pkg.PrepareSwitchFSLatency,
		ServiceName2LambdaName(config.TargetServiceName), time.Since(start)); err != nil {
		crlogger.Error().Err(err).Msg("emit PrepareSwitchFSLatency metric failed")
	}

	start = time.Now()
	switcher, err := switcher.SwitchFor(config)
	crlogger.Debug().Str("service name", config.TargetServiceName).
		Dur("overhead", time.Since(start)).Msg("SwitchFor")
	if err != nil {
		return err
	}
	newPid := switcher.PID()
	if newPid <= 0 {
		return fmt.Errorf("switchDeploy get wierd process id %d", newPid)
	}

	if appOverlay == nil {
		return fmt.Errorf("appOverlay is null when switch deploy!\n")
	}
	// update info
	ctr.Pid = newPid
	ctr.appOverlay = appOverlay
	return nil
}

type FaasnapCtr struct {
	vmId      string
	Pid       int
	IpAddress string
	network   *list.Element
}

func (ctr *FaasnapCtr) GetPid() int {
	return ctr.Pid
}

func (ctr *FaasnapCtr) GetIpAddress() string {
	return ctr.IpAddress
}

func (ctr *FaasnapCtr) Kill() error {
	client := swagger.NewAPIClient(swagger.NewConfiguration())
	_, err := client.DefaultApi.VmsVmIdDelete(context.Background(), ctr.vmId)
	if err != nil {
		return err
	}
	faasnap.ReleaseNetwork(ctr.network)
	return nil
}

func (ctr *FaasnapCtr) Switch(config switcher.SwitcherConfig) error {
	return errors.New("not implemented")
}

type CtrInstance struct {
	Ctr
	ServiceName    string // key in LambdaManager
	ID             uint64 // key in CtrPool
	status         ContainerStatus
	lastActive     time.Time
	depolyDecision DeployDecision // the depoly decision that created these instance

	// This is used for global priority queue
	globalPQIndex int
	// This is used for ctr pool's local priority queue
	localPQIndex int
}

func (i *CtrInstance) GetInstanceID() string {
	return fmt.Sprintf("%s-%d", i.ServiceName, i.ID)
}

func GetInstanceID(serviceName string, id uint64) string {
	return fmt.Sprintf("%s-%d", serviceName, id)
}
