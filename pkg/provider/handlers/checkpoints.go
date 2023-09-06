package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/openfaas/faasd/pkg"
)

type checkpointCache struct {
	data map[string]bool
	rw   sync.RWMutex
}

var cache *checkpointCache

// we do not use init() directly here
// since `faasd collect` will also call init
func InitCheckpointModule() {
	cache = &checkpointCache{
		data: make(map[string]bool),
	}
	cache.fillCache()
}

func (c *checkpointCache) lookup(serviceName string) bool {
	c.rw.RLock()
	defer c.rw.RUnlock()
	valid, ok := cache.data[serviceName]
	return valid && ok
}

func (c *checkpointCache) fillCache() error {
	checkpointDir := pkg.FaasdCheckpointDirPrefix
	// Check if namespace exists, and it has the openfaas label
	items, err := os.ReadDir(checkpointDir)
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
			cache.data[item.Name()] = true
		}
	}
	return nil
}

func (c *checkpointCache) list() []string {
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

func hasCheckpoint(serviceName string) bool {
	if cache.lookup(serviceName) {
		return true
	}
	if err := cache.fillCache(); err != nil {
		log.Printf("update checkpoint cache failed %s", err)
		return false
	}
	return cache.lookup(serviceName)
}

func MakeCheckpointReader() func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		checkpoints := cache.list()
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(checkpoints)
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}
}
