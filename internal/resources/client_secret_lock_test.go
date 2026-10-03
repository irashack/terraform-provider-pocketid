package resources

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// One client's secret sequences run one at a time; another client's run
// alongside them.
func TestLockClientSecrets(t *testing.T) {
	var inside, most atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := lockClientSecrets("lock-test-client")
			defer unlock()
			now := inside.Add(1)
			for {
				seen := most.Load()
				if now <= seen || most.CompareAndSwap(seen, now) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			inside.Add(-1)
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), most.Load(), "one sequence at a time per client")

	unlock := lockClientSecrets("lock-test-client-a")
	done := make(chan struct{})
	go func() {
		release := lockClientSecrets("lock-test-client-b")
		release()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("another client's lock waited for this one")
	}
	unlock()
}
