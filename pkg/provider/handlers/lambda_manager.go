package handlers

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"sync/atomic"
	"syscall"

	gocni "github.com/containerd/go-cni"
	"github.com/pkg/errors"
)

type LambdaManager struct {
	info map[string]*Lambda
	mu   sync.Mutex
}

type Lambda struct {
	instances   map[uint64]*ContainerInfo
	idAllocator atomic.Uint64
}

type ContainerInfo struct {
	serviceName string
	// unique id in the serviceName
	id        uint64
	pid       int
	status    ContainerStatus
	IpAddress string
	rootfs    OverlayInfo
	cniID     string
	// TODO(huang-jl) Add app overlayInfo
	appOverlay *OverlayInfo
}

type ContainerStatus int

const (
	INVALID ContainerStatus = iota
	// 1 finished for some time
	IDLE
	// 2 has been occupied
	RUNNING
	// 3 finish running
	FINISHED
	// 4 being switching, could not be takeup
	SWITCHING
)

func (status ContainerStatus) Valid() bool {
	return (status > INVALID) && (status <= SWITCHING)
}

func (status ContainerStatus) CanSwitch() bool {
	// return status == IDLE
	return status == IDLE || status == FINISHED
}

func (s ContainerStatus) String() string {
	switch s {
	case INVALID:
		return "INVALID"
	case IDLE:
		return "IDLE"
	case RUNNING:
		return "RUNNING"
	case FINISHED:
		return "FINISHED"
	case SWITCHING:
		return "SWITCHING"
	default:
		return "UNKNOWN"
	}
}

var ErrNotFoundIdleInstance = errors.New("no idle instance")
var ErrNotFoundTargetInstance = errors.New("could not find requested instance")
var ErrInstanceInvalidStatus = errors.New("invalid lambda instance status")

var lambdaManager = &LambdaManager{
	info: map[string]*Lambda{},
}

func (m *LambdaManager) AddNewInstance(serviceName string) (uint64, error) {
	newInstance := &ContainerInfo{
		serviceName: serviceName,
		pid:         -1,
		status:      INVALID,
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if lambda, ok := m.info[serviceName]; ok {
		id := lambda.idAllocator.Add(1)
		// add a new instance
		newInstance.id = id
		lambda.instances[id] = newInstance
		return id, nil
	}
	lambda := &Lambda{instances: map[uint64]*ContainerInfo{}}
	id := lambda.idAllocator.Add(1)
	newInstance.id = id
	lambda.instances[id] = newInstance
	m.info[serviceName] = lambda
	return id, nil
}

func (m *LambdaManager) RemoveInstance(serviceName string, id uint64) (*ContainerInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if lambda, ok := m.info[serviceName]; ok {
		if info, ok := lambda.instances[id]; ok {
			delete(lambda.instances, id)
			log.Printf("remove instance %+v\n", info)
			return info, nil
		}
		return nil, errors.Wrapf(ErrNotFoundTargetInstance, "remove %s - %d", serviceName, id)
	}
	return nil, errors.Wrapf(ErrNotFoundTargetInstance, "remove %s", serviceName)
}

type InstanceInfoUpdateFunc func(*ContainerInfo) error

func (m *LambdaManager) UpdateInstance(serviceName string, id uint64, updater InstanceInfoUpdateFunc) (res ContainerInfo, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if lambda, ok := m.info[serviceName]; ok {
		if instance, ok := lambda.instances[id]; ok {
			if updater != nil {
				err = updater(instance)
			}
			log.Printf("[LambdaManager] after update instance: %+v\n", instance)
			res = *instance
			return
		} else {
			err = errors.Wrapf(ErrNotFoundTargetInstance, "update %s-%d", serviceName, id)
			return
		}
	}
	err = errors.Wrapf(ErrNotFoundTargetInstance, "update %s", serviceName)
	return
}

// randomly choose an idle instance
// return its serviceName, id and error
func (m *LambdaManager) ChooseSwitchCandidate() (ContainerInfo, error) {
	var candidates []*ContainerInfo
	var res ContainerInfo
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, lambda := range m.info {
		for _, container := range lambda.instances {
			if container.status.CanSwitch() {
				candidates = append(candidates, container)
			}
		}
	}
	if len(candidates) == 0 {
		return res, ErrNotFoundIdleInstance
	}
	ctrPtr := candidates[rand.Intn(len(candidates))]
	ctrPtr.status = SWITCHING

	res = *ctrPtr

	log.Printf("[LambdaManager] choose switch candidate: %s - %d\n", res.serviceName, res.id)
	return res, nil
}

func (m *LambdaManager) ListInstances() ([]ContainerInfo, error) {
	var res []ContainerInfo
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, lambda := range m.info {
		for _, container := range lambda.instances {
			if container.status.Valid() {
				res = append(res, *container)
			}
		}
	}

	return res, nil
}

// clean **ALL** running instance if possible
func LambdaShutdown(cni gocni.CNI) {
	lambdaManager.mu.Lock()
	defer lambdaManager.mu.Unlock()
	for _, lambda := range lambdaManager.info {
		for _, container := range lambda.instances {
			if container.status.Valid() {
				pid := container.pid
				// remove cni network first (it needs network ns)
				err := cni.Remove(context.Background(), container.cniID,
					fmt.Sprintf("/proc/%d/ns/net", pid))
				if err != nil {
					log.Printf("remove cni network for %s failed: %s\n", container.cniID, err)
				}
				// kill process
				err = syscall.Kill(pid, syscall.SIGKILL)
				if err != nil {
					log.Printf("kill process %d failed: %s\n", pid, err)
				}
				container.status = INVALID
				log.Printf("kill instance %s-%d pid %d\n",
					container.serviceName, container.id, pid)
			}
		}
	}
}
