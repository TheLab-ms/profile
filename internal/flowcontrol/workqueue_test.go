package flowcontrol

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAddingSingleItem(t *testing.T) {
	q := NewQueue[string]()
	q.Add("item1")

	key := q.Get()
	if key != "item1" {
		t.Errorf("Expected 'item1', got %s", key)
	}
}

func TestAddMultipleItems(t *testing.T) {
	q := NewQueue[string]()
	q.Add("item1")
	q.Add("item2")

	key1 := q.Get()
	key2 := q.Get()

	if key1 == key2 {
		t.Errorf("Expected different items, got the same item twice: %s", key1)
	}
	if (key1 != "item1" && key1 != "item2") || (key2 != "item1" && key2 != "item2") {
		t.Errorf("Unexpected items: %s, %s", key1, key2)
	}
}

func TestItemUniqueConstraint(t *testing.T) {
	q := NewQueue[string]()
	q.Add("item1")
	q.Add("item1") // This should be ignored
	assert.Len(t, q.items, 1)
}

func TestConcurrentAddAndRetrieve(t *testing.T) {
	q := NewQueue[string]()
	var wg sync.WaitGroup
	keys := []string{"item1", "item2", "item3"}

	for _, key := range keys {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			q.Add(key)
		}(key)
	}

	wg.Wait()

	for i := 0; i < len(keys); i++ {
		key := q.Get()
		if key != "item1" && key != "item2" && key != "item3" {
			t.Errorf("Unexpected key retrieved: %s", key)
		}
	}
}

func TestDoneFunctionality(t *testing.T) {
	q := NewQueue[string]()
	q.Add("item1")
	q.Done("item1")
	assert.Len(t, q.items, 0)
}

func TestExponentialBackoffFunction(t *testing.T) {
	q := NewQueue[string]()

	backoff := q.exponentialBackoff(1)
	if !approxDuration(backoff, time.Millisecond*100, 0.2) {
		t.Errorf("Expected backoff around 2s, got %v", backoff)
	}

	backoff = q.exponentialBackoff(2)
	if !approxDuration(backoff, time.Millisecond*200, 0.2) {
		t.Errorf("Expected backoff around 4s, got %v", backoff)
	}
}

func approxDuration(d1, d2 time.Duration, tolerance float64) bool {
	return math.Abs(float64(d1-d2)) <= tolerance*float64(d1)
}
