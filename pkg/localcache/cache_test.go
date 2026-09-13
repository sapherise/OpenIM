// Copyright © 2024 OpenIM. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package localcache

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openimsdk/open-im-server/v3/pkg/localcache/lru"
)

func TestName(t *testing.T) {
	c := New[string](WithExpirationEvict())
	//c := New[string]()
	ctx := context.Background()

	const (
		num  = 10000
		tNum = 10000
		kNum = 100000
		pNum = 100
	)

	getKey := func(v uint64) string {
		return fmt.Sprintf("key_%d", v%kNum)
	}

	start := time.Now()
	t.Log("start", start)

	var (
		get atomic.Int64
		del atomic.Int64
	)

	incrGet := func() {
		if v := get.Add(1); v%pNum == 0 {
			//t.Log("#get count", v/pNum)
		}
	}
	incrDel := func() {
		if v := del.Add(1); v%pNum == 0 {
			//t.Log("@del count", v/pNum)
		}
	}

	var wg sync.WaitGroup

	for i := 0; i < tNum; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < num; i++ {
				c.Get(ctx, getKey(rand.Uint64()), func(ctx context.Context) (string, error) {
					return fmt.Sprintf("index_%d", i), nil
				})
				incrGet()
			}
		}()

		go func() {
			defer wg.Done()
			time.Sleep(time.Second / 10)
			for i := 0; i < num; i++ {
				c.Del(ctx, getKey(rand.Uint64()))
				incrDel()
			}
		}()
	}

	wg.Wait()
	end := time.Now()
	t.Log("end", end)
	t.Log("time", end.Sub(start))
	t.Log("get", get.Load())
	t.Log("del", del.Load())
	// 137.35s
}

// Linked keys may share an LRU slot even when their strings differ.
func TestCacheEvictLinkedKeys(t *testing.T) {
	for _, mode := range []struct {
		name   string
		option Option
	}{
		{name: "lazy", option: WithLazy()},
		{name: "expiration", option: WithExpirationEvict()},
	} {
		for _, slots := range []int{1, 100} {
			for _, action := range []string{"evict", "delete"} {
				t.Run(fmt.Sprintf("%s/slots=%d/%s", mode.name, slots, action), func(t *testing.T) {
					c := New[string](mode.option, WithLocalSlotNum(slots), WithLocalSlotSize(2),
						WithLinkSlotNum(slots), WithLocalSuccessTTL(time.Hour))
					defer c.Stop()

					// Select three different keys in one slot; the second stays cached
					// until invalidation propagates from the first.
					keys := make([]string, 0, 3)
					for i := 0; len(keys) < 3; i++ {
						key := fmt.Sprintf("key_%d", i)
						if LRUStringHash(key)%uint64(slots) == 0 {
							keys = append(keys, key)
						}
					}
					ctx := context.Background()
					oldValue := func(context.Context) (string, error) { return "old", nil }
					freshValue := func(context.Context) (string, error) { return "fresh", nil }
					if _, err := c.GetLink(ctx, keys[0], oldValue, keys[1]); err != nil {
						t.Fatal(err)
					}
					if _, err := c.Get(ctx, keys[1], oldValue); err != nil {
						t.Fatal(err)
					}

					done := make(chan error, 1)
					go func() {
						if action == "evict" {
							if _, err := c.Get(ctx, keys[2], freshValue); err != nil {
								done <- err
								return
							}
						} else {
							c.DelLocal(ctx, keys[0])
							value, err := c.Get(ctx, keys[1], freshValue)
							if err != nil || value != "fresh" {
								done <- fmt.Errorf("explicit deletion left a stale linked value: %q, err: %v", value, err)
								return
							}
						}

						// Capacity eviction is asynchronous, but must still invalidate linked
						// values and allow subsequent requests to use the same slot.
						deadline := time.Now().Add(time.Second)
						for {
							value, err := c.Get(ctx, keys[1], freshValue)
							if err != nil {
								done <- err
								return
							}
							if value == "fresh" {
								break
							}
							if time.Now().After(deadline) {
								done <- fmt.Errorf("linked key was not invalidated: %q", value)
								return
							}
							time.Sleep(time.Millisecond)
						}
						value, err := c.Get(ctx, keys[2], freshValue)
						if err == nil && value != "fresh" {
							err = fmt.Errorf("unexpected value from the same slot: %q", value)
						}
						done <- err
					}()

					select {
					case err := <-done:
						if err != nil {
							t.Fatal(err)
						}
					case <-time.After(2 * time.Second):
						t.Fatal("linked-key invalidation deadlocked the cache slot")
					}
				})
			}
		}
	}
}

// Pause deletion of a linked entry without holding an LRU lock. This makes an
// early return from Del/DelLocal observable independently of goroutine scheduling.
type linkedDeleteGate struct {
	lru.LRU[string, string]
	calls    atomic.Int32
	key      string
	entered  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func (g *linkedDeleteGate) Del(key string) bool {
	if key == g.key && g.calls.Add(1) == 1 {
		close(g.entered)
		<-g.release
		defer close(g.finished)
	}
	return g.LRU.Del(key)
}

func TestCacheDeleteWaitsForLinkedKeys(t *testing.T) {
	for _, mode := range []struct {
		name   string
		option Option
	}{
		{name: "lazy", option: WithLazy()},
		{name: "expiration", option: WithExpirationEvict()},
	} {
		for _, method := range []string{"Del", "DelLocal"} {
			t.Run(mode.name+"/"+method, func(t *testing.T) {
				ctx := context.Background()
				c := New[string](mode.option, WithLocalSlotNum(2), WithLocalSlotSize(4),
					WithLinkSlotNum(2), WithLocalSuccessTTL(time.Hour)).(*cache[string])
				defer c.Stop()
				oldValue := func(context.Context) (string, error) { return "old", nil }
				freshValue := func(context.Context) (string, error) { return "fresh", nil }
				if _, err := c.GetLink(ctx, "key_0", oldValue, "key_1"); err != nil {
					t.Fatal(err)
				}
				if _, err := c.Get(ctx, "key_1", oldValue); err != nil {
					t.Fatal(err)
				}
				gate := &linkedDeleteGate{LRU: c.local, key: "key_1",
					entered: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
				c.local = gate
				release := sync.OnceFunc(func() { close(gate.release) })
				defer release()
				done := make(chan struct{})
				go func() {
					if method == "Del" {
						c.Del(ctx, "key_0")
					} else {
						c.DelLocal(ctx, "key_0")
					}
					close(done)
				}()
				select {
				case <-gate.entered:
				case <-time.After(time.Second):
					t.Fatal("linked deletion was not attempted")
				}
				select {
				case <-done:
					t.Error("explicit deletion returned before linked invalidation finished")
				case <-time.After(20 * time.Millisecond):
				}
				release()
				for _, completion := range []<-chan struct{}{gate.finished, done} {
					select {
					case <-completion:
					case <-time.After(time.Second):
						t.Fatal("deletion did not finish after the linked entry was released")
					}
				}
				if value, err := c.Get(ctx, "key_1", freshValue); err != nil || value != "fresh" {
					t.Fatalf("linked entry remained stale after deletion: %q, err: %v", value, err)
				}
			})
		}
	}
}

func TestCacheConcurrentDeleteWaitsForLinkedKeys(t *testing.T) {
	for _, mode := range []struct {
		name   string
		option Option
	}{
		{name: "lazy", option: WithLazy()},
		{name: "expiration", option: WithExpirationEvict()},
	} {
		// Both the same key and a different key in the detached dependency group
		// must wait for the in-progress invalidation to finish.
		for _, secondKey := range []string{"key_0", "key_1"} {
			t.Run(mode.name+"/"+secondKey, func(t *testing.T) {
				ctx := context.Background()
				c := New[string](mode.option, WithLocalSlotNum(2), WithLocalSlotSize(4),
					WithLinkSlotNum(2), WithLocalSuccessTTL(time.Hour)).(*cache[string])
				defer c.Stop()
				oldValue := func(context.Context) (string, error) { return "old", nil }
				freshValue := func(context.Context) (string, error) { return "fresh", nil }
				if _, err := c.GetLink(ctx, "key_0", oldValue, "key_1"); err != nil {
					t.Fatal(err)
				}
				if _, err := c.Get(ctx, "key_1", oldValue); err != nil {
					t.Fatal(err)
				}
				gate := &linkedDeleteGate{LRU: c.local, key: "key_0",
					entered: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
				c.local = gate
				release := sync.OnceFunc(func() { close(gate.release) })
				defer release()
				firstDone := make(chan struct{})
				go func() {
					c.DelLocal(ctx, "key_0")
					close(firstDone)
				}()
				select {
				case <-gate.entered:
				case <-time.After(time.Second):
					t.Fatal("first deletion did not reach the cache after detaching links")
				}

				secondStarted := make(chan struct{})
				secondDone := make(chan struct{})
				go func() {
					close(secondStarted)
					c.Del(ctx, secondKey)
					close(secondDone)
				}()
				<-secondStarted
				select {
				case <-secondDone:
					t.Error("concurrent deletion returned before the detached dependency group was invalidated")
				case <-time.After(20 * time.Millisecond):
				}
				release()
				for _, done := range []<-chan struct{}{firstDone, secondDone} {
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Fatal("concurrent deletion did not finish after the first call was released")
					}
				}
				for _, key := range []string{"key_0", "key_1"} {
					if value, err := c.Get(ctx, key, freshValue); err != nil || value != "fresh" {
						t.Fatalf("stale value after concurrent deletion of %s: %q, err: %v", key, value, err)
					}
				}
			})
		}
	}
}
