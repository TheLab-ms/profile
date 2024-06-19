package flowcontrol

import (
	"container/heap"
	"context"
	"math"
	"math/rand"
	"sync"
	"time"
)

type QueueItem[T comparable] struct {
	key       T
	attempts  int
	nextRetry time.Time
}

type Queue[T comparable] struct {
	mu    sync.Mutex
	cond  *sync.Cond
	items map[T]*QueueItem[T]
	heap  *priorityQueue[T]
}

func NewQueue[T comparable]() *Queue[T] {
	q := &Queue[T]{
		items: make(map[T]*QueueItem[T]),
		heap:  &priorityQueue[T]{},
	}
	heap.Init(q.heap)
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *Queue[T]) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Millisecond * 100)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			q.mu.Lock()
			if q.heap.Len() == 0 {
				q.mu.Unlock()
				continue
			}

			nextItem := (*q.heap)[0]
			delta := nextItem.nextRetry.Sub(time.Now())
			if delta > 0 {
				ticker.Reset(delta)
				q.mu.Unlock()
				continue
			}

			q.cond.Signal()
			q.mu.Unlock()
		}
	}
}

func (q *Queue[T]) Add(key T) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, exists := q.items[key]; !exists {
		item := &QueueItem[T]{key: key, attempts: 0}
		q.items[key] = item
		heap.Push(q.heap, item)
		q.cond.Signal()
	}
}

func (q *Queue[T]) Done(key T) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if item, exists := q.items[key]; exists {
		delete(q.items, key)
		q.removeFromHeap(item)
	}
}

func (q *Queue[T]) Get() T {
	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		if q.heap.Len() == 0 {
			q.cond.Wait()
		} else {
			item := heap.Pop(q.heap).(*QueueItem[T])
			if item.nextRetry.Before(time.Now()) {
				return item.key
			}
			heap.Push(q.heap, item)
			q.cond.Wait()
		}
	}
}

func (q *Queue[T]) Retry(key T) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if item, exists := q.items[key]; exists {
		item.attempts++
		item.nextRetry = time.Now().Add(q.exponentialBackoff(item.attempts))
		heap.Push(q.heap, item)
		q.cond.Signal()
	}
}

func (q *Queue[T]) exponentialBackoff(attempts int) time.Duration {
	backoff := float64(time.Millisecond * 50)
	jitter := backoff * 0.1
	factor := math.Pow(2, float64(attempts))
	return time.Duration(backoff*factor + jitter*factor*0.5*rand.Float64())
}

func (q *Queue[T]) removeFromHeap(item *QueueItem[T]) {
	for i, heapItem := range *q.heap {
		if heapItem == item {
			heap.Remove(q.heap, i)
			break
		}
	}
}

type priorityQueue[T comparable] []*QueueItem[T]

func (pq priorityQueue[T]) Len() int { return len(pq) }
func (pq priorityQueue[T]) Less(i, j int) bool {
	return pq[i].nextRetry.Before(pq[j].nextRetry)
}
func (pq priorityQueue[T]) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
}
func (pq *priorityQueue[T]) Push(x interface{}) {
	item := x.(*QueueItem[T])
	*pq = append(*pq, item)
}
func (pq *priorityQueue[T]) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	*pq = old[0 : n-1]
	return item
}
