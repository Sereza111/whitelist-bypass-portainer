package tunnel

import (
	"errors"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	destinationTimeoutThreshold = 2
	destinationCooldownBase     = 2 * time.Second
	destinationCooldownMax      = 15 * time.Second
	destinationEntryTTL         = 5 * time.Minute
	destinationEntryLimit       = 1024
)

type destinationReachabilityEntry struct {
	timeouts     int
	blockedUntil time.Time
	lastUpdated  time.Time
	skipLogged   bool
}

// destinationReachabilityCache is a short negative cache for endpoints which
// repeatedly time out from Creator egress. It never rewrites destinations and
// never caches a successful TCP connection. Its only purpose is to return a
// quick SOCKS failure while an application chooses another endpoint instead of
// launching dozens of identical ten-second dials.
type destinationReachabilityCache struct {
	mu      sync.Mutex
	entries map[string]destinationReachabilityEntry
	now     func() time.Time
}

func newDestinationReachabilityCache() *destinationReachabilityCache {
	return &destinationReachabilityCache{
		entries: make(map[string]destinationReachabilityEntry),
		now:     time.Now,
	}
}

func (c *destinationReachabilityCache) allow(addr string) (allowed bool, retryAfter time.Duration, logSkip bool) {
	if c == nil {
		return true, 0, false
	}
	key := normalizeDestinationKey(addr)
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return true, 0, false
	}
	if now.Sub(entry.lastUpdated) > destinationEntryTTL {
		delete(c.entries, key)
		return true, 0, false
	}
	if !now.Before(entry.blockedUntil) {
		return true, 0, false
	}
	retryAfter = entry.blockedUntil.Sub(now)
	logSkip = !entry.skipLogged
	entry.skipLogged = true
	c.entries[key] = entry
	return false, retryAfter, logSkip
}

func (c *destinationReachabilityCache) recordSuccess(addr string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.entries, normalizeDestinationKey(addr))
	c.mu.Unlock()
}

func (c *destinationReachabilityCache) recordFailure(addr string, err error) (opened bool, cooldown time.Duration) {
	if c == nil || !isNetworkTimeout(err) {
		return false, 0
	}
	key := normalizeDestinationKey(addr)
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	entry.timeouts++
	entry.lastUpdated = now
	if entry.timeouts >= destinationTimeoutThreshold {
		shift := entry.timeouts - destinationTimeoutThreshold
		if shift > 3 {
			shift = 3
		}
		cooldown = destinationCooldownBase * time.Duration(1<<shift)
		if cooldown > destinationCooldownMax {
			cooldown = destinationCooldownMax
		}
		opened = entry.timeouts == destinationTimeoutThreshold
		entry.blockedUntil = now.Add(cooldown)
		entry.skipLogged = false
	}
	c.entries[key] = entry
	if len(c.entries) > destinationEntryLimit {
		c.pruneLocked(now)
	}
	return opened, cooldown
}

func (c *destinationReachabilityCache) pruneLocked(now time.Time) {
	for key, entry := range c.entries {
		if now.Sub(entry.lastUpdated) > destinationEntryTTL {
			delete(c.entries, key)
		}
	}
	for len(c.entries) > destinationEntryLimit {
		for key := range c.entries {
			delete(c.entries, key)
			break
		}
	}
}

func normalizeDestinationKey(addr string) string {
	return strings.ToLower(strings.TrimSpace(addr))
}

func isNetworkTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
