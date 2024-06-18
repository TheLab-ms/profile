package main

import (
	"container/heap"
	"context"
	"math"
	"math/rand"
	"sync"
	"time"
)

type QueueItem struct {
	key       string
	attempts  int
	nextRetry time.Time
}

type Queue struct {
	mu    sync.Mutex
	cond  *sync.Cond
	items map[string]*QueueItem
	heap  *priorityQueue
}

func NewQueue() *Queue {
	q := &Queue{
		items: make(map[string]*QueueItem),
		heap:  &priorityQueue{},
	}
	heap.Init(q.heap)
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *Queue) Run(ctx context.Context) {
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

func (q *Queue) Add(key string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, exists := q.items[key]; !exists {
		item := &QueueItem{key: key, attempts: 0}
		q.items[key] = item
		heap.Push(q.heap, item)
		q.cond.Signal()
	}
}

func (q *Queue) Done(key string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if item, exists := q.items[key]; exists {
		delete(q.items, key)
		q.removeFromHeap(item)
	}
}

func (q *Queue) Get() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		if q.heap.Len() == 0 {
			q.cond.Wait()
		} else {
			item := heap.Pop(q.heap).(*QueueItem)
			if item.nextRetry.Before(time.Now()) {
				delete(q.items, item.key)
				return item.key
			}
			heap.Push(q.heap, item)
			q.cond.Wait()
		}
	}
}

func (q *Queue) Retry(key string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if item, exists := q.items[key]; exists {
		item.attempts++
		item.nextRetry = time.Now().Add(q.exponentialBackoff(item.attempts))
		heap.Push(q.heap, item)
		q.cond.Signal()
	}
}

func (q *Queue) exponentialBackoff(attempts int) time.Duration {
	backoff := float64(time.Second)
	jitter := backoff * 0.1
	factor := math.Pow(2, float64(attempts))
	return time.Duration(backoff*factor + jitter*factor*0.5*rand.Float64())
}

func (q *Queue) removeFromHeap(item *QueueItem) {
	for i, heapItem := range *q.heap {
		if heapItem == item {
			heap.Remove(q.heap, i)
			break
		}
	}
}

type priorityQueue []*QueueItem

func (pq priorityQueue) Len() int { return len(pq) }
func (pq priorityQueue) Less(i, j int) bool {
	return pq[i].nextRetry.Before(pq[j].nextRetry)
}
func (pq priorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
}
func (pq *priorityQueue) Push(x interface{}) {
	item := x.(*QueueItem)
	*pq = append(*pq, item)
}
func (pq *priorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	*pq = old[0 : n-1]
	return item
}
