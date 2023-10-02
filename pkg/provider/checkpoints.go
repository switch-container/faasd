package provider

import (
	"encoding/json"
	"net/http"
	"os"
	"sync"

	"github.com/openfaas/faasd/pkg"
)

type CheckpointCache struct {
	data map[string]bool
	rw   sync.RWMutex

	checkpointDir string
}

// we do not use init() directly here
// since `faasd collect` will also call init
func NewCheckpointCache() *CheckpointCache {
	cache := &CheckpointCache{
		data:          make(map[string]bool),
		checkpointDir: pkg.FaasdCheckpointDirPrefix,
	}
	cache.FillCache()
	return cache
}

func (c *CheckpointCache) Lookup(serviceName string) bool {
	c.rw.RLock()
	defer c.rw.RUnlock()
	valid, ok := c.data[serviceName]
	return valid && ok
}

func (c *CheckpointCache) FillCache() error {
	// Check if namespace exists, and it has the openfaas label
	items, err := os.ReadDir(c.checkpointDir)
	if err != nil {
		// we do not have any checkpoints for now
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	c.rw.Lock()
	defer c.rw.Unlock()

	// first invalidate all checkpoint
	for k := range c.data {
		c.data[k] = false
	}
	// update from dir
	for _, item := range items {
		if item.Type().IsDir() {
			c.data[item.Name()] = true
		}
	}
	return nil
}

func (c *CheckpointCache) List() []string {
	var res []string
	c.rw.RLock()
	defer c.rw.RUnlock()

	for k, valid := range c.data {
		if valid {
			res = append(res, k)
		}
	}
	return res
}

func MakeCheckpointReader(m *LambdaManager) func(w http.ResponseWriter, r *http.Request) {
	c := m.Runtime.checkpointCache
	return func(w http.ResponseWriter, r *http.Request) {
		checkpoints := c.List()
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(checkpoints)
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}
}
