package redis

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/cache/cachekey"
	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/database"
	"github.com/redis/go-redis/v9"
)

type singleNoSpareMongo struct {
	database.SeqConversation
	mu   sync.Mutex
	seqs map[string]int64
}

func (m *singleNoSpareMongo) Malloc(ctx context.Context, cid string, size int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	previous := m.seqs[cid]
	m.seqs[cid] += size
	return previous, nil
}

type singleNoSpareRedis3 struct{ redis.UniversalClient }

func (c *singleNoSpareRedis3) Eval(ctx context.Context, script string, keys []string, args ...interface{}) *redis.Cmd {
	// Redis 3 lacks multi-field HSET. This test-only substitution has the same
	// effects; the allocator ignores the return value of these two commands.
	script = strings.ReplaceAll(script,
		`"HSET", key, "CURR", curr_seq, "LAST", last_seq, "TIME", mallocTime`,
		`"HMSET", key, "CURR", curr_seq, "LAST", last_seq, "TIME", mallocTime`)
	return c.UniversalClient.Eval(ctx, script, keys, args...)
}

func newSingleNoSpareAllocator(t *testing.T, initial int64) (*redis.Client, *singleNoSpareMongo, *seqConversationCacheRedis) {
	t.Helper()
	client := newUnreadTestRedis(t)
	var backend redis.UniversalClient = client
	info, err := client.Info(context.Background(), "server").Result()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(info, "redis_version:3.") {
		backend = &singleNoSpareRedis3{UniversalClient: client}
	}
	mongo := &singleNoSpareMongo{seqs: map[string]int64{"si_1001_1002": initial}}
	allocator := NewSeqConversationCacheRedis(backend, mongo).(*seqConversationCacheRedis)
	return client, mongo, allocator
}

func assertSingleNoSpareState(t *testing.T, client *redis.Client, mongo *singleNoSpareMongo, want int64) {
	t.Helper()
	const cid = "si_1001_1002"
	durable, err := mongo.Malloc(context.Background(), cid, 0)
	if err != nil {
		t.Fatal(err)
	}
	values, err := client.HMGet(context.Background(), cachekey.GetMallocSeqKey(cid), "CURR", "LAST").Result()
	if err != nil || durable != want || len(values) != 2 || values[0] != strconv.FormatInt(want, 10) || values[1] != strconv.FormatInt(want, 10) {
		t.Fatalf("Mongo=%d, Redis=%v, error=%v; all must equal %d", durable, values, err, want)
	}
}

func TestSingleNoSpareRecovery(t *testing.T) {
	client, mongo, allocator := newSingleNoSpareAllocator(t, 0)
	ctx := context.Background()
	const cid = "si_1001_1002"
	if first, err := allocator.Malloc(ctx, cid, 1); err != nil || first != 0 {
		t.Fatalf("first allocation=%d, error=%v", first, err)
	}
	assertSingleNoSpareState(t, client, mongo, 1)
	userSeq := NewSeqUserCacheRedis(client, &expiredReadMongo{}) // Durable read cursor is 1.
	if _, err := userSeq.GetUserReadSeq(ctx, cid, "1002"); err != nil {
		t.Fatal(err)
	}
	// Lose both caches, while keeping the Mongo counters, as after key expiry.
	if err := client.Del(ctx, cachekey.GetMallocSeqKey(cid), cachekey.GetSeqUserReadSeqKey(cid, "1002")).Err(); err != nil {
		t.Fatal(err)
	}
	maximum, err := allocator.GetMaxSeq(ctx, cid)
	if err != nil {
		t.Fatal(err)
	}
	read, err := userSeq.GetUserReadSeq(ctx, cid, "1002")
	if err != nil || maximum != 1 || read != 1 || maximum-read != 0 {
		t.Fatalf("after cache loss: max=%d, read=%d, error=%v; want zero unread", maximum, read, err)
	}
	if first, err := allocator.Malloc(ctx, cid, 3); err != nil || first != 1 {
		t.Fatalf("next batch must allocate 2..4: first=%d, error=%v", first, err)
	}
	assertSingleNoSpareState(t, client, mongo, 4)
	if err := client.Del(ctx, cachekey.GetMallocSeqKey(cid)).Err(); err != nil {
		t.Fatal(err)
	}
	maximum, err = allocator.GetMaxSeq(ctx, cid)
	if err != nil || maximum != 4 || maximum-read != 3 {
		t.Fatalf("three new messages must remain unread after recovery: max=%d, read=%d, error=%v", maximum, read, err)
	}
}

func TestSingleNoSpareConcurrentAllocation(t *testing.T) {
	client, mongo, allocator := newSingleNoSpareAllocator(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	const count = 8
	type result struct {
		first int64
		err   error
	}
	results := make(chan result, count)
	for i := 0; i < count; i++ {
		go func() {
			first, err := allocator.Malloc(ctx, "si_1001_1002", 1)
			results <- result{first, err}
		}()
	}
	seen := make(map[int64]bool, count)
	for i := 0; i < count; i++ {
		r := <-results
		if r.err != nil || r.first < 0 || r.first >= count || seen[r.first] {
			t.Errorf("duplicate or invalid allocation: first=%d, error=%v", r.first, r.err)
		}
		seen[r.first] = true
	}
	assertSingleNoSpareState(t, client, mongo, count)
}

func TestSingleNoSpareKeepsLegacyCounters(t *testing.T) {
	for _, test := range []struct {
		name                                string
		current, size, first, curr, durable int64
	}{
		{"existing spare slots", 1, 1, 1, 2, 51},
		{"exhausted old reservation", 51, 1, 51, 52, 52},
		{"batch crosses old reservation", 49, 3, 49, 52, 54},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, mongo, allocator := newSingleNoSpareAllocator(t, 51)
			ctx := context.Background()
			const cid = "si_1001_1002"
			key := cachekey.GetMallocSeqKey(cid)
			if err := client.HMSet(ctx, key, "CURR", test.current, "LAST", 51, "TIME", time.Now().UnixMilli()).Err(); err != nil {
				t.Fatal(err)
			}
			first, err := allocator.Malloc(ctx, cid, test.size)
			if err != nil || first != test.first {
				t.Fatalf("first=%d, error=%v; want %d", first, err, test.first)
			}
			durable, _ := mongo.Malloc(ctx, cid, 0)
			current, err := client.HGet(ctx, key, "CURR").Int64()
			last, lastErr := client.HGet(ctx, key, "LAST").Int64()
			if err != nil || lastErr != nil || current != test.curr || last != test.durable || durable != test.durable {
				t.Fatalf("legacy cursor changed incorrectly: current=%d, last=%d, Mongo=%d, errors=%v/%v", current, last, durable, err, lastErr)
			}
		})
	}
}

func TestSingleNoSpareSessionScope(t *testing.T) {
	allocator := &seqConversationCacheRedis{}
	for cid, want := range map[string]int64{
		"si_1001_1002": 1, "g_1001": 101, "sg_1001": 101, "n_1001_1002": 51, "sn_1001_1002": 51,
	} {
		if got := allocator.getMallocSize(cid, 1); got != want {
			t.Errorf("%s: requested 1, reserved %d; want %d", cid, got, want)
		}
		if got := allocator.getMallocSize(cid, 0); got != 0 {
			t.Errorf("%s: query-only access reserved %d", cid, got)
		}
	}
}
