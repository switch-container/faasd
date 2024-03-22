package faasnap

import (
	"container/list"
	"context"
	"fmt"
	"sync"

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

var networksUsed list.List
var networksFree list.List
var networkLock sync.Mutex

func GetFreeNetwork(ctx context.Context) (string, *list.Element, error) {
	client := swagger.NewAPIClient(swagger.NewConfiguration())

	var namespace string
	var namespaceElementPtr *list.Element
	networkLock.Lock()
	defer networkLock.Unlock()
	freeNetworkCount := networksFree.Len()
	usedNetworkCount := networksUsed.Len()
	if freeNetworkCount == 0 {
		// no free network, check if the total number of networks is less than 100
		totalNetworkCount := freeNetworkCount + usedNetworkCount
		if totalNetworkCount >= 100 {
			return "", nil, errors.New("no free api network available")
		}
		// still have available network, create a new one
		namespace = fmt.Sprintf("fc%d", totalNetworkCount+1)
		newInterface := swagger.NetifacesNamespaceBody{
			HostDevName: "vmtap0",
			IfaceId:     "eth0",
			GuestMac:    "AA:FC:00:00:00:01", // fixed MAC
			GuestAddr:   "172.16.0.2",        // fixed IP
			UniqueAddr:  fmt.Sprintf("192.168.0.%d", totalNetworkCount+3),
		}
		_, err := client.DefaultApi.NetIfacesNamespacePut(ctx, namespace, &swagger.DefaultApiNetIfacesNamespacePutOpts{
			Body: optional.NewInterface(newInterface),
		})
		if err != nil {
			return "", nil, errors.Errorf("failed to create new network: %v", err)
		}
		networksUsed.PushBack(namespace)
		namespaceElementPtr = networksUsed.Back()
	} else {
		// reuse a free network
		e := networksFree.Front()
		namespace = e.Value.(string)
		networksFree.Remove(e)
		networksUsed.PushBack(namespace)
		namespaceElementPtr = networksUsed.Back()
	}

	return namespace, namespaceElementPtr, nil
}

func ReleaseNetwork(namespace *list.Element) {
	networkLock.Lock()
	networksFree.PushBack(namespace)
	networksUsed.Remove(namespace)
	networkLock.Unlock()
}
