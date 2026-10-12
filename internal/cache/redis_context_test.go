package cache

import (
	"context"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"net"
	"testing"
	"time"
)

// An unavailable telemetry store must respect the caller's short deadline,
// including while waiting for Redis's initial handshake response.
func TestTelemetryRedisDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); <-done }()
		}
	}()
	c := &RedisCache{client: redis.NewClient(&redis.Options{Addr: listener.Addr().String(), ContextTimeoutEnabled: true})}
	defer c.client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	require.Error(t, c.SetWithContext(ctx, "attempt", "1", time.Minute))
	require.Less(t, time.Since(start), time.Second)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	start = time.Now()
	require.Empty(t, c.GetWithContext(ctx2, "attempt"))
	require.Less(t, time.Since(start), time.Second)
}
