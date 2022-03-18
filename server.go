package main

import (
	"io"
	"log"
	"net"
	"net/http"

	"github.com/gorilla/websocket"
)

var PortToServer = make(map[string]Server)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  BufSize,
	WriteBufferSize: BufSize,
	WriteBufferPool: WriteBufferPool,
}

type Server interface {
	Start()
	Addr() string
	CloneWithNewAddress(bindAddress string) Server
}

type server struct {
	bindAddress   string
	serverHandler ServerHandler
}

func (s *server) Start() {
	log.Println("New Server Listening on:", s.bindAddress)
	go func() {
		err := http.ListenAndServe(s.bindAddress, s.serverHandler)
		if err != nil {
			log.Println(err)
		}
	}()
}

func (c *server) Addr() string {
	return c.bindAddress
}

func (s *server) CloneWithNewAddress(bindAddress string) Server {
	return &server{
		bindAddress:   bindAddress,
		serverHandler: s.serverHandler,
	}
}

type ServerHandler http.Handler

type normalServerHandler struct {
	DestAddress string
}

func (s *normalServerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !websocket.IsWebSocketUpgrade(r) {
		closeTcpHandle(w, r)
		return
	}

	log.Println("Incoming --> ", r.RemoteAddr, r.Header, s.DestAddress)

	ch := make(chan net.Conn)
	defer func() {
		if i, ok := <-ch; ok {
			_ = i.Close()
		}
	}()

	edBuf, responseHeader := decodeXray0rtt(r.Header)

	go func() {
		defer close(ch)
		tcp, err := net.Dial("tcp", s.DestAddress)
		if err != nil {
			log.Println(err)
			return
		}

		if len(edBuf) > 0 {
			_, err = tcp.Write(edBuf)
			if err != nil {
				log.Println(err)
				return
			}
		}
		ch <- tcp
	}()

	ws, err := upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		log.Println(err)
		return
	}
	defer ws.Close()

	tcp, ok := <-ch
	if !ok {
		return
	}
	defer tcp.Close()

	TunnelTcpWs(tcp, ws)
}

type internalServerHandler struct {
	DestAddress string
	Proxy       string
	Client      Client
}

func (s *internalServerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !websocket.IsWebSocketUpgrade(r) {
		closeTcpHandle(w, r)
		return
	}

	log.Println("Incoming --> ", r.RemoteAddr, r.Header, " --> ( [Client]", s.DestAddress, s.Proxy, ") --> ", s.Client.Target())

	ch := make(chan io.Closer)
	defer func() {
		if i, ok := <-ch; ok {
			_ = i.Close()
		}
	}()

	edBuf, responseHeader := decodeXray0rtt(r.Header)

	go func() {
		defer close(ch)
		// send inHeader to client for Xray's 0rtt ws
		ws2, err := s.Client.Dial(edBuf, r.Header)
		if err != nil {
			log.Println(err)
			return
		}
		ch <- ws2
	}()

	ws, err := upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		log.Println(err)
		return
	}
	defer ws.Close()
	source := ws.UnderlyingConn()

	ws2, ok := <-ch
	if !ok {
		return
	}
	defer ws2.Close()
	target := s.Client.ToRawConn(ws2)

	TunnelTcpTcp(target, source)
}

func closeTcpHandle(writer http.ResponseWriter, request *http.Request) {
	h, ok := writer.(http.Hijacker)
	if !ok {
		return
	}
	netConn, _, err := h.Hijack()
	if err != nil {
		return
	}
	_ = netConn.Close()
}

func BuildServer(config ServerConfig) {
	mux := http.NewServeMux()
	hadRoot := false
	for port, client := range PortToClient {
		wsPath := client.GetServerWSPath()
		if len(wsPath) > 0 {
			config.Target = append(config.Target, ServerTargetConfig{
				WSPath:        wsPath,
				TargetAddress: net.JoinHostPort("127.0.0.1", port),
			})
		}
	}
	for _, target := range config.Target {
		if len(target.WSPath) == 0 {
			target.WSPath = "/"
		}
		host, port, err := net.SplitHostPort(target.TargetAddress)
		if err != nil {
			log.Println(err)
		}
		var sh ServerHandler
		client, ok := PortToClient[port]
		if ok && (host == "127.0.0.1" || host == "localhost") {
			log.Println("Short circuit replace (",
				target.WSPath, "<->", target.TargetAddress,
				") to (",
				target.WSPath, "<->", client.Target(), client.Proxy(),
				")")
			sh = &internalServerHandler{
				DestAddress: target.TargetAddress,
				Proxy:       client.Proxy(),
				Client:      client,
			}
		} else {
			sh = &normalServerHandler{
				DestAddress: target.TargetAddress,
			}

		}
		if target.WSPath == "/" {
			hadRoot = true
		}
		mux.Handle(target.WSPath, sh)
	}
	if !hadRoot {
		mux.HandleFunc("/", closeTcpHandle)
	}
	var s Server
	s = &server{
		bindAddress:   config.BindAddress,
		serverHandler: mux,
	}
	_, port, err := net.SplitHostPort(config.BindAddress)
	if err != nil {
		log.Println(err)
	}
	PortToServer[port] = s
}

func StartServers() {
	for _, server := range PortToServer {
		server.Start()
	}
}
