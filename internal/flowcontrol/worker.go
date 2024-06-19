package flowcontrol

import (
	"context"
	"log"
)

// TODO: Support graceful shutdown

func RunWorker[T comparable](ctx context.Context, queue *Queue[T], fn func(T) error) {
	for {
		item := queue.Get()
		err := fn(item)
		if err == nil {
			queue.Done(item)
			continue
		}
		log.Printf("error syncing resource %v: %s", item, err)
		queue.Retry(item)
	}
}
