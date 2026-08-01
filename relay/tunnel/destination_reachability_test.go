package tunnel

import (
	"net"
	"testing"
	"time"
)

func TestDestinationReachabilityCacheBoundsRepeatedTimeouts(t *testing.T) {
	now := time.Unix(100, 0)
	cache := newDestinationReachabilityCache()
	cache.now = func() time.Time { return now }
	addr := "unreachable.example:443"
	timeoutErr := &net.DNSError{Err: "timeout", IsTimeout: true}

	if allowed, _, _ := cache.allow(addr); !allowed {
		t.Fatal("new destination was blocked")
	}
	if opened, _ := cache.recordFailure(addr, timeoutErr); opened {
		t.Fatal("one timeout opened the circuit")
	}
	opened, cooldown := cache.recordFailure(addr, timeoutErr)
	if !opened || cooldown != destinationCooldownBase {
		t.Fatalf("second timeout opened=%t cooldown=%s", opened, cooldown)
	}
	allowed, retryAfter, logSkip := cache.allow(addr)
	if allowed || !logSkip || retryAfter != destinationCooldownBase {
		t.Fatalf("blocked allow=%t retry=%s log=%t", allowed, retryAfter, logSkip)
	}
	if _, _, duplicateLog := cache.allow(addr); duplicateLog {
		t.Fatal("same cooldown produced duplicate diagnostic")
	}

	now = now.Add(destinationCooldownBase)
	if allowed, _, _ := cache.allow(addr); !allowed {
		t.Fatal("destination remained blocked after bounded cooldown")
	}
	cache.recordSuccess(addr)
	if allowed, _, _ := cache.allow(addr); !allowed {
		t.Fatal("successful destination retained negative cache")
	}
}

func TestDestinationReachabilityCacheIgnoresNonTimeoutErrors(t *testing.T) {
	cache := newDestinationReachabilityCache()
	addr := "example.invalid:443"
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "refused"}}
	for i := 0; i < 4; i++ {
		if opened, _ := cache.recordFailure(addr, refused); opened {
			t.Fatal("non-timeout error opened the circuit")
		}
	}
	if allowed, _, _ := cache.allow(addr); !allowed {
		t.Fatal("non-timeout errors blocked destination")
	}
}
