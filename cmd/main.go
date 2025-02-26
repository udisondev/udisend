package main

import (
	"context"
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

var addr = flag.String("addr", "", "http service address")
var memberID = flag.String("member_id", "", "your memberID")
var entryPoint = flag.String("entry_point", "", "chat entrypoint")

func main() {
	flag.Parse()
	*memberID = strings.TrimSpace(*memberID)
	if *memberID == "" {
		log.Fatal("member_id must be defined and not blank")
	}

	privSignKey, pubSignKey, err := crypt.LoadOrGenerateKeys()
	if err != nil {
		log.Fatalf("error load or generate keys: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ctx = ctxtool.Span(ctx, "main")
	logger.Debugf(ctx, "Start server with <addr:%s> <member_id:%s> <entry_point:%s>", *addr, *memberID, *entryPoint)

	killSig := make(chan os.Signal, 1)
	signal.Notify(killSig, os.Interrupt, os.Kill)

	go func() {
		<-killSig
		cancel()
		time.Sleep(5 * time.Second)
		os.Exit(0)
	}()

	fmt.Printf("Wellcome %s!\n", *memberID)

	n := node.New(*memberID, privSignKey, pubSignKey, *addr)

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
