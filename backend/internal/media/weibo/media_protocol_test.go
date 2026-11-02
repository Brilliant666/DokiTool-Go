package weibo

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func TestPHPWeiboMediaProtocol(t *testing.T) {
	packed, err := os.ReadFile("testdata/php-media-baseline.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	body, err := io.ReadAll(io.LimitReader(reader, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := unified.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	if unified.Str(fixture["php_source_commit"]) != "25beaee107f8c1a208ced9deb3c637068445e316" {
		t.Fatal("unexpected PHP source")
	}
	for _, raw := range unified.Slice(fixture["cases"]) {
		entry := unified.Map(raw)
		t.Run(unified.Str(entry["id"]), func(t *testing.T) {
			input := unified.Map(entry["input"])
			var got unified.Object
			switch unified.Str(entry["operation"]) {
			case "range":
				value, err := ParseRange(unified.Str(input["header"]))
				if err != nil {
					got = unified.Object{"ok": false, "message": err.Error()}
				}
				if err == nil {
					got = unified.Object{"ok": true, "value": value}
				}
			case "plan":
				headers := map[string]string{}
				for key, value := range unified.Map(input["headers"]) {
					headers[key] = unified.Str(value)
				}
				parsed, err := ParseRange(unified.Str(input["range"]))
				if err != nil {
					t.Fatal(err)
				}
				status, ok := unified.PHPInt(input["status"])
				if !ok {
					t.Fatal("missing status")
				}
				got = ResponsePlan(int(status), headers, parsed)
			default:
				t.Fatalf("unknown operation %q", entry["operation"])
			}
			normalized, err := unified.Encode(got)
			if err != nil {
				t.Fatal(err)
			}
			want, err := unified.Encode(entry["expected"])
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(normalized, want) {
				t.Fatalf("PHP/Go media protocol difference\nPHP: %s\nGo:  %s", want, normalized)
			}
		})
	}
}

func TestResponsePlanRejectsEmptyCriticalHeaders(t *testing.T) {
	encoding := map[string]string{"content-type": "video/mp4", "content-length": "8", "content-encoding": ""}
	ranged := map[string]string{"content-type": "video/mp4", "content-length": "8", "content-range": ""}
	for name, headers := range map[string]map[string]string{"empty-encoding": encoding, "unexpected-empty-range": ranged} {
		t.Run(name, func(t *testing.T) {
			result := ResponsePlan(200, headers, nil)
			if result["ok"] != false || result["status"] != 502 {
				t.Fatalf("critical header accepted: %v", result)
			}
		})
	}
}
