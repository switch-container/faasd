package provider

import (
	"context"
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
	return InstanceGCBackgroundTask{
		interval:    interval,
		gcCriterion: criterion,
	}
}

func (t InstanceGCBackgroundTask) Run(m *LambdaManager) {
	var toBeGC []*CtrInstance
	for !m.terminate {
		time.Sleep(t.interval)
		// step 1: find all instances that not active for gcCriterion
		for {
			instance := m.PopFromGlobalLRU()
			if instance == nil {
				break
			}
			if time.Since(instance.lastActive) <= t.gcCriterion {
				// we has collect all expired instances already
				// put this one back and break
				m.PushIntoGlobalLRU(instance)
				break
			}
			// we go here means need to delete the instance from pool
			// and add it to gc list
			if instance.localPQIndex != 0 {
				dplogger.Error().Str("instance id", instance.GetInstanceID()).
					Int("local PQ Index", instance.localPQIndex).Msg("find weird instance when gc")
			}
			pool, _ := m.GetCtrPool(instance.ServiceName)
			pool.RemoveFromFree(instance)
			toBeGC = append(toBeGC, instance)
			m.memBound.RemoveCtr(pool.memoryRequirement)
		}

		// step 2: start gc
		for len(toBeGC) > 0 {
			instance := toBeGC[0]
			enqueue := m.Runtime.KillInstanceAsync(instance, false)
			if enqueue {
				// if send to channel, then we pop it from toBeGC
				toBeGC = toBeGC[1:]
			} else {
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
	if err := m.RegisterService(t.baseCtrSpec); err != nil {
		bglogger.Error().Err(err).Msg("register base ctr faild!")
		return
	}

	serviceName := t.baseCtrSpec.Service
	pool, err := m.GetCtrPool(serviceName)
	if err != nil {
		bglogger.Error().Err(err).Str("service name", serviceName).
			Msg("PopulateCtrBackgroundTask get ctr pool failed")

	}

	bglogger.Debug().Int("num", t.num).Msg("start populate dummy containers...")
	for i := 0; i < t.num; i++ {
		id := pool.idAllocator.Add(1)
	retry:
		// but actually the background task should not retry or crash
		// since there should be no load or request while populating
		instance, err := m.StartNewCtr(DeployResult{
			decision:   COLD_START,
			targetPool: pool,
		}, id)
		if errors.Is(err, ErrColdStartTooMuch) {
			goto retry
		} else if err != nil {
			bglogger.Err(err).Str("lambda", serviceName).
				Msg("PopulateCtrBackgroundTask cold start failed")
			break
		}
		m.memBound.AddCtr(pool.memoryRequirement)
		pool.PushIntoFree(instance)
		m.PushIntoGlobalLRU(instance)
		bglogger.Debug().Err(err).Str("instance id", instance.GetInstanceID()).
			Msg("PopulateCtrBackgroundTask cold start succeed")
	}
}

type FillFaasnapNetworkBackgroundTask struct {
	num int
}

func NewFillFaasnapNetworkBackgroundTask(num int) FillFaasnapNetworkBackgroundTask {
	return FillFaasnapNetworkBackgroundTask{num}
}

func (t FillFaasnapNetworkBackgroundTask) Run(m *LambdaManager) {
	if m.Runtime.fnm == nil {
		bglogger.Error().Msg("FaasnapNetworkManager is nil, cannot fill network for faasnap")
		return
	}
	netNss := make([]string, 0, t.num)
	var (
		netns   string
		isNewns bool
		err     error
	)
	for i := 0; i < t.num; i++ {
		// this closure is just used to defer cancel()
		func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			netns, isNewns, err = m.Runtime.fnm.GetNetwork(ctx)
			if err != nil {
				bglogger.Err(err).Msg("FillFaasnapNetworkBackgroundTask get network err")
			} else if err := ctx.Err(); err != nil {
				bglogger.Err(err).Msg("FillFaasnapNetworkBackgroundTask GetNetwork timeout")
			}
		}()
		if err != nil {
			return
		}
		if !isNewns {
			bglogger.Warn().Msg("FillFaasnapNetworkBackgroundTask GetNetwork() not create but reuse")
		}
		netNss = append(netNss, netns)
	}
	bglogger.Debug().Int("num", len(netNss)).Msg("FillFaasnapNetworkBackgroundTask precreate network ns")
	for _, netNs := range netNss {
		m.Runtime.fnm.PutNetwork(netNs)
	}
}
