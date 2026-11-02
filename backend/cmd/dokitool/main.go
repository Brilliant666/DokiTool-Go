// dokitool is the Go migration's explicit, independent verification service.
// Production remains PHP until the migration acceptance and rollback gates pass.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/api"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/catalog"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/media/weibo"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/bilibili"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/douyin"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/kuaishou"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/pipigx"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/pipixia"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/toutiao"
	weiboplatform "github.com/Brilliant666/DokiTool-Go/backend/internal/platform/weibo"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/xiaohongshu"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/zuiyou"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/probe"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

var version = "development"

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(args []string) error {
	command := "serve"
	if len(args) > 0 && (args[0] == "" || args[0][0] != '-') {
		command, args = args[0], args[1:]
	}
	if command == "version" {
		fmt.Println(version)
		return nil
	}
	if command == "validate" {
		body, err := io.ReadAll(io.LimitReader(os.Stdin, (8<<20)+1))
		if err != nil {
			return err
		}
		if len(body) > 8<<20 {
			return fmt.Errorf("input exceeds 8 MiB")
		}
		value, err := unified.Decode(body)
		if err != nil {
			return err
		}
		if err := unified.ValidateEnvelope(value); err != nil {
			return err
		}
		return emit(value)
	}
	c, err := catalog.Load()
	if err != nil {
		return err
	}
	if command == "catalog" {
		return emit(c.Envelope())
	}
	if command != "serve" {
		return fmt.Errorf("unknown command %q (serve, catalog, validate, version)", command)
	}
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := flags.String("listen", "127.0.0.1:9082", "loopback verification listener; production remains PHP")
	xhsTransport := flags.String("xhs-page-transport", "standard", "XHS HTTPS pages only: standard or chrome131 (opt-in verification)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	host, port, err := net.SplitHostPort(*listen)
	number, numberErr := strconv.Atoi(port)
	ip := net.ParseIP(host)
	if err != nil || numberErr != nil || number < 1024 || number > 65535 || ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen must use an explicit loopback IP and port 1024–65535")
	}
	xhsExecutor, err := newXHSExecutor(*xhsTransport)
	if err != nil {
		return err
	}
	executor := fetch.New()
	adapters := map[string]api.Adapter{
		"douyin":      douyin.New(executor, probe.New(executor, douyin.AllowedMediaURL)),
		"bilibili":    bilibili.New(executor),
		"weibo":       weiboplatform.New(executor),
		"toutiao":     toutiao.New(executor),
		"kuaishou":    kuaishou.New(executor),
		"pipixia":     pipixia.New(executor),
		"pipigx":      pipigx.New(executor),
		"zuiyou":      zuiyou.New(executor),
		"xiaohongshu": xiaohongshu.New(xhsExecutor),
	}
	handler, err := api.New(c, adapters, time.Now, weiboplatform.PreparePublic(os.Getenv("SNAP_MEDIA_SIGNING_KEY")))
	if err != nil {
		return err
	}
	// The route is present even without a configured key, where it returns the
	// PHP-compatible legacy 503 media envelope without issuing network calls.
	handler.SetWeiboRelay(weibo.NewMediaRelay(os.Getenv("SNAP_MEDIA_SIGNING_KEY")))
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 50 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	log.Printf("Go migration verification server %s listening on %s", version, *listen)
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 310*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

func newXHSExecutor(mode string) (fetch.Executor, error) {
	switch mode {
	case "standard":
		return fetch.New(), nil
	case "chrome131":
		return fetch.NewXiaohongshuPages(), nil
	default:
		return nil, fmt.Errorf("xhs-page-transport must be standard or chrome131")
	}
}

func emit(value any) error {
	encoded, err := unified.Encode(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, string(encoded))
	return err
}
