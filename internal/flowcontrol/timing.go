package flowcontrol

import (
	"context"
	"math"
	"math/rand"
	"time"
)

type LoopTickHandler func(context.Context) time.Duration

func RetryHandler(interval time.Duration, fn func(context.Context) bool) LoopTickHandler {
	const base = time.Millisecond * 100
	failures := 0
	return func(ctx context.Context) time.Duration {
		ok := fn(ctx)
		if ok {
			return interval
		}

		failures++
		return base * time.Duration(failures)
	}
}

type Loop struct {
	Handler LoopTickHandler
	signal  chan struct{}
}

func (l *Loop) Run(ctx context.Context) {
	l.init()

	ticker := time.NewTicker(Jitter(100 * time.Millisecond)) // almost exactly at startup
	defer ticker.Stop()
	for {
		wait := l.Handler(ctx)

		// Drain the timer so we can reset it now that the handler has returned
		ticker.Stop()
		for {
			select {
			case <-ticker.C:
				continue
			default:
			}
			break
		}
		ticker.Reset(Jitter(wait))

		// Wait for the next tick
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

// Kick makes the handler run NOW (if it isn't already running)
func (l *Loop) Kick() {
	l.init()
	select {
	case l.signal <- struct{}{}:
	default:
	}
}

func (l *Loop) init() {
	if l.signal == nil {
		l.signal = make(chan struct{}, 1)
	}
}

func Jitter(d time.Duration) time.Duration {
	jitter := float64(d) * 0.1
	jitterValue := (rand.Float64()*2 - 1) * jitter
	return d + time.Duration(jitterValue)
}

func exponentialBackoff(attempts int) time.Duration {
	backoff := float64(time.Millisecond * 50)
	jitter := backoff * 0.1
	factor := math.Pow(2, float64(attempts))
	return time.Duration(backoff*factor + jitter*factor*0.5*rand.Float64())
}
