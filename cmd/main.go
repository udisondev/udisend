package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"
	"udisend/internal/ctxtool"
	"udisend/internal/logger"
	"udisend/internal/node"
	"udisend/pkg/crypt"
)

var listenPort = flag.String("listen_port", "", "port for receiving new connection")
var privateAuthKeyPath = flag.String("private_auth", "", "path to your private auth ecdsa key")
var publicAuthKeyPath = flag.String("public_auth", "", "path to your public auth ecdsa key")
var chatPort = flag.String("chat_port", ":9000", "port for you chat")
var memberID = flag.String("member_id", "", "your memberID")
var entryPoint = flag.String("entry_point", "", "chat entrypoint")

func main() {
	flag.Parse()
	*memberID = strings.TrimSpace(*memberID)
	if *memberID == "" {
		*memberID = rand.Text()
	}

	privSignKey, pubSignKey, err := crypt.LoadOrGenerateKeys(*privateAuthKeyPath, *publicAuthKeyPath)
	if err != nil {
		log.Fatalf("error load or generate keys: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ctx = ctxtool.Span(ctx, "main")
	logger.Debugf(
		ctx,
		`Start server with
		<listen_port:%s>
		<chat_port:%s>
		<member_id:%s>
		<entry_point:%s>`,
		*listenPort,
		*chatPort,
		*memberID,
		*entryPoint,
	)

	killSig := make(chan os.Signal, 1)
	signal.Notify(killSig, os.Interrupt, os.Kill)

	go func() {
		<-killSig
		cancel()
		time.Sleep(5 * time.Second)
		os.Exit(0)
	}()

	fmt.Printf("Wellcome %s!\n", *memberID)

	n := node.New(*memberID, privSignKey, pubSignKey, *chatPort, *listenPort)

	wg := sync.WaitGroup{}
	wg.Add(1)

	go func() {
		defer wg.Done()
		n.Run(ctx)
	}()

	if *entryPoint != "" {
		err := n.AttachHead(ctx, *entryPoint)
		if err != nil {
			logger.Errorf(ctx, "n.AttachHead: %v", err)
			cancel()
		}
	}

	wg.Wait()

}
