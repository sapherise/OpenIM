package redis

import (
	"context"
	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/database"
	"github.com/redis/go-redis/v9"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every test owns a Redis process on a private Unix socket. Never reuse the
// repository's integration-test addresses or an already running Redis instance.
func newUnreadTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	binary, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("redis-server is required for isolated allocator integration tests")
	}
	dir, err := os.MkdirTemp("/tmp", "im-unread-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "redis.sock")
	cmd := exec.Command(binary, "--port", "0", "--unixsocket", socket, "--save", "", "--appendonly", "no", "--loglevel", "warning")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{Network: "unix", Addr: socket, MaxRetries: -1, DialTimeout: time.Second})
	t.Cleanup(func() { client.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = os.RemoveAll(dir) })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := client.Ping(context.Background()).Err(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("isolated Redis did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return client
}

type expiredReadMongo struct{ database.SeqUser }

func (*expiredReadMongo) GetUserReadSeq(context.Context, string, string) (int64, error) {
	return 1, nil
}

type newerReadBeforeWrite struct {
	redis.UniversalClient
	key string
}

func (c *newerReadBeforeWrite) Eval(ctx context.Context, script string, keys []string, args ...interface{}) *redis.Cmd {
	if strings.Contains(script, "local current = tonumber") {
		c.UniversalClient.HSet(ctx, c.key, "value", 52)
	}
	return c.UniversalClient.Eval(ctx, script, keys, args...)
}
func TestExpiredRepairRedisReadNeverRegresses(t *testing.T) {
	client := newUnreadTestRedis(t)
	ctx := context.Background()
	const key = "SEQ_USER_READ:si_1001_1002:1002"
	if err := client.HSet(ctx, key, "value", 1).Err(); err != nil {
		t.Fatal(err)
	}
	seqs := NewSeqUserCacheRedis(&newerReadBeforeWrite{UniversalClient: client, key: key}, &expiredReadMongo{})
	if err := seqs.SetUserReadSeq(ctx, "si_1001_1002", "1002", 51); err != nil {
		t.Fatal(err)
	}
	value, err := client.HGet(ctx, key, "value").Int64()
	if err != nil || value != 52 {
		t.Fatalf("newer read overwritten: %d, %v", value, err)
	}
}
