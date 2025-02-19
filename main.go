package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
)

var addr = flag.String("addr", ":8080", "http service address")
var memberID = flag.String("member_id", "", "your memberID")
var entryPoint = flag.String("entry_point", "", "chat entrypoint")

func main() {
	flag.Parse()
	*memberID = strings.TrimSpace(*memberID)
	if *memberID == "" {
		log.Fatal("member_id must be defined and not blank")
	}

	fmt.Printf("Wellcome %s!\n", *memberID)

	node := newNode()
	go node.run()

	go func() {
		keyboard := bufio.NewScanner(os.Stdin)
		text := keyboard.Text()
	}()
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWs(node, w, r)
	})
	err := http.ListenAndServe(*addr, nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
