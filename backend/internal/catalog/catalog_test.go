package catalog

import (
	"os"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/config"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func TestCatalogUsesSingleRegistry(t *testing.T) {
	canonical, err := os.ReadFile("../../config/platforms.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != string(config.Platforms()) {
		t.Fatal("embedded registry differs from sole registry")
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := os.ReadFile("../../testdata/php-baseline/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	oracle, err := unified.Decode(baseline)
	if err != nil || !unified.Equal(c.Envelope(), oracle["expected"]) {
		t.Fatal("public catalogue differs from frozen PHP output")
	}
	rows := unified.Slice(unified.Map(c.Envelope()["data"])["platforms"])
	if len(rows) != 9 || unified.Map(rows[0])["id"] != "douyin" {
		t.Fatal("catalog order/size changed")
	}
	for _, value := range rows {
		row := unified.Map(value)
		if len(row) != 5 || row["file"] != nil || row["adapter"] != nil || row["domains"] != nil {
			t.Fatal("private registry field published")
		}
	}
}

func TestInputDomainBoundary(t *testing.T) {
	c, _ := Load()
	for raw, expected := range map[string]string{
		"https://www.douyin.com/video/a%ZZ": "douyin",
		"https://a..douyin.com/video/123":   "", "https://-.douyin.com/video/123": "",
		"https://www.douyin.com/video/中文": "", "https://www.douyin.com/video/%E4%B8%AD": "douyin",
		"https://v.douyin.com/share/": "douyin", "https://WWW.DOUYIN.COM:443/video/123": "douyin", "http://b23.tv/x?p=2": "bilibili", "https://www.rednote.com/explore/a": "xiaohongshu",
		"https://douyin.com.evil.example/video/1": "", "https://notdouyin.com/": "", "https://evil.example/?next=https://douyin.com": "", "https://user@douyin.com/": "", "https://douyin.com:8443/": "", "https://douyin.com\\@evil.example/": "", "file://douyin.com/": "", "https://douyin.com./": "",
	} {
		if actual := c.PlatformForURL(raw); actual != expected {
			t.Errorf("%s => %q want %q", raw, actual, expected)
		}
	}
}
