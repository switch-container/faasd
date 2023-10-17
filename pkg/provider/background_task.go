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
	// how many goroutines will do gc work
	concurrency int
}

func NewInstanceGCBackgroundTask(interval, criterion time.Duration, concurrency int) InstanceGCBackgroundTask {
	return InstanceGCBackgroundTask{
		interval:    interval,
		gcCriterion: criterion,
		concurrency: concurrency,
	}
}

func (t InstanceGCBackgroundTask) gcWork(m *LambdaManager, ch <-chan *CtrInstance) {
	for instance := range ch {
		instanceID := instance.GetInstanceID()
		if err := m.KillInstance(instance); err != nil {
			bglogger.Error().Err(err).Str("instance", instanceID).Msg("garbage collect instance failed")
		} else {
			bglogger.Debug().Str("instance", instanceID).Msg("garbage collect instance finish")
		}
	}
}

func (t InstanceGCBackgroundTask) Run(m *LambdaManager) {
	instanceCh := make(chan *CtrInstance, 128)
	m.registerCleanup(func(lm *LambdaManager) {
		close(instanceCh)
	})
	for i := 0; i < t.concurrency; i++ {
		go t.gcWork(m, instanceCh)
	}

	var toBeGC []*CtrInstance
	for {
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
					m.memBound.RemoveCtr(pool.memoryRequirement)
				} else {
					break
				}
			}
			pool.mu.Unlock()
		}
		m.mu.RUnlock()

		for len(toBeGC) > 0 {
			instance := toBeGC[0]
			select {
			case instanceCh <- instance:
				// if send to channel, then we pop it from toBeGC
				toBeGC = toBeGC[1:]
				bglogger.Debug().Str("instance id", instance.GetInstanceID()).
					Msg("decide to gc instance")
			default:
				// if channel if full, we buffer it until next round to retry
				// the main idea here is that we should not block this goroutine
				// so that at least the containers can be kick out as soon as possible
				bglogger.Debug().Int("buf size", len(toBeGC)).Msg("ctr garbage collection channel is full")
				break
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
		Memory: "256M",
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
	if err := m.RegisterLambda(t.baseCtrSpec); err != nil {
		bglogger.Error().Err(err).Msg("register base ctr faild!")
		return
	}

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
		m.memBound.AddCtr(pool.memoryRequirement)
		instance, err := m.ColdStart(DeployResult{
			decision:   COLD_START,
			targetPool: pool,
		}, id)
		if errors.Is(err, ErrColdStartTooMuch) {
			goto retry
		} else if err != nil {
			bglogger.Error().Err(err).Str("instance id", instance.GetInstanceID()).
				Msg("PopulateCtrBackgroundTask cold start failed")
			break
		}
		pool.mu.Lock()
		pool.free = append(pool.free, instance)
		pool.mu.Unlock()
		bglogger.Error().Err(err).Str("instance id", instance.GetInstanceID()).
			Msg("PopulateCtrBackgroundTask cold start succeed")
	}
}
