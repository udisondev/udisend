package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	"udisend/config"
	"udisend/internal/node"
)

func main() {
	cfg := config.GetConfig()

	sigs := make(chan os.Signal, 1)
	ctx, cancel := context.WithCancelCause(context.Background())
	go func() {
		sig := <-sigs
		fmt.Println("Получен сигнал:", sig)
		// Здесь можно выполнить все необходимые действия для graceful shutdown:
		// закрыть соединения, завершить горутины, освободить ресурсы и т.д.
		fmt.Println("Выполняется корректное завершение работы...")
		cancel(errors.New("shutdown"))
		time.Sleep(5 * time.Second)
		fmt.Println("Завершение работы приложения")
		os.Exit(0)
	}()
	// Подписываемся на SIGINT и SIGTERM.
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	n := node.New(ctx, *cfg)

	wg := sync.WaitGroup{}

	wg.Add(1)
	go func() {
		defer wg.Done()
		n.Serve(ctx)
	}()

	if !cfg.IsTURN {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n.AttachHead(ctx)
		}()
	}

	wg.Wait()
}
