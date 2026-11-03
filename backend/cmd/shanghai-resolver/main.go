package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/douyin"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/probe"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/shresolver"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:9083", "explicit loopback address behind HTTPS ingress")
	cert := flag.String("tls-cert", "", "PEM certificate chain for native HTTPS")
	key := flag.String("tls-key", "", "PEM private key for native HTTPS")
	flag.Parse()
	if (*cert == "") != (*key == "") {
		log.Fatal("tls-cert and tls-key must be set together")
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || !net.ParseIP(host).IsLoopback() || flag.NArg() != 0 {
		log.Fatal("invalid loopback listener")
	}
	h, err := shresolver.NewHandler(os.Getenv("DOKITOOL_SHANGHAI_TOKEN"), probe.New(fetch.New(), douyin.AllowedMediaURL))
	if err != nil {
		log.Fatal(err)
	}
	s := http.Server{Addr: *listen, Handler: h, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 4 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8 << 10}
	if *cert != "" {
		log.Fatal(s.ListenAndServeTLS(*cert, *key))
	}
	log.Fatal(s.ListenAndServe())
}
