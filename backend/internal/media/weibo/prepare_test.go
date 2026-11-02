package weibo

import (
	"context"
	"net/netip"
	"os"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func TestPHPWeiboMediaPreparation(t *testing.T) {
	body, err := os.ReadFile("testdata/php-prepare-baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := unified.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	if unified.Str(fixture["php_source_commit"]) != "25beaee107f8c1a208ced9deb3c637068445e316" {
		t.Fatal("unknown PHP oracle provenance")
	}
	for _, value := range unified.Slice(fixture["cases"]) {
		entry := unified.Map(value)
		t.Run(unified.Str(entry["id"]), func(t *testing.T) {
			input := unified.Map(entry["input"])
			var addresses []netip.Addr
			var lookupErr error
			for _, value := range unified.Slice(input["ips"]) {
				ip, err := netip.ParseAddr(unified.Str(value))
				if err != nil {
					lookupErr = err
					break
				}
				addresses = append(addresses, ip)
			}
			lookup := func(_ context.Context, host string) ([]netip.Addr, error) {
				if host != "f.video.weibocdn.com" {
					t.Fatal("wrong DNS target", host)
				}
				return addresses, lookupErr
			}
			now, ok := unified.PHPInt(input["now"])
			if !ok {
				t.Fatal("invalid oracle clock")
			}
			got := Prepare(context.Background(), unified.Str(input["method"]), unified.Map(input["query"]), unified.Str(input["range"]), unified.Str(input["key"]), now, lookup)
			if !unified.Equal(got, entry["expected"]) {
				actual, _ := unified.Encode(got)
				wanted, _ := unified.Encode(entry["expected"])
				t.Fatalf("PHP preparation mismatch\nGo: %s\nPHP: %s", actual, wanted)
			}
		})
	}
}

func TestPHPMediaQueryBoundary(t *testing.T) {
	for _, tc := range []struct {
		raw, expected string
		ok            bool
	}{
		{"ticket=a&ticket=b", "b", true},
		{"ticket=a%2Bb", "a+b", true},
		{"ticket=a+b", "a b", true},
		{"ticket=a&ticket[]=x", "", false},
		{"ticket%5B0%5D=x", "", false},
		{"ticket=x&debug=1", "", false},
		{"ticket=%ZZ", "", false},
		{"", "", true},
	} {
		query, ok := PHPQuery(tc.raw)
		if ok != tc.ok {
			t.Errorf("%q accepted=%v want=%v", tc.raw, ok, tc.ok)
		}
		if ok && tc.expected != "" && unified.Str(query["ticket"]) != tc.expected {
			t.Errorf("%q final ticket = %v", tc.raw, query["ticket"])
		}
	}
}
