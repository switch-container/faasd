package faasnap

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/antihax/optional"
	"github.com/pkg/errors"
	"github.com/switch-container/faasd/pkg/provider/faasnap/api/swagger"
)

/*
For Faasnap,
We manage two lists which hold used and free networks respectively for network reuse.
When we call `CtrRuntime.faasnapStartInstance()`, we will pop a network from the free list and push it into the used list.
If there is no free network, we will create a new one and push it into the used list.
When we call `CtrRuntime.Kill()`, we will pop the network from the used list and push it into the free list.
*/

type FaasnapNetworkManager struct {
	// used to communicate with faasnap daemon
	client      *swagger.APIClient
	idAllocator atomic.Uint64
	mu          sync.Mutex
	netPool     []string
}

func NewFaasnapNetworkManager() *FaasnapNetworkManager {
	client := swagger.NewAPIClient(swagger.NewConfiguration())
	fnm := &FaasnapNetworkManager{
		client:  client,
		netPool: make([]string, 0),
	}
	// start from 21
	fnm.idAllocator.Store(20)
	return fnm
}

// return the network namesapce identifier and error
func (fnm *FaasnapNetworkManager) createNetwork(ctx context.Context) (string, error) {
	id := fnm.idAllocator.Add(1)
	cmd := exec.Command("/var/lib/faasd/network.sh", strconv.FormatUint(id, 10))
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return "", err
	}
	ns := fmt.Sprintf("fc%d", id)
	newInterface := swagger.NetifacesNamespaceBody{
		HostDevName: "vmtap0",
		IfaceId:     "eth0",
		GuestMac:    "AA:FC:00:00:00:01", // fixed MAC
		GuestAddr:   "172.16.0.2",        // fixed IP
		UniqueAddr:  fmt.Sprintf("192.168.0.%d", id+2),
	}
	_, err := fnm.client.DefaultApi.NetIfacesNamespacePut(ctx, ns, &swagger.DefaultApiNetIfacesNamespacePutOpts{
		Body: optional.NewInterface(newInterface),
	})
	if err != nil {
		return "", errors.Errorf("failed to create new network: %v", err)
	}
	return ns, nil
}

// return value:
// the string: the namespace name
// the bool: whether create a new net ns or not (i.e., reuse)
// error
func (fnm *FaasnapNetworkManager) GetNetwork(ctx context.Context) (string, bool, error) {
	fnm.mu.Lock()
	if len(fnm.netPool) > 0 {
		netNs := fnm.netPool[0]
		fnm.netPool = fnm.netPool[1:]
		fnm.mu.Unlock()
		return netNs, false, nil
	}
	// no reusable net namespace, then creat a new one
	// however, we need drop the lock while create network
	fnm.mu.Unlock()
	netNs, err := fnm.createNetwork(ctx)
  return netNs, true, err
}

func (fnm *FaasnapNetworkManager) PutNetwork(ns string) {
	fnm.mu.Lock()
	defer fnm.mu.Unlock()
	fnm.netPool = append(fnm.netPool, ns)
}
