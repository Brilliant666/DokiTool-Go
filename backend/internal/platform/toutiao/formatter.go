package toutiao

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

var renderScript = regexp.MustCompile(`(?is)<script\b[^>]*\sid\s*=\s*(?:"RENDER_DATA"|'RENDER_DATA')[^>]*>(.*?)</script\s*>`)
var definitionLabel = regexp.MustCompile(`^[0-9]{3,4}p$`)

func responseFromPage(html, id string) unified.Object {
	match := renderScript.FindStringSubmatch(html)
	if match == nil {
		if strings.Contains(html, "__ac_signature") || strings.Contains(html, "验证码") {
			return failure(429, "今日头条返回了访问验证页，请稍后重试")
		}
		return failure(404, "今日头条页面未提供视频数据")
	}
	raw := strings.Trim(match[1], " \n\r\t\v\x00")
	if !strings.HasPrefix(raw, "{") {
		raw = percentDecode(raw)
	}
	decoded, err := decodePHPJSON([]byte(raw))
	if err != nil || !phpArray(decoded) {
		return failure(502, "今日头条视频数据格式异常")
	}
	root := unified.Map(unified.Map(decoded)["data"])
	videoValue := root["initialVideo"]
	video := unified.Map(videoValue)
	if !phpArray(videoValue) || !matchingID(root["itemId"], id) || !matchingID(video["itemId"], id) {
		return failure(404, "今日头条页面未提供该视频数据")
	}
	play := unified.Map(video["videoPlayInfo"])
	entries, err := orderedValues([]byte(raw), play["video_list"], "data", "initialVideo", "videoPlayInfo", "video_list")
	if err != nil {
		return failure(502, "今日头条视频数据格式异常")
	}
	choices := candidates(entries)
	if len(choices) == 0 {
		return failure(404, "今日头条未返回可用视频地址")
	}
	cell := unified.Map(video["itemCell"])
	user := unified.Map(firstDefined(cell["userInfo"], video["userInfo"]))
	userID := firstDefined(user["userID"], user["userId"])
	if !phpScalar(userID) {
		userID = ""
	}
	data := unified.Object{
		"itemId":       id,
		"videoType":    "video",
		"title":        unified.Str(video["title"]),
		"author":       unified.Str(user["name"]),
		"userID":       phpString(userID),
		"avatar":       unified.MediaURL(firstDefined(user["avatarURL"], user["avatarUrl"])),
		"cover":        unified.MediaURL(firstDefined(video["coverUrl"], video["poster"])),
		"video_backup": append(unified.Array{}, choices[1:]...),
	}
	for key, value := range unified.Map(choices[0]) {
		data[key] = value
	}
	if value, ok := user["description"].(string); ok {
		data["author_description"] = value
	}
	if music := unified.Map(cell["videoAbility"])["music"]; phpArray(music) {
		data["music"] = phpArrayWire(music)
	}
	if value, ok := unified.Numeric(firstDefined(play["video_duration"], video["duration"])); ok && value > 0 {
		data["duration"] = value
	}
	if _, ok := unified.Numeric(video["publishTime"]); ok {
		if timestamp, _ := unified.PHPInt(video["publishTime"]); timestamp > 0 {
			data["create_time"] = timestamp
			data["publish_time"] = time.Unix(timestamp, 0).UTC().Format("2006-01-02 15:04:05")
		}
	}
	statistics := unified.Object{}
	for _, field := range [][2]string{{"diggCount", "digg_count"}, {"commentCount", "comment_count"}, {"repinCount", "collect_count"}, {"playCount", "play_count"}} {
		if value, ok := unified.Numeric(video[field[0]]); ok && value >= 0 {
			statistics[field[1]], _ = unified.PHPInt(video[field[0]])
		}
	}
	if len(statistics) != 0 {
		data["extra"] = unified.Object{"statistics": statistics}
	}
	return unified.Object{"code": 200, "msg": "解析成功", "data": data}
}

func candidates(entries unified.Array) unified.Array {
	out := unified.Array{}
	seen := map[string]bool{}
	for _, entryValue := range entries {
		if !phpArray(entryValue) {
			continue
		}
		entry := unified.Map(entryValue)
		meta := entry
		if phpArray(entry["video_meta"]) {
			meta = unified.Map(entry["video_meta"])
		}
		urls := []string{}
		for _, field := range []string{"main_url", "backup_url", "backup_url_1", "backup_url_2"} {
			if source := mediaURL(entry[field]); source != "" {
				urls = append(urls, source)
			}
		}
		sort.Strings(urls)
		hash := sha256.Sum256([]byte(unified.PHPIdentityJSON(urls, false)))
		fields := unified.Object{"private_representation_id": hex.EncodeToString(hash[:])}
		for _, pair := range [][2]string{{"width", "vwidth"}, {"height", "vheight"}, {"bit_rate", "real_bitrate"}, {"size", "size"}} {
			value := meta[pair[1]]
			if pair[0] == "bit_rate" {
				value = firstDefined(value, meta["bitrate"])
			}
			if number, valid := unified.Numeric(value); valid && number > 0 {
				fields[pair[0]], _ = unified.PHPInt(value)
			}
		}
		if codec, ok := meta["codec_type"].(string); ok && codec != "" {
			fields["codec"] = codec
		}
		fields["quality"] = "unknown"
		if width, hasWidth := fields["width"]; hasWidth {
			if height, hasHeight := fields["height"]; hasHeight {
				w, _ := unified.PHPInt(width)
				h, _ := unified.PHPInt(height)
				fields["quality"] = strconv.FormatInt(min(w, h), 10) + "p"
			}
		}
		if definition := unified.Str(meta["definition"]); fields["quality"] == "unknown" && definitionLabel.MatchString(definition) {
			fields["quality"] = definition
		}
		for _, field := range []string{"main_url", "backup_url", "backup_url_1", "backup_url_2"} {
			source := mediaURL(entry[field])
			if source == "" || seen[source] {
				continue
			}
			seen[source] = true
			candidate := unified.Clone(fields)
			candidate["url"] = source
			out = append(out, candidate)
		}
	}
	// PHP 8 usort is stable. Equal facts preserve the original representation
	// and main/backup order; Go map iteration is never involved here.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := unified.Map(out[i]), unified.Map(out[j])
		if order := comparePixels(a, b); order != 0 {
			return order > 0
		}
		for _, field := range []string{"bit_rate", "size"} {
			x, _ := unified.PHPInt(a[field])
			y, _ := unified.PHPInt(b[field])
			if x != y {
				return x > y
			}
		}
		return false
	})
	return out
}

// PHP promotes an overflowing integer product to a double. Preserve its
// comparison, including equal rounded products, without overflowing Go ints.
func comparePixels(a, b unified.Object) int {
	aw, _ := unified.PHPInt(a["width"])
	ah, _ := unified.PHPInt(a["height"])
	bw, _ := unified.PHPInt(b["width"])
	bh, _ := unified.PHPInt(b["height"])
	x := new(big.Int).Mul(big.NewInt(aw), big.NewInt(ah))
	y := new(big.Int).Mul(big.NewInt(bw), big.NewInt(bh))
	if x.IsInt64() && y.IsInt64() {
		return x.Cmp(y)
	}
	xf, yf := float64(aw)*float64(ah), float64(bw)*float64(bh)
	if xf > yf {
		return 1
	}
	if xf < yf {
		return -1
	}
	return 0
}

func mediaURL(value any) string {
	if source := unified.MediaURL(value); source != "" {
		return source
	}
	raw, ok := value.(string)
	if !ok {
		return ""
	}
	// PHP strict base64 permits SP/TAB/CR/LF and absent padding, but rejects
	// vertical tab and form feed even though they are ASCII whitespace.
	raw = strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "").Replace(raw)
	var decoded []byte
	var err error
	if strings.Contains(raw, "=") {
		decoded, err = base64.StdEncoding.DecodeString(raw)
	} else {
		decoded, err = base64.RawStdEncoding.DecodeString(raw)
	}
	if err != nil {
		return ""
	}
	return unified.MediaURL(string(decoded))
}

func matchingID(value any, wanted string) bool {
	return value == nil || phpScalar(value) && phpString(value) == wanted
}

func phpScalar(value any) bool {
	switch value.(type) {
	case string, bool, json.Number, int, int64, float64:
		return true
	}
	return false
}

func phpString(value any) string {
	switch value := value.(type) {
	case nil:
		return ""
	case string:
		return value
	case bool:
		if value {
			return "1"
		}
		return ""
	case json.Number:
		if _, err := value.Int64(); err == nil {
			return value.String()
		}
		number, _ := value.Float64()
		return phpFloatString(number)
	case float64:
		return phpFloatString(value)
	case int64:
		return strconv.FormatInt(value, 10)
	case int:
		return strconv.Itoa(value)
	default:
		return fmt.Sprint(value)
	}
}

func phpFloatString(value float64) string {
	text := strconv.FormatFloat(value, 'G', 14, 64)
	if index := strings.IndexByte(text, 'E'); index >= 0 {
		mantissa, exponent := text[:index], text[index+1:]
		if !strings.Contains(mantissa, ".") {
			mantissa += ".0"
		}
		sign := ""
		if strings.HasPrefix(exponent, "+") || strings.HasPrefix(exponent, "-") {
			sign, exponent = exponent[:1], exponent[1:]
		}
		exponent = strings.TrimLeft(exponent, "0")
		if exponent == "" {
			exponent = "0"
		}
		text = mantissa + "E" + sign + exponent
	}
	return text
}

func firstDefined(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func phpArray(value any) bool {
	return unified.Map(value) != nil || unified.Slice(value) != nil
}

func phpArrayWire(value any) any {
	if fields := unified.Map(value); fields != nil {
		if len(fields) == 0 {
			return unified.Array{}
		}
		out := unified.Object{}
		for key, value := range fields {
			out[key] = phpArrayWire(value)
		}
		return out
	}
	if values := unified.Slice(value); values != nil {
		out := unified.Array{}
		for _, value := range values {
			out = append(out, phpArrayWire(value))
		}
		return out
	}
	return value
}
