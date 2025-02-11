package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"udisend/config"
	"udisend/node"

	"github.com/gorilla/websocket"
)

func main() {
	cfg := config.GetConfig()

	node.New(*cfg)
}

func (s *Server) serve() {
}
