package douyin

import (
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func pickImageURL(image object) string {
	if text := unified.Str(first(image["urlList"])); text != "" {
		return toHTTPS(text)
	}
	values := unified.Slice(image["url_list"])
	if text := unified.Str(first(values)); text != "" {
		return toHTTPS(text)
	}
	if len(values) > 0 {
		if text := unified.Str(values[len(values)-1]); text != "" {
			return toHTTPS(text)
		}
	}
	return ""
}

func extractLivePhotoURL(video object) string {
	selected, v26 := "", ""
	if values := unified.Slice(video["playAddr"]); values != nil {
		for _, value := range values {
			src := unified.Str(unified.Map(value)["src"])
			if src == "" {
				continue
			}
			if strings.Contains(src, "v3-web") {
				selected = src
				break
			}
			if v26 == "" && strings.Contains(src, "v26-web") {
				v26 = src
			}
		}
		if !truthy(selected) && truthy(v26) {
			selected = replacePlayHost(v26)
		}
		if !truthy(selected) {
			for _, index := range []int{1, 0} {
				if index < len(values) {
					if src := unified.Str(unified.Map(values[index])["src"]); src != "" {
						selected = src
						break
					}
				}
			}
		}
	}
	if values := unified.Slice(child(video, "play_addr", "url_list")); !truthy(selected) && values != nil {
		for _, value := range values {
			src, ok := value.(string)
			if !ok || src == "" {
				continue
			}
			if strings.Contains(src, "v3-web") {
				selected = src
				break
			}
			if v26 == "" && strings.Contains(src, "v26-web") {
				v26 = src
			}
		}
		if !truthy(selected) && truthy(v26) {
			selected = replacePlayHost(v26)
		}
		if !truthy(selected) {
			for _, index := range []int{1, 0} {
				if index < len(values) {
					if src := unified.Str(values[index]); src != "" {
						selected = src
						break
					}
				}
			}
		}
	}
	if !truthy(selected) {
		selected = unified.Str(video["playApi"])
	}
	return selected
}

func extractVID(video object) string {
	if value := child(video, "play_addr", "uri"); value != nil && scalar(value) {
		return stringValue(value)
	}
	if value := unified.Map(first(video["playAddr"]))["uri"]; value != nil && scalar(value) {
		return stringValue(value)
	}
	return unified.Str(video["uri"])
}

func pickCover(detail object) string {
	video := unified.Map(detail["video"])
	if len(video) > 0 {
		for _, field := range [][2]string{{"originCover", "urlList"}, {"origin_cover", "url_list"}} {
			if value := unified.Str(first(child(video, field[0], field[1]))); value != "" {
				return value
			}
		}
		if value := unified.Str(video["originCover"]); value != "" {
			return value
		}
		if value := unified.Str(first(video["originCoverUrlList"])); value != "" {
			return value
		}
		if value, ok := pick(first(child(video, "cover", "urlList")), first(child(video, "cover", "url_list")), "").(string); ok && value != "" {
			return value
		}
		if value := unified.Str(video["cover"]); value != "" {
			return value
		}
	}
	if value := unified.Str(first(child(detail, "cover", "url_list"))); value != "" {
		return value
	}
	if len(video) > 0 {
		for _, field := range [][2]string{{"dynamicCover", "urlList"}, {"dynamic_cover", "url_list"}} {
			if value := unified.Str(first(child(video, field[0], field[1]))); value != "" {
				return value
			}
		}
	}
	images := unified.Slice(detail["images"])
	if len(images) == 0 {
		images = unified.Slice(detail["image_list"])
	}
	return unified.Str(first(child(first(images), "url_list")))
}
