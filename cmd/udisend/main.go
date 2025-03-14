package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"log"
	"net/http"
	"runtime"
	"sync"
	"udisend/internal/network"
	"udisend/pkg/crypt"
	"udisend/pkg/logger"
	"udisend/pkg/span"
)

var listenPort = flag.String("lp", ":8000", "port for receiving new connection")
var entryPoint = flag.String("ep", "", "chat entrypoint")

func main() {
	ctx, stop := context.WithCancel(span.Init("main"))
	flag.Parse()

	privateAuth, publicAuth, err := crypt.LoadOrGenerateKeys("", "")
	if err != nil {
		log.Fatal(err)
	}

	pem, err := crypt.PublicKeyToPEM(publicAuth)
	if err != nil {
		log.Fatal(err)
	}
	pemBase64 := base64.StdEncoding.EncodeToString([]byte(pem))
	mesh := rand.Text() + pemBase64
	n := network.New(mesh, privateAuth, runtime.NumCPU(), 10)
	wg := sync.WaitGroup{}
	wg.Add(1)
	go n.Run(ctx)
	wg.Wait()

	go func() {
		// Создаем отдельный multiplexer для этого сервера
		muxWs := http.NewServeMux()
		muxWs.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
			n.ServeWs(w, r)
		})
		muxWs.HandleFunc("/id", func(w http.ResponseWriter, r *http.Request) {
			logger.Debugf(ctx, "ID requested <ID:%s>", mesh)
			w.Write([]byte(mesh))
		})

		logger.Infof(ctx, "Stat listening %s")
		if err := http.ListenAndServe(*listenPort, muxWs); err != nil {
			logger.Errorf(ctx, "Error listening <addr:%s> %v", listenPort, err)
		}
	}()

	if *entryPoint != "" {
		err := n.AttachHead(*entryPoint)
		if err != nil {
			stop()
		}
	}

}
