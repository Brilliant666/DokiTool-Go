// dokitool serves the production Go API behind an authenticated loopback edge.
// The frozen PHP tree is a behavior reference, not a runtime dependency.
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
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/instagram"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/kuaishou"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/pipigx"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/pipixia"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/toutiao"
	weiboplatform "github.com/Brilliant666/DokiTool-Go/backend/internal/platform/weibo"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/x"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/xiaohongshu"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/youtube"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/ytgeneric"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/zuiyou"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/probe"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/shresolver"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/ytengine"
)

// preparePublicMedia keeps platform-specific transformations at the public boundary.
func preparePublicMedia(key string) api.PublicMedia {
	weibo := weiboplatform.PreparePublic(key)
	return func(ctx context.Context, platform string, payload unified.Object, now int64) (unified.Object, error) {
		if platform == "douyin" {
			return douyin.PreparePublic(ctx, platform, payload, now)
		}
		if platform == "xiaohongshu" {
			return xiaohongshu.PreparePublic(ctx, platform, payload, now)
		}
		return weibo(ctx, platform, payload, now)
	}
}

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
	listen := flags.String("listen", "127.0.0.1:9082", "loopback API listener; use an authenticated edge for public traffic")
	xhsTransport := flags.String("xhs-page-transport", "standard", "XHS HTTPS pages only: standard or chrome131")
	xTombstoneFallback := flags.String("x-tombstone-fallback", x.TombstoneFallbackOff, "X TweetTombstone metadata fallback: off or fxtwitter")
	clientIdentity := flags.String("client-identity", "direct", "client identity for admission: direct or authenticated caddy")
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
	if err := x.ValidateTombstoneFallback(*xTombstoneFallback); err != nil {
		return err
	}
	cacheCfg, err := api.LoadCacheConfig(os.Getenv)
	if err != nil {
		return err
	}
	executor := fetch.New()
	japan := probe.New(executor, douyin.AllowedMediaURL)
	shanghai, control, err := configureShanghai(executor, os.Getenv)
	if err != nil {
		return err
	}
	douyinParser := douyin.New(executor, japan)
	if shanghai != nil {
		douyinParser = douyin.NewWithOriginalResolver(executor, japan.WithFingerprint(), shanghai)
	}
	pipigxSave, err := enableTBVideo("DOKITOOL_TBVIDEO_REWRITE_PIPIGX")
	if err != nil {
		return err
	}
	zuiyouSave, err := enableTBVideo("DOKITOOL_TBVIDEO_REWRITE_ZUIYOU")
	if err != nil {
		return err
	}
	adapters := map[string]api.Adapter{
		"douyin":      douyinParser,
		"bilibili":    bilibili.New(executor),
		"weibo":       weiboplatform.New(executor),
		"toutiao":     toutiao.New(executor),
		"kuaishou":    kuaishou.New(executor),
		"pipixia":     pipixia.New(executor),
		"pipigx":      pipigx.NewWithTBVideo(executor, pipigxSave),
		"zuiyou":      zuiyou.NewWithTBVideo(executor, zuiyouSave),
		"xiaohongshu": xiaohongshu.New(xhsExecutor),
	}
	if err := configureYTDLPWithOptions(c, adapters, executor, *xTombstoneFallback, os.Getenv); err != nil {
		return err
	}
	handler, err := api.NewWithCacheConfig(c, adapters, time.Now, preparePublicMedia(os.Getenv("SNAP_MEDIA_SIGNING_KEY")), cacheCfg)
	if err != nil {
		return err
	}
	if err := configureYouTubeHelper(c, handler, executor, os.Getenv); err != nil {
		return err
	}
	// The route is present even without a configured key, where it returns the
	// PHP-compatible legacy 503 media envelope without issuing network calls.
	handler.SetWeiboRelay(weibo.NewMediaRelay(os.Getenv("SNAP_MEDIA_SIGNING_KEY")))
	limits, err := api.LoadAdmissionConfig(os.Getenv)
	if err != nil {
		return err
	}
	entry, err := api.WithAdmissionConfig(handler, control, *clientIdentity, os.Getenv("SNAP_INGRESS_KEY"), limits)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: *listen, Handler: entry, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 50 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	log.Printf("DokiTool Go API %s listening on %s; admission=%+v", version, *listen, limits)
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

// An existing #27 endpoint remains opt-in for direct mode. Pull never invokes that endpoint.
func configureShanghai(executor fetch.Executor, getenv func(string) string) (douyin.OriginalResolver, http.Handler, error) {
	mode, endpoint := getenv("DOKITOOL_SHANGHAI_MODE"), getenv("DOKITOOL_SHANGHAI_ENDPOINT")
	switch mode {
	case "off":
		return nil, nil, nil
	case "", "direct":
		if mode == "" && endpoint == "" {
			return nil, nil, nil
		}
		client, err := shresolver.NewClient(endpoint, getenv("DOKITOOL_SHANGHAI_TOKEN"), executor)
		if err != nil {
			if mode == "" {
				// Retain the legacy fail-soft behavior for pre-mode deployments.
				log.Print("Shanghai original resolver disabled: invalid configuration")
				return nil, nil, nil
			}
			return nil, nil, err
		}
		return client, nil, nil
	case "pull":
		if endpoint != "" {
			return nil, nil, errors.New("remove DOKITOOL_SHANGHAI_ENDPOINT before enabling pull mode")
		}
		token := getenv("DOKITOOL_SHANGHAI_PULL_TOKEN")
		for _, key := range []string{"SNAP_INGRESS_KEY", "SNAP_MEDIA_SIGNING_KEY", "DOKITOOL_SHANGHAI_TOKEN"} {
			if token != "" && token == getenv(key) {
				return nil, nil, errors.New("pull mode requires a dedicated control credential")
			}
		}
		broker, err := shresolver.NewPullBroker(token)
		if err != nil {
			return nil, nil, err
		}
		return broker, broker, nil
	default:
		return nil, nil, errors.New("DOKITOOL_SHANGHAI_MODE must be off, direct or pull")
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

// The two deployment switches apply independently when the API process starts.
func enableTBVideo(key string) (bool, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return true, nil
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return enabled, nil
}

// configureYTDLPX optionally registers the X / Twitter, YouTube, Instagram, and
// DOKITOOL_YTDLP_SITES platforms when their respective switches are enabled
// and the shared yt-dlp binary preflight succeeds.
// Production defaults all switches to off.
func configureYTDLPX(c *catalog.Catalog, adapters map[string]api.Adapter, executor fetch.Executor, getenv func(string) string) error {
	return configureYTDLPWithOptions(c, adapters, executor, x.TombstoneFallbackOff, getenv)
}

func configureYTDLPWithOptions(c *catalog.Catalog, adapters map[string]api.Adapter, executor fetch.Executor, xTombstoneFallback string, getenv func(string) string) error {
	if err := x.ValidateTombstoneFallback(xTombstoneFallback); err != nil {
		return err
	}
	parseSwitch := func(key string) (bool, error) {
		raw := getenv(key)
		if raw == "" {
			return false, nil
		}
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return false, fmt.Errorf("%s must be a boolean", key)
		}
		return enabled, nil
	}

	xEnabled, err := parseSwitch("DOKITOOL_YTDLP_X_ENABLED")
	if err != nil {
		return err
	}
	ytEnabled, err := parseSwitch("DOKITOOL_YTDLP_YOUTUBE_ENABLED")
	if err != nil {
		return err
	}
	igEnabled, err := parseSwitch("DOKITOOL_YTDLP_INSTAGRAM_ENABLED")
	if err != nil {
		return err
	}
	genericSites, err := ytgeneric.ParseSitesEnv(getenv("DOKITOOL_YTDLP_SITES"))
	if err != nil {
		return err
	}
	if !xEnabled && !ytEnabled && !igEnabled && len(genericSites) == 0 {
		return nil
	}

	baseEngine, err := ytengine.NewFromEnv(getenv, nil)
	if err != nil || !baseEngine.Enabled() {
		log.Printf("dokitool: yt-dlp engine preflight failed (platforms not registered in catalog): %v", err)
		return nil
	}
	log.Printf("dokitool: yt-dlp shared engine verified (version=%s, asset=%s, sha256=%s)", ytengine.PinnedVersion, baseEngine.BinaryAsset(), baseEngine.BinarySHA256())

	if xEnabled {
		if err := c.Register(catalog.XPlatform()); err != nil {
			return err
		}
		xEngine := newXEngine(baseEngine)
		xParser, err := x.NewWithSizeProber(xEngine, x.NewSizeProber(executor)).
			WithFetcher(executor).
			WithTombstoneFallback(xTombstoneFallback)
		if err != nil {
			return err
		}
		adapters["x"] = xParser
	}
	if ytEnabled {
		if err := c.Register(catalog.YouTubePlatform()); err != nil {
			return err
		}
		ytEngine := baseEngine.WithTargetPolicy(youtube.AllowedTargetURL, youtube.AllowedOutboundHost)
		adapters["youtube"] = youtube.NewWithSizeProber(ytEngine, ytengine.NewSizeProber(executor, youtube.AllowedMediaURL))
	}
	if igEnabled {
		if err := c.Register(catalog.InstagramPlatform()); err != nil {
			return err
		}
		igEngine := baseEngine.WithTargetPolicy(instagram.AllowedTargetURL, instagram.AllowedOutboundHost)
		adapters["instagram"] = instagram.NewWithSizeProber(igEngine, ytengine.NewSizeProber(executor, instagram.AllowedMediaURL)).WithFetcher(executor)
	}
	for _, spec := range genericSites {
		if err := c.Register(spec.CatalogEntry()); err != nil {
			return err
		}
		policyParser := ytgeneric.NewWithResolver(spec, nil, nil, net.DefaultResolver)
		siteEngine := baseEngine.
			WithTargetPolicy(policyParser.AllowedTargetURL, policyParser.AllowedOutboundHost).
			WithExtractors(spec.Extractors...)
		var prober ytengine.SizeProber
		if !spec.SingleStreamHLSOnly {
			prober = ytengine.NewSizeProber(executor, policyParser.AllowedMediaURL)
		}
		adapters[spec.PlatformID] = ytgeneric.NewWithResolver(spec, siteEngine, prober, net.DefaultResolver)
	}
	return nil
}

func newXEngine(baseEngine *ytengine.Engine) *ytengine.Engine {
	return baseEngine.
		WithTargetPolicy(x.AllowedTargetURL, x.AllowedOutboundHost).
		WithExtractors(x.AllowedExtractors()...)
}

// configureYouTubeHelper optionally enables /api/youtube/context and
// /api/youtube/finalize when DOKITOOL_YOUTUBE_HELPER is true and the pinned
// Deno + yt-dlp/ejs runtime assets pass SHA-256 verification.
// Verification failure disables only the YouTube helper without affecting other platforms.
func configureYouTubeHelper(c *catalog.Catalog, handler *api.Handler, executor fetch.Executor, getenv func(string) string) error {
	raw := getenv("DOKITOOL_YOUTUBE_HELPER")
	if raw == "" {
		return nil
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return fmt.Errorf("DOKITOOL_YOUTUBE_HELPER must be a boolean")
	}
	if !enabled {
		return nil
	}
	helper, err := youtube.NewHelperFromEnv(executor, getenv, time.Now)
	if err != nil {
		log.Printf("dokitool: youtube helper runtime preflight failed (youtube helper disabled): %v", err)
		return nil
	}
	if c != nil && !c.Has("youtube") {
		if err := c.Register(catalog.YouTubePlatform()); err != nil {
			return err
		}
	}
	sb := helper.Sandbox()
	log.Printf("dokitool: youtube helper runtime verified (deno_version=%s, deno_asset=%s, deno_sha256=%s, ejs_version=%s, lib_sha256=%s, core_sha256=%s)",
		youtube.PinnedDenoVersion, sb.DenoAsset(), sb.DenoSHA256(), youtube.PinnedEJSVersion, sb.LibSHA256(), sb.CoreSHA256())
	handler.SetYouTubeHelper(helper)
	return nil
}
