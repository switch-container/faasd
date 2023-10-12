package provider

import (
	"time"

	"github.com/openfaas/faas-provider/types"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
)

var bglogger = log.With().
	Str("component", "[BackgroundTask]").
	Logger()

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
			instanceID := GetInstanceID(instance.LambdaName, instance.ID)
			if err := m.Runtime.KillInstance(instance); err != nil {
				bglogger.Error().Err(err).Str("instance", instanceID).Msg("garbage collect instance failed")
			} else {
				bglogger.Debug().Str("instance", instanceID).Msg("garbage collect instance")
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
		bglogger.Error().Err(err).Str("lambda name", lambdaName).
			Msg("PopulateCtrBackgroundTask get ctr pool failed")
	}

	bglogger.Debug().Int("num", t.num).Msg("start populate dummy containers...")
	for i := 0; i < t.num; i++ {
		id := pool.idAllocator.Add(1)
	retry:
		// but actually the background task should not retry or crash
		// since there should be no load or request while populating
		instance, err := m.Runtime.ColdStart(t.baseCtrSpec, id)
		if errors.Is(err, ColdStartTooMuchError) {
			goto retry
		} else if err != nil {
			bglogger.Error().Err(err).Str("lambda name", lambdaName).Uint64("id", id).
				Msg("PopulateCtrBackgroundTask cold start failed")
			break
		}
		pool.mu.Lock()
		pool.free = append(pool.free, instance)
		pool.mu.Unlock()
		bglogger.Error().Err(err).Str("lambda name", lambdaName).Uint64("id", instance.ID).
			Msg("PopulateCtrBackgroundTask cold start succeed")
	}
}
