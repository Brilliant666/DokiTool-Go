package weibo

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func TestFrozenPHPTickets(t *testing.T) {
	const directory = "../../../testdata/php-baseline/"
	manifestBytes, err := os.ReadFile(directory + "manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := unified.Decode(manifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(directory + "tickets.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != unified.Str(unified.Map(manifest["files"])["tickets.json"]) {
		t.Fatal("frozen PHP ticket hash changed")
	}
	fixture, err := unified.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range unified.Slice(fixture["cases"]) {
		item := unified.Map(value)
		t.Run(unified.Str(item["id"]), func(t *testing.T) {
			input := unified.Map(item["input"])
			number, _ := unified.Numeric(input["now"])
			now := int64(number)
			var got any
			switch unified.Str(item["operation"]) {
			case "ticket_sign":
				var expiry *int64
				if input["expires_at"] != nil {
					n, _ := unified.Numeric(input["expires_at"])
					e := int64(n)
					expiry = &e
				}
				if signed, ok := Sign(unified.Str(input["url"]), unified.Str(input["key"]), now, expiry); ok {
					got = signed
					// Matching exact PHP bytes also means the unchanged PHP verifier
					// receives precisely the same ticket after a rollback.
					if _, valid := Verify(strings.TrimPrefix(signed, "/api/media/weibo?ticket="), unified.Str(input["key"]), now); !valid {
						t.Fatal("signed ticket cannot be verified")
					}
				}
			case "ticket_verify":
				if payload, ok := Verify(unified.Str(input["ticket"]), unified.Str(input["key"]), now); ok {
					got = payload
				}
			default:
				t.Fatal("unknown ticket fixture operation")
			}
			if !unified.Equal(got, item["expected"]) {
				actual, _ := unified.Encode(got)
				wanted, _ := unified.Encode(item["expected"])
				t.Fatalf("ticket PHP mismatch\ngot: %s\nwant: %s", actual, wanted)
			}
		})
	}
}

func TestTicketURLFormatKeepsPHPTransportBytes(t *testing.T) {
	for raw, allowed := range map[string]bool{
		"https://f.video.weibocdn.com/a%ZZ.mp4":   true,
		"https://f.video.weibocdn.com:0443/a.mp4": true,
		"https://f.video.weibocdn.com":            false,
		"https://f.video.weibocdn.com/a.mp4#":     false,
		"https://f.video.weibocdn.com/中文.mp4":     false,
		"https://F.VIDEO.WEIBOCDN.COM/a.mp4":      false,
	} {
		if AllowedURL(raw) != allowed {
			t.Errorf("PHP URL shape differs for %q", raw)
		}
	}
}
