package provider

import (
	"log"
	"time"

	"github.com/openfaas/faas-provider/types"
	"github.com/pkg/errors"
)

// Background Task is an abstraction for add cyclical task
// in faasd system.
// For now, the main use case is container gc for baselines

// each task will be running is seperate goroutine
type BackgroundTask interface {
	Run(m *LambdaManager)
}

type InstanceGCBackgroundTask struct {
	interval    time.Duration
	gcCriterion time.Duration
}

func NewInstanceGCBackgroundTask(interval, criterion time.Duration) InstanceGCBackgroundTask {
	return InstanceGCBackgroundTask{interval: interval, gcCriterion: criterion}
}

func (t InstanceGCBackgroundTask) Run(m *LambdaManager) {
	for {
		var toBeGC []*CtrInstance
		time.Sleep(t.interval)
		m.mu.RLock()
		if m.terminate {
			m.mu.RUnlock()
			return
		}
		for _, pool := range m.pools {
			pool.mu.Lock()
			// since `free` is a queue, which means the first instance
			// at `free` array is supposed to be the least used one.
			// so we only need to check the first instance in `free` array
			// to do garbage collection
			for len(pool.free) > 0 {
				instance := pool.free[0]
				if time.Since(instance.lastActive) > t.gcCriterion {
					pool.free = pool.free[1:]
					toBeGC = append(toBeGC, instance)
				} else {
					break
				}
			}
			pool.mu.Unlock()
		}
		m.mu.RUnlock()

		// start to gc
		for _, instance := range toBeGC {
			if err := m.Runtime.KillInstance(instance); err != nil {
				log.Printf("garbage collect instance %s-%d failed: %s", instance.LambdaName, instance.ID, err)
			} else {
				log.Printf("garbage collect instance %s-%d", instance.LambdaName, instance.ID)
			}
		}
	}
}

// populate `num` containers with the type of `baseCtrSpec`
type PopulateCtrBackgroundTask struct {
	num         int
	baseCtrSpec types.FunctionDeployment
}

var helloWorldSpec = types.FunctionDeployment{
	Service:    "h-hello-world",
	Image:      "jialianghuang/h-hello-world:latest",
	EnvProcess: "python index.py",
	EnvVars: map[string]string{
		"port": "9001",
	},
	Limits: &types.FunctionResources{
		Memory: "1G",
		CPU:    "1",
	},
}

func NewPopulateCtrBackgroundTask(num int) PopulateCtrBackgroundTask {
	return PopulateCtrBackgroundTask{
		num:         num,
		baseCtrSpec: helloWorldSpec,
	}
}

func (t PopulateCtrBackgroundTask) Run(m *LambdaManager) {
	m.RegisterLambda(t.baseCtrSpec)

	lambdaName := t.baseCtrSpec.Service
	pool, err := m.GetCtrPool(lambdaName)
	if err != nil {
		log.Printf("[PopulateCtrBackgroundTask] error occur when get pool with %s: %s", lambdaName, err)
	}

	log.Printf("will populate %d dummy containers...", t.num)
	for i := 0; i < t.num; i++ {
		id := pool.idAllocator.Add(1)
	retry:
		// but actually the background task should not retry or crash
		// since there should be no load or request while populating
		instance, err := m.Runtime.ColdStart(t.baseCtrSpec, id)
		if errors.Is(err, ColdStartTooMuchError) {
			goto retry
		} else if err != nil {
			log.Printf("[PopulateCtrBackgroundTask] error occur when cold start %s-%d: %s",
				lambdaName, id, err)
			break
		}
		pool.mu.Lock()
		pool.free = append(pool.free, instance)
		pool.mu.Unlock()
		log.Printf("[PopulateCtrBackgroundTask] populate %s-%d", instance.LambdaName, instance.ID)
	}
}
