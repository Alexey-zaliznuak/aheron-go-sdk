// Package readcache provides disposable, distributed read-through caches.
// It is never a store for authorization decisions, execution state or work leases.
package readcache

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

const (
	maxTTL        = 24 * time.Hour
	tagTTL        = 25 * time.Hour
	maxValueBytes = 4 << 20
)

type Config struct {
	URL              string
	Namespace        string
	Lease            time.Duration
	OperationTimeout time.Duration
	// LoadTimeout bounds a shared fill independently of any individual waiter.
	LoadTimeout time.Duration
}

type Cache struct {
	client                                              *redis.Client
	namespace                                           string
	lease, operationTimeout, loadTimeout                time.Duration
	group                                               singleflight.Group
	log                                                 *zap.Logger
	unavailableUntil                                    atomic.Int64
	hits, misses, loads, waits, failures, invalidations atomic.Uint64
}

type Statistics struct {
	Hits, Misses, Loads, Waits, Failures, Invalidations uint64
}

func (c *Cache) Statistics() Statistics {
	if c == nil {
		return Statistics{}
	}
	return Statistics{c.hits.Load(), c.misses.Load(), c.loads.Load(), c.waits.Load(), c.failures.Load(), c.invalidations.Load()}
}

// New returns nil for an empty URL, disabling caching and its read overhead.
// It validates configuration without requiring Redis to be available at startup.
func New(cfg Config, logger *zap.Logger) (*Cache, error) {
	if cfg.Namespace == "" {
		cfg.Namespace = "aheron:stand"
	}
	if len(cfg.Namespace) > 128 || strings.ContainsAny(cfg.Namespace, "\r\n{}") {
		return nil, errors.New("readcache: invalid namespace")
	}
	if cfg.Lease < 0 || cfg.OperationTimeout < 0 || cfg.LoadTimeout < 0 {
		return nil, errors.New("readcache: durations must be positive")
	}
	if cfg.Lease == 0 {
		cfg.Lease = 5 * time.Second
	}
	if cfg.OperationTimeout == 0 {
		cfg.OperationTimeout = 500 * time.Millisecond
	}
	if cfg.LoadTimeout == 0 {
		cfg.LoadTimeout = 30 * time.Second
	}
	if cfg.Lease < 30*time.Millisecond {
		return nil, errors.New("readcache: lease must be at least 30ms")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	c := &Cache{namespace: cfg.Namespace + ":readcache:v1:", lease: cfg.Lease, operationTimeout: cfg.OperationTimeout, loadTimeout: cfg.LoadTimeout, log: logger}
	if cfg.URL == "" {
		return nil, nil
	}
	opts, err := redis.ParseURL(cfg.URL)
	if err != nil {
		return nil, errors.New("readcache: invalid Redis URL")
	}
	opts.DialTimeout = cfg.OperationTimeout
	opts.ReadTimeout = cfg.OperationTimeout
	opts.WriteTimeout = cfg.OperationTimeout
	opts.MaxRetries = -1
	opts.ContextTimeoutEnabled = true
	c.client = redis.NewClient(opts)
	return c, nil
}

func (c *Cache) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}

type payload struct {
	Value json.RawMessage `json:"value"`
	Until int64           `json:"until"`
}

type result struct {
	data []byte
	// Never broadcast an unencodable mutable value through singleflight.
	// Each caller independently loads such values without caching.
	uncacheable bool
}

type loader func(context.Context) (result, error)

// Get caches successful values for at most ttl (jitter only shortens it).
// Keys must identify the tenant, resource, representation version and all inputs.
func Get[T any](ctx context.Context, c *Cache, key string, ttl time.Duration, load func(context.Context) (T, error)) (T, error) {
	// Count source latency and lock wait against the freshness budget. Otherwise
	// a slow old snapshot could gain a new full TTL after a failed invalidation.
	deadline := time.Now().Add(ttl)
	return get(ctx, c, key, func(T) time.Time { return deadline }, load)
}

// GetUntil is suitable for immutable records with an absolute expiry, e.g. token
// bodies. Callers must still validate expiry and current authorization per use.
func GetUntil[T any](ctx context.Context, c *Cache, key string, expiresAt func(T) time.Time, load func(context.Context) (T, error)) (T, error) {
	return get(ctx, c, key, expiresAt, load)
}

func get[T any](ctx context.Context, c *Cache, key string, expiresAt func(T) time.Time, load func(context.Context) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if c == nil {
		return load(ctx)
	}
	wrapped := func(fillCtx context.Context) (result, error) {
		c.loads.Add(1)
		value, err := load(fillCtx)
		if err != nil {
			return result{}, err
		}
		body, err := json.Marshal(value)
		if err != nil {
			return result{uncacheable: true}, nil
		}
		deadline := expiresAt(value)
		now := time.Now()
		ttl := deadline.Sub(now)
		if ttl > maxTTL {
			ttl = maxTTL
		}
		// Do not round a requested deadline up. Randomness spreads bulk expiries.
		if ttl > time.Millisecond {
			ttl -= time.Duration(randomByte()) * (ttl / 2560)
		}
		encoded, _ := json.Marshal(payload{Value: body, Until: now.Add(ttl).UnixNano()})
		return result{data: encoded}, nil
	}
	for attempts := 0; ; attempts++ {
		v, err := c.coalesce(ctx, key, wrapped)
		if err != nil {
			return zero, err
		}
		if v.uncacheable {
			return load(ctx)
		}
		var p payload
		var value T
		if err = json.Unmarshal(v.data, &p); err == nil {
			decoder := json.NewDecoder(bytes.NewReader(p.Value))
			decoder.UseNumber()
			err = decoder.Decode(&value)
		}
		if err == nil {
			return value, nil
		}
		// A rolling deployment with a changed codec must use a new key version.
		// A corrupt/foreign entry is discarded without exposing it to callers.
		_ = c.Invalidate(ctx, key)
		if attempts > 0 {
			return load(ctx)
		}
	}
}

func (c *Cache) coalesce(ctx context.Context, key string, load loader) (result, error) {
	ch := c.group.DoChan(key, func() (any, error) {
		// A canceled leader must not cancel other callers waiting for its fill.
		fillCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.loadTimeout)
		defer cancel()
		return c.read(fillCtx, key, load)
	})
	select {
	case <-ctx.Done():
		return result{}, ctx.Err()
	case v := <-ch:
		if v.Err != nil {
			return result{}, v.Err
		}
		return v.Val.(result), nil
	}
}

var acquire = redis.NewScript(`
local value = redis.call('GET', KEYS[1])
if value then return {1, value} end
if redis.call('SET', KEYS[2], ARGV[1], 'NX', 'PX', ARGV[2]) then return {2, ''} end
return {0, ''}`)

var publish = redis.NewScript(`
if redis.call('GET', KEYS[2]) ~= ARGV[1] then return 0 end
if tonumber(ARGV[3]) > 0 then redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3]) end
redis.call('DEL', KEYS[2])
return 1`)

var release = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end
return 0`)

var renew = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('PEXPIRE', KEYS[1], ARGV[2]) end
return 0`)

var discard = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end
return 0`)

func (c *Cache) keys(key string) (string, string) {
	sum := sha256.Sum256([]byte(key))
	base := c.namespace + "{" + hex.EncodeToString(sum[:]) + "}"
	return base + ":value", base + ":lock"
}

func (c *Cache) available() bool {
	return c.client != nil && time.Now().UnixNano() >= c.unavailableUntil.Load()
}

func (c *Cache) failed(operation string) {
	c.failures.Add(1)
	now := time.Now()
	previous := c.unavailableUntil.Swap(now.Add(time.Second).UnixNano())
	if now.UnixNano() >= previous {
		// Do not log URLs, keys, tokens, source data or raw Redis server errors.
		c.log.Error("read cache unavailable; reading source", zap.String("operation", operation))
	}
}

func (c *Cache) run(ctx context.Context, script *redis.Script, keys []string, args ...any) *redis.Cmd {
	opCtx, cancel := context.WithTimeout(ctx, c.operationTimeout)
	defer cancel()
	return script.Run(opCtx, c.client, keys, args...)
}

func (c *Cache) read(ctx context.Context, key string, load loader) (result, error) {
	if !c.available() {
		return load(ctx)
	}
	valueKey, lockKey := c.keys(key)
	token := nonce()
	for {
		if err := ctx.Err(); err != nil {
			return result{}, err
		}
		reply, err := c.run(ctx, acquire, []string{valueKey, lockKey}, token, c.lease.Milliseconds()).Slice()
		if err != nil {
			c.failed("acquire")
			return load(ctx)
		}
		switch reply[0].(int64) {
		case 1:
			body := []byte(reply[1].(string))
			var p payload
			if json.Unmarshal(body, &p) != nil || p.Until <= time.Now().UnixNano() || len(p.Value) == 0 {
				if err := c.run(ctx, discard, []string{valueKey}, body).Err(); err != nil {
					c.failed("discard")
					return load(ctx)
				}
				continue
			}
			c.hits.Add(1)
			return result{data: body}, nil
		case 2:
			c.misses.Add(1)
			loaded, err := c.fill(ctx, []string{valueKey, lockKey}, token, load)
			if errors.Is(err, errLeaseLost) {
				continue
			}
			return loaded, err
		default:
			c.waits.Add(1)
			timer := time.NewTimer(time.Duration(15+randomByte()%20) * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return result{}, ctx.Err()
			case <-timer.C:
			}
		}
	}
}

var errLeaseLost = errors.New("readcache: fill invalidated or lease lost")

func (c *Cache) fill(ctx context.Context, keys []string, token string, load loader) (result, error) {
	stop := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(c.lease / 3)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				ok, err := c.run(ctx, renew, keys[1:], token, c.lease.Milliseconds()).Int()
				if err != nil || ok == 0 {
					return
				}
			}
		}
	}()
	loaded, err := load(ctx)
	close(stop)
	<-finished
	if err != nil {
		_ = c.run(ctx, release, keys[1:], token).Err()
		return result{}, err
	}
	var p payload
	_ = json.Unmarshal(loaded.data, &p)
	ttl := time.Until(time.Unix(0, p.Until)).Milliseconds()
	if len(loaded.data) > maxValueBytes || len(loaded.data) == 0 {
		ttl = 0
	}
	ok, pubErr := c.run(ctx, publish, keys, token, loaded.data, ttl).Int()
	if pubErr != nil {
		c.failed("publish")
		return loaded, nil
	}
	if ok == 0 {
		return result{}, errLeaseLost
	}
	return loaded, nil
}

// Invalidate atomically removes both a value and its lease. The former holder
// cannot publish after this operation, or unlock a replacement holder's lease.
// Call after the database transaction commits, including all alternate writers.
func (c *Cache) Invalidate(ctx context.Context, keys ...string) error {
	if c == nil || c.client == nil {
		return nil
	}
	for _, key := range keys {
		valueKey, lockKey := c.keys(key)
		opCtx, cancel := context.WithTimeout(ctx, c.operationTimeout)
		err := c.client.Del(opCtx, valueKey, lockKey).Err()
		cancel()
		if err != nil {
			c.failed("invalidate")
			return errors.New("readcache: invalidation unavailable")
		}
		c.invalidations.Add(1)
	}
	return nil
}

var generations = redis.NewScript(`
local result = {}
for i, key in ipairs(KEYS) do
  local value = redis.call('GET', key)
  if not value then
    value = ARGV[1] .. ':' .. i
    redis.call('SET', key, value, 'PX', ARGV[2])
  else
    redis.call('PEXPIRE', key, ARGV[2])
  end
  result[i] = value
end
return result`)

var invalidateTags = redis.NewScript(`
for i, key in ipairs(KEYS) do
  redis.call('SET', key, ARGV[1] .. ':' .. i, 'PX', ARGV[2])
end
return 1`)

func (c *Cache) tagKeys(tags []string) []string {
	sorted := append([]string(nil), tags...)
	sort.Strings(sorted)
	keys := make([]string, 0, len(sorted))
	for i, tag := range sorted {
		if i > 0 && tag == sorted[i-1] {
			continue
		}
		hash := sha256.Sum256([]byte(tag))
		keys = append(keys, c.namespace+"tag:"+hex.EncodeToString(hash[:]))
	}
	return keys
}

func (c *Cache) generation(ctx context.Context, keys []string) (string, error) {
	values, err := c.run(ctx, generations, keys, nonce(), tagTTL.Milliseconds()).StringSlice()
	if err != nil {
		c.failed("generation")
		return "", err
	}
	encoded, _ := json.Marshal(values)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

// GetTagged supports invalidating multiple aliases through shared domain tags.
// Generations are random, so expired or evicted generation keys cannot resurrect
// old entries. Both cache hits and fills are checked against current generations.
func GetTagged[T any](ctx context.Context, c *Cache, key string, ttl time.Duration, tags []string, load func(context.Context) (T, error)) (T, error) {
	if c == nil || len(tags) == 0 {
		return Get(ctx, c, key, ttl, load)
	}
	if !c.available() {
		return Get(ctx, c, key, 0, load)
	}
	tagKeys := c.tagKeys(tags)
	for {
		before, err := c.generation(ctx, tagKeys)
		if err != nil {
			return Get(ctx, c, key, 0, load)
		}
		value, err := Get(ctx, c, key+":tags:"+before, ttl, load)
		if err != nil {
			return value, err
		}
		after, err := c.generation(ctx, tagKeys)
		if err != nil {
			return Get(ctx, c, key, 0, load)
		}
		if before == after {
			return value, nil
		}
		if err := ctx.Err(); err != nil {
			var zero T
			return zero, err
		}
	}
}

func (c *Cache) InvalidateTags(ctx context.Context, tags ...string) error {
	if c == nil || c.client == nil || len(tags) == 0 {
		return nil
	}
	if err := c.run(ctx, invalidateTags, c.tagKeys(tags), nonce(), tagTTL.Milliseconds()).Err(); err != nil {
		c.failed("invalidate_tags")
		return errors.New("readcache: tag invalidation unavailable")
	}
	c.invalidations.Add(1)
	return nil
}

func randomByte() byte { var b [1]byte; _, _ = rand.Read(b[:]); return b[0] }
func nonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("readcache: random source failed: %v", err))
	}
	return hex.EncodeToString(b[:])
}
