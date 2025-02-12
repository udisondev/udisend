package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
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

	node.New(*cfg).Serve(ctx)
}
