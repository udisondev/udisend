package broadcast

import (
	"context"
	"sync"
	"udisend/event"
)

type ClusterBroadcast struct {
	mu   sync.Mutex
	subs map[chan event.Event]struct{}
}

func NewClusterBroadcast() *ClusterBroadcast {
	return &ClusterBroadcast{mu: sync.Mutex{}, subs: make(map[chan event.Event]struct{})}
}

func (cb *ClusterBroadcast) Chan(ctx context.Context) (chan<- event.Event, func(ctx context.Context) <-chan event.Event) {
	broadcast := make(chan event.Event)
	go func() {
		<-ctx.Done()
		close(broadcast)
	}()
	return broadcast, func(ctx context.Context) <-chan event.Event {
		sub := make(chan event.Event)
		cb.mu.Lock()
		cb.subs[sub] = struct{}{}
		cb.mu.Unlock()

		go func() {
			<-ctx.Done()
			cb.mu.Lock()
			delete(cb.subs, sub)
			cb.mu.Unlock()
			close(sub)
		}()

		return sub
	}

}
