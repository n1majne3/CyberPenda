package blackboardv2input

import (
	"fmt"
	"sync"

	detailedjsonschema "github.com/santhosh-tekuri/jsonschema/v6"

	"pentest/internal/blackboardv2contract"
)

type contractInputSchemaEntry struct {
	compile func() (*detailedjsonschema.Schema, error)
}

type contractInputSchemaCache struct {
	mu     sync.Mutex
	values map[string]*contractInputSchemaEntry
}

func (cache *contractInputSchemaCache) load(key string, compile func() (*detailedjsonschema.Schema, error)) (*detailedjsonschema.Schema, error) {
	cache.mu.Lock()
	entry := cache.values[key]
	if entry == nil {
		entry = &contractInputSchemaEntry{compile: sync.OnceValues(compile)}
		if cache.values == nil {
			cache.values = make(map[string]*contractInputSchemaEntry)
		}
		cache.values[key] = entry
	}
	cache.mu.Unlock()

	schema, err := entry.compile()
	if err != nil {
		cache.mu.Lock()
		if cache.values[key] == entry {
			delete(cache.values, key)
		}
		cache.mu.Unlock()
	}
	return schema, err
}

func loadContractInputSchema(schemaName string, sessionOwner bool) (*detailedjsonschema.Schema, error) {
	harness, err := blackboardv2contract.NewHarness()
	if err != nil {
		return nil, fmt.Errorf("load Blackboard v2 Contract input %s: %w", schemaName, err)
	}
	inputSchema, err := harness.ToolInputSchema(schemaName)
	if err != nil {
		return nil, fmt.Errorf("load Blackboard v2 input Schema %s: %w", schemaName, err)
	}
	if sessionOwner {
		inputSchema = SessionToolInputSchema(inputSchema)
	}
	schema, err := compileDetailedToolSchema(inputSchema)
	if err != nil {
		return nil, fmt.Errorf("compile Blackboard v2 input Schema %s: %w", schemaName, err)
	}
	return schema, nil
}
