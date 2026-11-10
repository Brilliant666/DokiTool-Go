package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func resolveTestDenoBinary(t *testing.T) string {
	t.Helper()
	if envPath := strings.TrimSpace(os.Getenv("DOKITOOL_DENO_BINARY")); envPath != "" {
		return envPath
	}
	if runnerTemp := strings.TrimSpace(os.Getenv("RUNNER_TEMP")); runnerTemp != "" {
		candidate := filepath.Join(runnerTemp, "dokitool-validation", "bin", "deno")
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	if p, err := exec.LookPath("deno"); err == nil && p != "" {
		return p
	}
	t.Fatal("deno v2.9.8 binary not found in DOKITOOL_DENO_BINARY or PATH")
	return ""
}

func newTestSandbox(t *testing.T) *EJSSandbox {
	t.Helper()
	denoBin := resolveTestDenoBinary(t)
	ejsDir := filepath.Join("testdata", "ejs")
	sb, err := NewEJSSandbox(SandboxConfig{
		DenoBinary: denoBin,
		EJSDir:     ejsDir,
	})
	if err != nil {
		t.Fatalf("NewEJSSandbox failed: %v", err)
	}
	return sb
}

func TestEJSSandboxVerification(t *testing.T) {
	sb := newTestSandbox(t)
	if sb.DenoSHA256() == "" || sb.DenoAsset() == "" {
		t.Fatalf("expected non-empty deno sha256 and asset, got sha=%q asset=%q", sb.DenoSHA256(), sb.DenoAsset())
	}
	if sb.LibSHA256() != OfficialEJSLibSHA256 {
		t.Fatalf("lib sha256 = %q, want %q", sb.LibSHA256(), OfficialEJSLibSHA256)
	}
	if sb.CoreSHA256() != OfficialEJSCoreSHA256 {
		t.Fatalf("core sha256 = %q, want %q", sb.CoreSHA256(), OfficialEJSCoreSHA256)
	}

	// Tampered ejs asset must fail verification.
	badDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(badDir, "yt.solver.lib.min.js"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "yt.solver.core.min.js"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewEJSSandbox(SandboxConfig{
		DenoBinary: resolveTestDenoBinary(t),
		EJSDir:     badDir,
	}); err == nil {
		t.Fatal("expected tampered ejs assets to fail verification")
	}

	// Tampered deno binary must fail verification.
	fakeDeno := filepath.Join(badDir, "deno")
	if err := os.WriteFile(fakeDeno, []byte("#!/bin/sh\necho deno 2.9.8\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewEJSSandbox(SandboxConfig{
		DenoBinary: fakeDeno,
		EJSDir:     filepath.Join("testdata", "ejs"),
	}); err == nil {
		t.Fatal("expected unpinned deno binary to fail SHA-256 verification")
	}
}

func TestEJSSandboxBlocksNetworkAndFilesystem(t *testing.T) {
	sb := newTestSandbox(t)
	ctx := context.Background()

	// 1. Network access inside sandbox must fail.
	_, err := sb.RunRawScript(ctx, `await fetch("http://127.0.0.1:8080/");`)
	if err == nil {
		t.Fatal("expected network access inside deno sandbox to fail")
	}
	if !strings.Contains(err.Error(), "NotCapable") && !strings.Contains(err.Error(), "net access") {
		t.Fatalf("expected NotCapable net access error, got: %v", err)
	}

	// 2. File read inside sandbox must fail.
	_, err = sb.RunRawScript(ctx, `Deno.readTextFileSync("/etc/passwd");`)
	if err == nil {
		t.Fatal("expected file read inside deno sandbox to fail")
	}
	if !strings.Contains(err.Error(), "NotCapable") && !strings.Contains(err.Error(), "read access") {
		t.Fatalf("expected NotCapable read access error, got: %v", err)
	}

	// 3. Environment variable access and subprocess spawning inside sandbox must also fail.
	_, err = sb.RunRawScript(ctx, `Deno.env.get("HOME");`)
	if err == nil {
		t.Fatal("expected env access inside deno sandbox to fail")
	}
	_, err = sb.RunRawScript(ctx, `new Deno.Command("sh").outputSync();`)
	if err == nil {
		t.Fatal("expected subprocess spawn inside deno sandbox to fail")
	}
}

type mockYouTubeFetchExecutor struct {
	playerID      string
	playerBaseJS  []byte
	fetchDelay    time.Duration
	failRefresh   atomic.Bool
	iframeHits    atomic.Int32
	playerJSHits  atomic.Int32
	forbiddenHits atomic.Int32
}

func (m *mockYouTubeFetchExecutor) Do(_ context.Context, req fetch.Request) (fetch.Response, error) {
	if req.Policy.AllowedURL == nil || !req.Policy.AllowedURL(req.URL) {
		m.forbiddenHits.Add(1)
		return fetch.Response{}, fetch.ErrPolicy
	}
	// Policy must reject any non-www.youtube.com URL, especially googlevideo.com.
	if req.Policy.AllowedURL("https://rr1---sn-npoe7n7r.googlevideo.com/videoplayback") ||
		req.Policy.AllowedURL("http://www.youtube.com/iframe_api") ||
		req.Policy.AllowedURL("https://evil.example.com/iframe_api") {
		m.forbiddenHits.Add(1)
		return fetch.Response{}, fetch.ErrPolicy
	}
	if m.fetchDelay > 0 {
		time.Sleep(m.fetchDelay)
	}
	switch req.URL {
	case "https://www.youtube.com/iframe_api":
		m.iframeHits.Add(1)
		if m.failRefresh.Load() {
			return fetch.Response{StatusCode: http.StatusBadGateway}, nil
		}
		body := `var scriptUrl = 'https:\/\/www.youtube.com\/s\/player\/` + m.playerID + `\/www-widgetapi.vflset\/www-widgetapi.js';`
		return fetch.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/javascript"}},
			Body:       []byte(body),
			URL:        req.URL,
		}, nil
	case "https://www.youtube.com/s/player/" + m.playerID + "/player_ias.vflset/en_US/base.js":
		m.playerJSHits.Add(1)
		if m.failRefresh.Load() {
			return fetch.Response{StatusCode: http.StatusBadGateway}, nil
		}
		return fetch.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/javascript"}},
			Body:       m.playerBaseJS,
			URL:        req.URL,
		}, nil
	default:
		m.forbiddenHits.Add(1)
		return fetch.Response{StatusCode: http.StatusNotFound}, nil
	}
}

func TestYouTubeHelperContextAndFinalizeSample46(t *testing.T) {
	sb := newTestSandbox(t)
	playerJS, err := os.ReadFile(filepath.Join("testdata", "player_base_fixture.js"))
	if err != nil {
		t.Fatal(err)
	}
	sampleJSON, err := os.ReadFile(filepath.Join("testdata", "finalize_sample_46.json"))
	if err != nil {
		t.Fatal(err)
	}

	execMock := &mockYouTubeFetchExecutor{
		playerID:     "a5c2aafe",
		playerBaseJS: playerJS,
	}
	currentNow := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	helper, err := NewHelper(execMock, sb, func() time.Time { return currentNow })
	if err != nil {
		t.Fatal(err)
	}

	// 1. GET /api/youtube/context populates cache and preprocesses player JS.
	ctxPayload, status := helper.Context(context.Background())
	if status != http.StatusOK {
		t.Fatalf("Context status = %d, payload = %#v", status, ctxPayload)
	}
	data := unified.Map(ctxPayload["data"])
	if got := unified.Str(data["player_id"]); got != "a5c2aafe" {
		t.Fatalf("player_id = %q, want a5c2aafe", got)
	}
	if got, ok := unified.Numeric(data["signature_timestamp"]); !ok || int(got) != 20760 {
		t.Fatalf("signature_timestamp = %v, want 20760", data["signature_timestamp"])
	}
	clientObj := unified.Map(data["client"])
	if unified.Str(clientObj["name"]) != TVHTML5ClientName || unified.Str(clientObj["version"]) != TVHTML5ClientVersion {
		t.Fatalf("client = %#v", clientObj)
	}
	if unified.Str(data["expires_at"]) != "2026-11-09T18:00:00Z" {
		t.Fatalf("expires_at = %q, want 2026-11-09T18:00:00Z", unified.Str(data["expires_at"]))
	}

	// 2. Second Context call within TTL uses cached player without re-fetching.
	_, status2 := helper.Context(context.Background())
	if status2 != http.StatusOK || execMock.iframeHits.Load() != 1 || execMock.playerJSHits.Load() != 1 {
		t.Fatalf("expected 1 iframe hit and 1 playerJS hit, got iframe=%d playerJS=%d", execMock.iframeHits.Load(), execMock.playerJSHits.Load())
	}

	// 3. POST /api/youtube/finalize with #46 sanitized sample.
	finPayload, finStatus := helper.Finalize(context.Background(), sampleJSON, false)
	if finStatus != http.StatusOK {
		t.Fatalf("Finalize status = %d, payload = %#v", finStatus, finPayload)
	}
	if err := unified.ValidateEnvelopeAt(finPayload, currentNow.Unix()); err != nil {
		t.Fatalf("ValidateEnvelopeAt failed: %v", err)
	}
	if execMock.forbiddenHits.Load() != 0 {
		t.Fatalf("unexpected outbound hits: %d", execMock.forbiddenHits.Load())
	}

	finData := unified.Map(finPayload["data"])
	if unified.Str(finData["platform"]) != "youtube" || unified.Str(finData["content_id"]) != "I9JvDaciaFk" {
		t.Fatalf("unexpected platform/content_id: %#v", finData)
	}
	items := unified.Slice(finData["items"])
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	item := unified.Map(items[0])
	def := unified.Map(item["default"])
	if def == nil {
		t.Fatal("expected non-nil item.default")
	}
	defVariantID := unified.Str(def["variant_id"])
	if defVariantID == "" {
		t.Fatal("expected non-empty item.default.variant_id")
	}
	defURL := unified.Str(def["url"])
	if defURL == "" {
		t.Fatal("expected non-empty item.default.url")
	}
	parsedDefURL, err := url.Parse(defURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsedDefURL.Query().Get("itag") != "18" {
		t.Fatalf("default url itag = %q, want 18", parsedDefURL.Query().Get("itag"))
	}
	if parsedDefURL.Query().Get("title") != "I9JvDaciaFk_360p" {
		t.Fatalf("expected itag 18 URL to have title=I9JvDaciaFk_360p, got %q", parsedDefURL.Query().Get("title"))
	}
	if parsedDefURL.Query().Get("n") != "SOLVED_N_CHALLENGE_N_18" {
		t.Fatalf("expected solved n parameter, got %q", parsedDefURL.Query().Get("n"))
	}
	if !strings.HasPrefix(parsedDefURL.Query().Get("sig"), "SOLVED_SIG_") {
		t.Fatalf("expected solved sig parameter, got %q", parsedDefURL.Query().Get("sig"))
	}

	variants := unified.Slice(item["variants"])
	var sawDefaultVariant, saw1080VideoOnly, sawAudioM4A bool
	for _, rawV := range variants {
		v := unified.Map(rawV)
		label := unified.Str(v["label"])
		meta := unified.Map(v["metadata"])
		if unified.Str(v["id"]) == defVariantID {
			sawDefaultVariant = true
			if label != "360p (含音频)" {
				t.Fatalf("default variant label = %q, want 360p (含音频)", label)
			}
			if meta["has_audio"] != true {
				t.Fatalf("default variant metadata.has_audio = %v, want true", meta["has_audio"])
			}
		}
		if strings.HasPrefix(label, "1080p") && strings.Contains(label, "仅视频无声") {
			saw1080VideoOnly = true
			if meta["has_audio"] != false {
				t.Fatalf("expected 1080p video-only has_audio=false, got %v", meta["has_audio"])
			}
		}
		if strings.HasPrefix(label, "仅音频 M4A") {
			sawAudioM4A = true
			if meta["has_audio"] != true || unified.Str(meta["extension"]) != "m4a" {
				t.Fatalf("unexpected audio variant metadata: %#v", meta)
			}
		}
	}
	if !sawDefaultVariant || !saw1080VideoOnly || !sawAudioM4A {
		t.Fatalf("missing expected variants: sawDefaultVariant=%v saw1080VideoOnly=%v sawAudioM4A=%v", sawDefaultVariant, saw1080VideoOnly, sawAudioM4A)
	}
}

func TestYouTubeHelperFinalizeRejections(t *testing.T) {
	sb := newTestSandbox(t)
	playerJS, err := os.ReadFile(filepath.Join("testdata", "player_base_fixture.js"))
	if err != nil {
		t.Fatal(err)
	}
	sampleJSON, err := os.ReadFile(filepath.Join("testdata", "finalize_sample_46.json"))
	if err != nil {
		t.Fatal(err)
	}

	execMock := &mockYouTubeFetchExecutor{
		playerID:     "a5c2aafe",
		playerBaseJS: playerJS,
	}
	currentNow := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	helper, err := NewHelper(execMock, sb, func() time.Time { return currentNow })
	if err != nil {
		t.Fatal(err)
	}
	_, _ = helper.Context(context.Background())

	var baseReq map[string]any
	if err := json.Unmarshal(sampleJSON, &baseReq); err != nil {
		t.Fatal(err)
	}
	firstFmt := baseReq["formats"].([]any)[0].(map[string]any)

	mustReject := func(name string, body []byte) {
		t.Helper()
		payload, status := helper.Finalize(context.Background(), body, false)
		if status != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d (%#v)", name, status, payload)
		}
		if unified.Str(payload["code"]) != "INVALID_INPUT" {
			t.Fatalf("%s: expected INVALID_INPUT, got %q", name, unified.Str(payload["code"]))
		}
	}

	// 1. Non-googlevideo.com/videoplayback URLs.
	for _, badURL := range []string{
		"https://evil.example.com/videoplayback?id=I9JvDaciaFk&itag=18&n=abc",
		"http://rr1---sn-npoe7n7r.googlevideo.com/videoplayback?id=I9JvDaciaFk&itag=18&n=abc",
		"https://googlevideo.com/videoplayback?id=I9JvDaciaFk&itag=18&n=abc",
		"https://rr1---sn-npoe7n7r.googlevideo.com/other?id=I9JvDaciaFk&itag=18&n=abc",
	} {
		reqBody, _ := json.Marshal(map[string]any{
			"video_id":  "I9JvDaciaFk",
			"player_id": "a5c2aafe",
			"formats": []any{
				map[string]any{
					"itag":     18,
					"mimeType": `video/mp4; codecs="avc1.42001E, mp4a.40.2"`,
					"url":      badURL,
				},
			},
		})
		mustReject("non-googlevideo URL "+badURL, reqBody)
	}

	// 2. URL parameter id inconsistent with video_id.
	for _, badIDURL := range []string{
		"https://rr1---sn-npoe7n7r.googlevideo.com/videoplayback?id=dQw4w9WgXcQ&itag=18&n=abc",
		"https://rr1---sn-npoe7n7r.googlevideo.com/videoplayback?id=o-short&itag=18&n=abc",
		"https://rr1---sn-npoe7n7r.googlevideo.com/videoplayback?itag=18&n=abc",
	} {
		reqBody, _ := json.Marshal(map[string]any{
			"video_id":  "I9JvDaciaFk",
			"player_id": "a5c2aafe",
			"formats": []any{
				map[string]any{
					"itag":     18,
					"mimeType": `video/mp4; codecs="avc1.42001E, mp4a.40.2"`,
					"url":      badIDURL,
				},
			},
		})
		mustReject("mismatched id "+badIDURL, reqBody)
	}

	// Also reject mixed stream IDs within the same request.
	mixedBody, _ := json.Marshal(map[string]any{
		"video_id":  "I9JvDaciaFk",
		"player_id": "a5c2aafe",
		"formats": []any{
			firstFmt,
		},
		"adaptiveFormats": []any{
			map[string]any{
				"itag":     140,
				"mimeType": `audio/mp4; codecs="mp4a.40.2"`,
				"url":      "https://rr1---sn-npoe7n7r.googlevideo.com/videoplayback?id=o-AMD5XRy5l4PR-ScuEnaV1lEOnKBdYN9jbrY9dri_rXIK&itag=140&n=abc",
			},
		},
	})
	mustReject("inconsistent stream id across formats", mixedBody)

	// 3. player_id not in cache.
	uncachedBody, _ := json.Marshal(map[string]any{
		"video_id":  "I9JvDaciaFk",
		"player_id": "deadbeef",
		"formats":   []any{firstFmt},
	})
	mustReject("uncached player_id", uncachedBody)

	// 4. player_id expired.
	currentNow = currentNow.Add(7 * time.Hour)
	mustReject("expired player_id", sampleJSON)
	// Restore time for remaining checks.
	currentNow = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

	// 5. Format entries > 64.
	many := make([]any, 65)
	for i := range many {
		many[i] = firstFmt
	}
	tooManyBody, _ := json.Marshal(map[string]any{
		"video_id":        "I9JvDaciaFk",
		"player_id":       "a5c2aafe",
		"formats":         many[:1],
		"adaptiveFormats": many[1:],
	})
	mustReject("entries > 64", tooManyBody)

	// 6. Body > 512 KiB.
	hugeBody := bytes.Repeat([]byte(" "), MaxFinalizeBodyBytes+1)
	mustReject("body > 512 KiB", hugeBody)

	// 7. Unknown top-level field.
	unknownFieldBody, _ := json.Marshal(map[string]any{
		"video_id":   "I9JvDaciaFk",
		"player_id":  "a5c2aafe",
		"formats":    []any{firstFmt},
		"visitor_id": "unexpected",
	})
	mustReject("unknown top-level field", unknownFieldBody)
}

func TestYouTubeHelperContextConcurrentSingleflightAndStaleFallback(t *testing.T) {
	sb := newTestSandbox(t)
	playerJS, err := os.ReadFile(filepath.Join("testdata", "player_base_fixture.js"))
	if err != nil {
		t.Fatal(err)
	}

	execMock := &mockYouTubeFetchExecutor{
		playerID:     "a5c2aafe",
		playerBaseJS: playerJS,
		fetchDelay:   25 * time.Millisecond,
	}
	var nowMu sync.Mutex
	currentNow := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	helper, err := NewHelper(execMock, sb, func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return currentNow
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. 20 concurrent Context calls when cache is empty must coalesce into a single refresh.
	const concurrency = 20
	var wg sync.WaitGroup
	startGate := make(chan struct{})
	statuses := make([]int, concurrency)
	playerIDs := make([]string, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-startGate
			payload, status := helper.Context(context.Background())
			statuses[idx] = status
			playerIDs[idx] = unified.Str(unified.Map(payload["data"])["player_id"])
		}(i)
	}
	close(startGate)
	wg.Wait()

	for i := 0; i < concurrency; i++ {
		if statuses[i] != http.StatusOK || playerIDs[i] != "a5c2aafe" {
			t.Fatalf("caller %d got status=%d player_id=%q", i, statuses[i], playerIDs[i])
		}
	}
	if got := execMock.iframeHits.Load(); got != 1 {
		t.Fatalf("iframe_api hits = %d, want 1", got)
	}
	if got := execMock.playerJSHits.Load(); got != 1 {
		t.Fatalf("base.js hits = %d, want 1", got)
	}

	// 2. Stale fallback within 1 hour when refresh fails.
	execMock.failRefresh.Store(true)
	nowMu.Lock()
	currentNow = currentNow.Add(DefaultPlayerCacheTTL + 30*time.Minute)
	nowMu.Unlock()

	stalePayload, staleStatus := helper.Context(context.Background())
	if staleStatus != http.StatusOK {
		t.Fatalf("expected 200 from stale fallback within 1h, got %d (%#v)", staleStatus, stalePayload)
	}
	if got := unified.Str(unified.Map(stalePayload["data"])["player_id"]); got != "a5c2aafe" {
		t.Fatalf("stale fallback player_id = %q, want a5c2aafe", got)
	}

	// 3. Past 1 hour after expiry, failed refresh must return 502.
	nowMu.Lock()
	currentNow = currentNow.Add(45 * time.Minute) // total +6h75m (> +7h)
	nowMu.Unlock()

	failPayload, failStatus := helper.Context(context.Background())
	if failStatus != http.StatusBadGateway {
		t.Fatalf("expected 502 when stale > 1h and refresh fails, got %d (%#v)", failStatus, failPayload)
	}
}

func TestYouTubeHelperFinalizeMetadataFields(t *testing.T) {
	sb := newTestSandbox(t)
	playerJS, err := os.ReadFile(filepath.Join("testdata", "player_base_fixture.js"))
	if err != nil {
		t.Fatal(err)
	}
	sampleJSON, err := os.ReadFile(filepath.Join("testdata", "finalize_sample_46.json"))
	if err != nil {
		t.Fatal(err)
	}

	execMock := &mockYouTubeFetchExecutor{
		playerID:     "a5c2aafe",
		playerBaseJS: playerJS,
	}
	currentNow := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	helper, err := NewHelper(execMock, sb, func() time.Time { return currentNow })
	if err != nil {
		t.Fatal(err)
	}
	_, _ = helper.Context(context.Background())

	var baseReq map[string]any
	if err := json.Unmarshal(sampleJSON, &baseReq); err != nil {
		t.Fatal(err)
	}
	firstFmt := baseReq["formats"].([]any)[0].(map[string]any)

	// 1. Valid title, author, duration_seconds, and i.ytimg.com thumbnail.
	validBody, _ := json.Marshal(map[string]any{
		"video_id":         "I9JvDaciaFk",
		"player_id":        "a5c2aafe",
		"title":            "  Me at the zoo  ",
		"author":           "  jawed  ",
		"duration_seconds": 19,
		"thumbnail":        "https://i.ytimg.com/vi/I9JvDaciaFk/maxresdefault.jpg",
		"formats":          []any{firstFmt},
	})
	payload, status := helper.Finalize(context.Background(), validBody, false)
	if status != http.StatusOK {
		t.Fatalf("valid metadata finalize status = %d, payload = %#v", status, payload)
	}
	data := unified.Map(payload["data"])
	if got := unified.Str(data["title"]); got != "Me at the zoo" {
		t.Fatalf("data.title = %q, want %q", got, "Me at the zoo")
	}
	if got := unified.Str(unified.Map(data["author"])["name"]); got != "jawed" {
		t.Fatalf("data.author.name = %q, want %q", got, "jawed")
	}
	if got := unified.Str(unified.Map(data["cover"])["url"]); got != "https://i.ytimg.com/vi/I9JvDaciaFk/maxresdefault.jpg" {
		t.Fatalf("data.cover.url = %q", got)
	}
	item := unified.Map(unified.Slice(data["items"])[0])
	if got, ok := unified.Numeric(item["duration_ms"]); !ok || int(got) != 19000 {
		t.Fatalf("item.duration_ms = %v, want 19000", item["duration_ms"])
	}
	defURL, _ := url.Parse(unified.Str(unified.Map(item["default"])["url"]))
	if got := defURL.Query().Get("title"); got != "Me_at_the_zoo_360p" {
		t.Fatalf("itag 18 title query = %q, want Me_at_the_zoo_360p", got)
	}

	// 2. Title with control characters and Title with only control chars/whitespace.
	ctrlBody, _ := json.Marshal(map[string]any{
		"video_id":  "I9JvDaciaFk",
		"player_id": "a5c2aafe",
		"title":     " \x00\x07Hello\n\tWorld\x1f\x7f ",
		"formats":   []any{firstFmt},
	})
	ctrlPayload, ctrlStatus := helper.Finalize(context.Background(), ctrlBody, false)
	if ctrlStatus != http.StatusOK {
		t.Fatalf("control-char title status = %d", ctrlStatus)
	}
	if got := unified.Str(unified.Map(ctrlPayload["data"])["title"]); got != "HelloWorld" {
		t.Fatalf("control-char stripped title = %q, want HelloWorld", got)
	}

	emptyCtrlBody, _ := json.Marshal(map[string]any{
		"video_id":  "I9JvDaciaFk",
		"player_id": "a5c2aafe",
		"title":     " \x00\x07\t\n ",
		"formats":   []any{firstFmt},
	})
	emptyCtrlPayload, emptyCtrlStatus := helper.Finalize(context.Background(), emptyCtrlBody, false)
	if emptyCtrlStatus != http.StatusOK {
		t.Fatalf("empty control-char title status = %d", emptyCtrlStatus)
	}
	if got := unified.Str(unified.Map(emptyCtrlPayload["data"])["title"]); got != "I9JvDaciaFk" {
		t.Fatalf("fallback title = %q, want I9JvDaciaFk", got)
	}

	// 3. Overly long title (> 200 runes) is truncated to 200 runes.
	longTitle := strings.Repeat("测A", 125) // 250 runes
	longBody, _ := json.Marshal(map[string]any{
		"video_id":  "I9JvDaciaFk",
		"player_id": "a5c2aafe",
		"title":     longTitle,
		"formats":   []any{firstFmt},
	})
	longPayload, longStatus := helper.Finalize(context.Background(), longBody, false)
	if longStatus != http.StatusOK {
		t.Fatalf("overlong title status = %d", longStatus)
	}
	gotLongTitle := unified.Str(unified.Map(longPayload["data"])["title"])
	if rc := utf8.RuneCountInString(gotLongTitle); rc != MaxFinalizeTitleRunes {
		t.Fatalf("truncated title rune count = %d, want %d", rc, MaxFinalizeTitleRunes)
	}

	// 4. Non-whitelisted thumbnail URLs and invalid duration_seconds must be rejected with 400.
	for _, badThumb := range []string{
		"https://evil.example.com/vi/I9JvDaciaFk/hqdefault.jpg",
		"http://127.0.0.1/thumb.jpg",
		"https://rr1---sn-npoe7n7r.googlevideo.com/videoplayback?itag=18",
		"ftp://i.ytimg.com/vi/I9JvDaciaFk/hqdefault.jpg",
	} {
		badThumbBody, _ := json.Marshal(map[string]any{
			"video_id":  "I9JvDaciaFk",
			"player_id": "a5c2aafe",
			"thumbnail": badThumb,
			"formats":   []any{firstFmt},
		})
		rejPayload, rejStatus := helper.Finalize(context.Background(), badThumbBody, false)
		if rejStatus != http.StatusBadRequest || unified.Str(rejPayload["code"]) != "INVALID_INPUT" {
			t.Fatalf("expected 400 INVALID_INPUT for thumbnail %q, got %d (%#v)", badThumb, rejStatus, rejPayload)
		}
	}

	for _, badDur := range []any{0, -10, "19", 19.5} {
		badDurBody, _ := json.Marshal(map[string]any{
			"video_id":         "I9JvDaciaFk",
			"player_id":        "a5c2aafe",
			"duration_seconds": badDur,
			"formats":          []any{firstFmt},
		})
		rejPayload, rejStatus := helper.Finalize(context.Background(), badDurBody, false)
		if rejStatus != http.StatusBadRequest || unified.Str(rejPayload["code"]) != "INVALID_INPUT" {
			t.Fatalf("expected 400 INVALID_INPUT for duration_seconds %v, got %d (%#v)", badDur, rejStatus, rejPayload)
		}
	}
}
