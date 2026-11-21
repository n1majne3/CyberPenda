package blackboardv2input

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	detailedjsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestContractInputSchemaCacheConcurrentFirstUse(t *testing.T) {
	var cache contractInputSchemaCache
	keys := []string{"changeBatch:project", "changeBatch:session"}
	schemas := []*detailedjsonschema.Schema{{}, {}}
	counts := make([]atomic.Int32, len(keys))
	start := make(chan struct{})
	results := make(chan error, 32)
	var workers sync.WaitGroup
	for worker := range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			index := worker % len(keys)
			schema, err := cache.load(keys[index], func() (*detailedjsonschema.Schema, error) {
				counts[index].Add(1)
				return schemas[index], nil
			})
			if err != nil || schema != schemas[index] {
				results <- fmt.Errorf("load(%q) = %p, %v; want %p", keys[index], schema, err, schemas[index])
			}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		t.Error(err)
	}
	for index := range keys {
		if count := counts[index].Load(); count != 1 {
			t.Errorf("load(%q) compiled %d times; want 1", keys[index], count)
		}
	}
}

func TestContractInputSchemaCacheDoesNotBlockOtherKeys(t *testing.T) {
	var cache contractInputSchemaCache
	warm := &detailedjsonschema.Schema{}
	if _, err := cache.load("warm", func() (*detailedjsonschema.Schema, error) { return warm, nil }); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = cache.load("slow", func() (*detailedjsonschema.Schema, error) {
			close(entered)
			<-release
			return &detailedjsonschema.Schema{}, nil
		})
	}()
	<-entered
	defer func() { close(release); <-finished }()
	results := make(chan error, 1)
	go func() {
		schema, err := cache.load("warm", func() (*detailedjsonschema.Schema, error) {
			return nil, errors.New("cached Schema was compiled again")
		})
		if err != nil || schema != warm {
			results <- fmt.Errorf("load(warm) = %p, %v; want %p", schema, err, warm)
			return
		}
		_, err = cache.load("cold", func() (*detailedjsonschema.Schema, error) { return &detailedjsonschema.Schema{}, nil })
		results <- err
	}()
	select {
	case err := <-results:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Schema compilation blocked other cache keys")
	}
}

func TestContractInputSchemaCacheRetriesErrors(t *testing.T) {
	var cache contractInputSchemaCache
	wantErr := errors.New("compile failed")
	if schema, err := cache.load("test", func() (*detailedjsonschema.Schema, error) { return nil, wantErr }); schema != nil || !errors.Is(err, wantErr) {
		t.Fatalf("load() = %p, %v; want nil, %v", schema, err, wantErr)
	}
	want := &detailedjsonschema.Schema{}
	if schema, err := cache.load("test", func() (*detailedjsonschema.Schema, error) { return want, nil }); schema != want || err != nil {
		t.Fatalf("load() after failure = %p, %v; want %p, nil", schema, err, want)
	}
}

func TestDecodeContractInputConcurrentFirstUse(t *testing.T) {
	start := make(chan struct{})
	results := make(chan error, 32)
	var workers sync.WaitGroup
	for worker := range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			var target struct {
				Key string `json:"key"`
			}
			if err := DecodeContractInput("readRequest", worker%2 == 0, json.RawMessage(`{"key":"entity:host"}`), &target); err != nil {
				results <- err
			} else if target.Key != "entity:host" {
				results <- fmt.Errorf("decoded key = %q; want entity:host", target.Key)
			}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		t.Error(err)
	}
}
