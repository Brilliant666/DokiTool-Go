package shresolver

import (
	"context"
	"encoding/json"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
	"net/http/httptest"
	"strings"
	"testing"
)

type resolverFunc func(context.Context, string) ([]unified.Object, error)

func (f resolverFunc) Originals(c context.Context, u string) ([]unified.Object, error) {
	return f(c, u)
}
func TestAuthorizedVIDOnlyAndUniqueConfirmedAddresses(t *testing.T) {
	token := strings.Repeat("a", 32)
	vid := "v0200fg10000d9umdsfog65tds1f8o0g"
	cdn := "https://v26-cold.douyinvod.com/00000000000000000000000000000000/abcdef01/video/tos/cn/tos-cn-v/test-object/"
	calls := 0
	handler, err := NewHandler(token, resolverFunc(func(_ context.Context, raw string) ([]unified.Object, error) {
		calls++
		if raw != "https://aweme.snssdk.com/aweme/v1/play/?video_id="+vid+"&ratio=default&line=0" {
			t.Fatal("non-official entry")
		}
		meta := unified.Object{"width": 3840, "height": 2160, "has_audio": true}
		row := unified.Object{"url": cdn, "prefix_sha256": strings.Repeat("f", 64), "metadata": meta}
		return []unified.Object{row, row}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, method, auth, body string
		code                     int
	}{
		{Path, "GET", token, "", 405}, {Path + "?url=a", "POST", token, "", 404}, {Path, "POST", "wrong", "", 401},
		{Path, "POST", token, `{"video_id":"7673404708262453110"}`, 400}, {Path, "POST", token, `{"url":"https://attacker.invalid"}`, 400},
		{Path, "POST", token, `{"video_id":"` + strings.Repeat("v", 600) + `"}`, 400},
		{Path, "POST", token, `{"video_id":"` + vid + `"}`, 200},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.auth)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Errorf("%s: status %d want %d", tc.body, w.Code, tc.code)
		}
		if w.Code == 200 {
			var result Response
			err = json.Unmarshal(w.Body.Bytes(), &result)
			if err != nil || result.VideoID != vid || len(result.Candidates) != 1 || result.Candidates[0].URL != cdn || result.Candidates[0].ObtainedAt <= 0 || result.Candidates[0].ExpiresAt != 0 {
				t.Fatalf("unexpected candidates: %s", w.Body)
			}
		}
	}
	if calls != 1 {
		t.Fatalf("unauthorized upstream calls: %d", calls)
	}
}
