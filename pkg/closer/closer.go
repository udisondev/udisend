package closer

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
	"udisend/pkg/logger"
	"udisend/pkg/span"
)

type closer struct {
	fns []func() error
}

var (
	globalCloser = closer{}
	mu           = sync.Mutex{}
)

func init() {
	killSign := make(chan os.Signal, 1)
	signal.Notify(killSign, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-killSign
		ctx := span.Init("kill sitnal")
		wg := sync.WaitGroup{}
		for _, fn := range globalCloser.fns {
			wg.Add(1)
			go func() {
				defer func() {
					wg.Done()
					recover()
				}()
				err := fn()
				if err != nil {
					logger.Errorf(ctx, err.Error())
				}
			}()
		}
		wg.Wait()
	}()
}

func Add(fn func() error) {
	mu.Lock()
	defer mu.Unlock()
	globalCloser.fns = append(globalCloser.fns, fn)
}
