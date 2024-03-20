package faasnap

import (
	"container/list"
	"sync"
)

/*
For Faasnap,
We manage two lists which hold used and free networks respectively for network reuse.
When we call `CtrRuntime.faasnapStartInstance()`, we will pop a network from the free list and push it into the used list.
If there is no free network, we will create a new one and push it into the used list.
When we call `CtrRuntime.Kill()`, we will pop the network from the used list and push it into the free list.
*/

var NetworksUsed list.List
var NetworksFree list.List
var NetworkLock sync.Mutex
