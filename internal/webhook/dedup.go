package webhook

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/slvic/rca-agent/internal/report"
)

type Deduplicator struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
}

func NewDeduplicator(ttl time.Duration) *Deduplicator {
	d := &Deduplicator{
		seen: make(map[string]time.Time),
		ttl:  ttl,
	}
	go d.cleanup()
	return d
}

func alertKey(a report.Alert) string {
	keys := make([]string, 0, len(a.Labels))
	for k, v := range a.Labels {
		keys = append(keys, k+"="+v)
	}
	sort.Strings(keys)
	return a.Name + "|" + strings.Join(keys, ",")
}

// IsDuplicate returns true if the alert was already seen within the TTL window.
func (d *Deduplicator) IsDuplicate(a report.Alert) bool {
	key := alertKey(a)
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[key]; ok {
		return true
	}
	d.seen[key] = time.Now()
	return false
}

func (d *Deduplicator) cleanup() {
	ticker := time.NewTicker(d.ttl / 2)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		d.mu.Lock()
		for k, t := range d.seen {
			if now.Sub(t) > d.ttl {
				delete(d.seen, k)
			}
		}
		d.mu.Unlock()
	}
}
