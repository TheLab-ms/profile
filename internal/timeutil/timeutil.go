package timeutil

import (
	"context"
	"math/rand"
	"time"
)

type Loop struct {
	Handler  func(context.Context)
	Interval time.Duration

	signal chan struct{}
}

func (l *Loop) Run(ctx context.Context) {
	l.init()

	ticker := time.NewTicker(Jitter(l.Interval))
	defer ticker.Stop()
	for {
		l.Handler(ctx)

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
		ticker.Reset(Jitter(l.Interval))

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
