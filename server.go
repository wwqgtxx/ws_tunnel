package main

import (
	"encoding/base64"
	"github.com/gorilla/websocket"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
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

var replacer = strings.NewReplacer("+", "-", "/", "_", "=", "")

func decodeEd(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(replacer.Replace(s))
}

type ServerHandler http.Handler

type normalServerHandler struct {
	DestAddress string
}

func (s *normalServerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Println("Incoming --> ", r.RemoteAddr, r.Header, s.DestAddress)

	ch := make(chan net.Conn)
	defer func() {
		if i, ok := <-ch; ok {
			_ = i.Close()
		}
	}()

	var edBuf []byte
	responseHeader := http.Header{}
	// read inHeader's `Sec-WebSocket-Protocol` for Xray's 0rtt ws
	if secProtocol := r.Header.Get("Sec-WebSocket-Protocol"); len(secProtocol) > 0 {
		if buf, err := decodeEd(secProtocol); err == nil { // sure could base64 decode
			edBuf = buf
			responseHeader.Set("Sec-WebSocket-Protocol", secProtocol)
		}
	}

	go func() {
		defer close(ch)
		if !websocket.IsWebSocketUpgrade(r) {
			return
		}
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
	Client      Client
}

func (s *internalServerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Println("Incoming --> ", r.RemoteAddr, r.Header, " --> ( [Client]", s.DestAddress, ") --> ", s.Client.Target())

	ch := make(chan io.Closer)
	defer func() {
		if i, ok := <-ch; ok {
			_ = i.Close()
		}
	}()

	responseHeader := http.Header{}
	// read inHeader's `Sec-WebSocket-Protocol` for Xray's 0rtt ws
	if secProtocol := r.Header.Get("Sec-WebSocket-Protocol"); len(secProtocol) > 0 {
		if _, err := decodeEd(secProtocol); err == nil { // sure could base64 decode
			responseHeader.Set("Sec-WebSocket-Protocol", secProtocol)
		}
	}

	go func() {
		defer close(ch)
		if !websocket.IsWebSocketUpgrade(r) {
			return
		}
		// send inHeader to client for Xray's 0rtt ws
		ws2, err := s.Client.Dial(r.Header)
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

func BuildServer(config ServerConfig) {
	mux := http.NewServeMux()
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
				target.WSPath, "<->", client.Target(),
				")")
			sh = &internalServerHandler{
				DestAddress: target.TargetAddress,
				Client:      client,
			}
		} else {
			sh = &normalServerHandler{
				DestAddress: target.TargetAddress,
			}

		}
		mux.Handle(target.WSPath, sh)
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
