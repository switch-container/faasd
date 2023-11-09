package provider

import (
	"sync/atomic"

	"github.com/rs/zerolog/log"
)

var mblogger = log.With().
	Str("component", "[MemoryBound]").
	Logger()

type MemoryBound struct {
	bound    int64
	used     atomic.Int64
	peakUsed atomic.Int64
}

func NewMemoryBound(bound int64) MemoryBound {
	return MemoryBound{
		bound: bound,
	}
}

func (m *MemoryBound) ExtraSpaceFor(needed int64) int64 {
	return m.used.Load() + needed - m.bound
}

// Add ctr into pool which means increment memory been used
//
// return current used
func (m *MemoryBound) AddCtr(usage int64) int64 {
	newUsed := m.used.Add(usage)
  // mblogger.Debug().Int64("add usage", usage).Int64("new used val", newUsed).Msg("AddCtr")
	if newUsed > m.peakUsed.Load() {
		m.peakUsed.Store(newUsed)
	}
	return newUsed
}

// Remove ctr from pool which means decrement memory been used
//
// return current used
func (m *MemoryBound) RemoveCtr(usage int64) int64 {
  newUsed := m.used.Add(-usage)
  // mblogger.Debug().Int64("remove usage", usage).Int64("new used val", newUsed).Msg("RemoveCtr")
  return newUsed
}

// Switch ctr = RemoceCtr old + AddCtr new
//
// return current used
func (m *MemoryBound) SwitchCtr(newUsage, oldUsage int64) int64 {
	newUsed := m.used.Add(newUsage - oldUsage)
  // mblogger.Debug().Int64("new ctr", newUsage).Int64("old ctr", oldUsage).Int64("new used val", newUsed).Msg("SwitchCtr")
	if newUsed > m.peakUsed.Load() {
		m.peakUsed.Store(newUsed)
	}
	return newUsed
}

func (m *MemoryBound) Left() int64 {
	return m.bound - m.used.Load()
}
