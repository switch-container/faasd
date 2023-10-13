package provider

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/openfaas/faasd/pkg"
	"github.com/rs/zerolog/log"
)

// This is a read-only cache and will be
// initialized when created.
type CheckpointCache struct {
	data          map[string]bool
	checkpointDir string
}

// we do not use init() directly here
// since `faasd collect` will also call init
func NewCheckpointCache() *CheckpointCache {
	cache := &CheckpointCache{
		data:          make(map[string]bool),
		checkpointDir: pkg.FaasdCheckpointDirPrefix,
	}
	cache.fillCache()
	return cache
}

func (c *CheckpointCache) fillCache() error {
	items, err := os.ReadDir(c.checkpointDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, item := range items {
		if item.Type().IsDir() {
			c.data[item.Name()] = true
			log.Debug().Str("name", item.Name()).Msg("find checkpoint image directory")
		}
	}
	return nil
}

func (c *CheckpointCache) Lookup(serviceName string) bool {
	valid, ok := c.data[serviceName]
	return valid && ok
}

func (c *CheckpointCache) List() []string {
	var res []string

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
