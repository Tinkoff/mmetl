package slack

import (
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
)

func TestRedisStorage(t *testing.T) {
	redis, err := miniredis.Run()
	assert.NoError(t, err)

	redisCfg := &RedisConfig{
		Addr: redis.Addr(),
	}

	t.Run("store, lookup post", func(t *testing.T) {
		storage, err := newRedisStorage(redisCfg, "channel")
		assert.NoError(t, err)

		threadTS := "11"
		post := &IntermediatePost{
			Message: "msg",
		}
		assert.False(t, storage.HasThread(threadTS))
		assert.Nil(t, storage.LookupThread(threadTS))

		storage.StoreThread(threadTS, post)

		assert.True(t, storage.HasThread(threadTS))
		assert.NotNil(t, storage.LookupThread(threadTS))
		assert.Equal(t, "msg", storage.LookupThread(threadTS).Message)
	})

	t.Run("lookup post from another storage", func(t *testing.T) {
		storage, err := newRedisStorage(redisCfg, "channel")
		assert.NoError(t, err)

		threadTS := "21"
		post := &IntermediatePost{
			Message: "msg",
		}
		storage.StoreThread(threadTS, post)
		storage.StoreThread("22", post)
		assert.Equal(t, 2, len(storage.GetChangedThreads()))

		anotherStorage, err := newRedisStorage(redisCfg, "channel")
		assert.NoError(t, err)
		assert.NotNil(t, anotherStorage.LookupThread(threadTS))
		assert.Equal(t, "msg", anotherStorage.LookupThread(threadTS).Message)
		assert.Equal(t, 1, len(anotherStorage.GetChangedThreads())) // only the post that was looked up should be marked as changed
	})

	t.Run("post should retain replies", func(t *testing.T) {
		storage, err := newRedisStorage(redisCfg, "channel")
		assert.NoError(t, err)

		threadTS := "31"
		post := &IntermediatePost{
			Message: "msg",
		}
		storage.StoreThread(threadTS, post)

		assert.Equal(t, 0, len(storage.LookupThread(threadTS).Replies))
		post = storage.LookupThread(threadTS)
		post.Replies = append(post.Replies, &IntermediatePost{})
		assert.Equal(t, 1, len(storage.LookupThread(threadTS).Replies))
	})
}
