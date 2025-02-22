package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"udisend/internal"
	"udisend/internal/message"
)

var addr = flag.String("addr", "", "http service address")
var memberID = flag.String("member_id", "", "your memberID")
var entryPoint = flag.String("entry_point", "", "chat entrypoint")

func main() {
	flag.Parse()
	*memberID = strings.TrimSpace(*memberID)
	if *memberID == "" {
		log.Fatal("member_id must be defined and not blank")
	}

	fmt.Printf("Wellcome %s!\n", *memberID)

	wg := sync.WaitGroup{}

	n := node.New(*memberID)

	wg.Add(1)
	go func() {
		defer wg.Done()
		n.Run()
	}()

	fmt.Printf("entrypoint is: %s\n", *entryPoint)
	if *entryPoint != "" {
		n.AttachHead(*entryPoint)
	}

	keyboard := bufio.NewScanner(os.Stdin)
	fmt.Println("Чтобы отправить личное сообщение введите: /<recepient> ваше сообщение")
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			keyboard.Scan()
			text := keyboard.Text()
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
			err := n.Send(message.Outcome{
				To: recepient,
				Message: message.Message{
					Type: message.ForYou,
					Text: text[del+1:],
				},
			})
			if err != nil {
				log.Println("Error sending message", err.Error())
			}
		}
	}()

	if *addr != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()

			http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
				node.ServeWs(n, w, r)
			})
			err := http.ListenAndServe(*addr, nil)
			if err != nil {
				log.Fatal("ListenAndServe: ", err)
			}
		}()
	}

	wg.Wait()

}
