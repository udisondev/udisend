package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"udisend/config"
	"udisend/internal/message"
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

	input := make(chan message.Outcome)
	defer close(input)

	wg.Add(1)
	go func() {
		defer wg.Done()
		n.ListenMe(input)
	}()

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("Чтобы отправить личное сообщение введите: /<recepient> ваше сообщение")
	wg.Add(1)
	go func() {
		for {
			scanner.Scan()
			text := scanner.Text()
			if len(text) < 4 {
				fmt.Println("Ваше сообщение не может быть короче 4х символов!")
				continue
			}
			if !strings.HasPrefix(text, "/") {
				fmt.Println("Ваше сообщение должно начинаться с '/'!")
				continue
			}
			del := strings.Index(text, " ")
			if del == -1 {
				fmt.Println("После '/<recepient' должен быть пробел и ваше сообщение!")
				continue
			}
			if len(text[del:]) < 2 {
				fmt.Println("Сообщение должно быть не пустым!")
				continue
			}
			recepient := text[1:del]
			fmt.Print("\033[1A\033[2K")
			fmt.Printf("You for %s: %s\n", recepient, text[del+1:])
			input <- message.Outcome{
				To:   recepient,
				Text: text[del+1:],
			}
		}
	}()

	wg.Wait()
}
