package scheduler

// worker_retry_helpers.go — thin wrappers around go-redis types for retry ZSet.

import (
	"strconv"

	"github.com/redis/go-redis/v9"
)

// scoreZ builds a redis.Z (member + score) for ZADD.
func scoreZ(score float64, member string) redis.Z {
	return redis.Z{Score: score, Member: member}
}

// newScoreRange builds a ZRangeBy for ZRANGEBYSCORE [0, max].
func newScoreRange(max float64) redis.ZRangeBy {
	return redis.ZRangeBy{
		Min: "0",
		Max: strconv.FormatFloat(max, 'f', 0, 64),
	}
}

// toAnySlice converts []string to []any for ZRem variadic args.
func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
