package weibo

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const maxMediaBytes int64 = 1 << 30

type rangeError string

func (e rangeError) Error() string { return string(e) }

var rangeSyntax = regexp.MustCompile(`^bytes=([0-9]*)-([0-9]*)$`)
var unsatisfiedSyntax = regexp.MustCompile(`^bytes \*/([0-9]{1,10})$`)
var partialSyntax = regexp.MustCompile(`^bytes ([0-9]{1,10})-([0-9]{1,10})/([0-9]{1,10})$`)
var lengthSyntax = regexp.MustCompile(`^[0-9]{1,10}$`)

// ParseRange mirrors the original PHP media transport boundary: one bounded
// byte range, never a multi-range or arbitrary forwarded request header.
// An absent or empty header means full content; malformed values are errors.
func ParseRange(raw string) (unified.Object, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 80 {
		return nil, rangeError("Invalid media Range")
	}
	matches := rangeSyntax.FindStringSubmatch(strings.TrimSpace(raw))
	if matches == nil || matches[1] == "" && matches[2] == "" {
		return nil, rangeError("Invalid media Range")
	}
	numbers := [2]int64{}
	for i, raw := range []string{matches[1], matches[2]} {
		if raw == "" {
			continue
		}
		if len(raw) > 10 {
			return nil, rangeError("Media Range exceeds byte budget")
		}
		number, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || number > maxMediaBytes {
			return nil, rangeError("Media Range exceeds byte budget")
		}
		numbers[i] = number
	}
	if matches[1] == "" {
		if numbers[1] < 1 {
			return nil, rangeError("Empty media suffix")
		}
		return unified.Object{"header": "bytes=-" + strconv.FormatInt(numbers[1], 10), "suffix": numbers[1]}, nil
	}
	start := numbers[0]
	if start >= maxMediaBytes {
		return nil, rangeError("Invalid media range bounds")
	}
	var end any
	suffix := ""
	if matches[2] != "" {
		if numbers[1] < start || numbers[1] >= maxMediaBytes {
			return nil, rangeError("Invalid media range bounds")
		}
		end = numbers[1]
		suffix = strconv.FormatInt(numbers[1], 10)
	}
	return unified.Object{"header": "bytes=" + strconv.FormatInt(start, 10) + "-" + suffix, "start": start, "end": end}, nil
}

func responseFailure(message string, status int) unified.Object {
	return unified.Object{"ok": false, "status": status, "message": message}
}

func decimal(raw string) (int64, bool) {
	if !lengthSyntax.MatchString(raw) {
		return 0, false
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	return n, err == nil
}

// ResponsePlan checks the entire upstream header set before a single media
// byte is delivered. Callers must supply lower-case names and join duplicate
// critical headers with ", ", as PHP cURL does, before invoking this function.
func ResponsePlan(status int, headers map[string]string, requested unified.Object) unified.Object {
	common := unified.Object{"Cache-Control": "private, no-store", "X-Content-Type-Options": "nosniff", "Accept-Ranges": "bytes", "X-Accel-Buffering": "no"}
	if status == 416 && requested != nil {
		if match := unsatisfiedSyntax.FindStringSubmatch(headers["content-range"]); match != nil {
			if total, ok := decimal(match[1]); ok && total <= maxMediaBytes {
				common["Content-Range"] = "bytes */" + strconv.FormatInt(total, 10)
				common["Content-Length"] = "0"
				return unified.Object{"ok": true, "status": 416, "length": int64(0), "start": nil, "headers": common}
			}
		}
	}
	if status != 200 && status != 206 {
		return responseFailure("微博媒体暂不可用，请重新解析或稍后重试", 502)
	}
	if encoding, present := headers["content-encoding"]; present && encoding != "identity" {
		return responseFailure("微博媒体编码响应异常", 502)
	}
	mime := strings.ToLower(strings.TrimSpace(strings.SplitN(headers["content-type"], ";", 2)[0]))
	if mime != "video/mp4" && mime != "application/octet-stream" {
		return responseFailure("微博未返回可播放的视频文件", 502)
	}
	length, ok := decimal(headers["content-length"])
	if !ok {
		return responseFailure("微博媒体长度未知或无效", 502)
	}
	if length < 1 {
		return responseFailure("微博返回了空视频文件", 502)
	}
	if length > maxMediaBytes {
		return responseFailure("视频超过单文件 1 GiB 限制", 413)
	}
	common["Content-Type"] = "video/mp4"
	common["Content-Length"] = strconv.FormatInt(length, 10)
	common["Content-Disposition"] = `inline; filename="weibo-video.mp4"`
	start := int64(0)
	if requested == nil {
		_, hasRange := headers["content-range"]
		if status != 200 || hasRange {
			return responseFailure("微博返回了意外的部分视频", 502)
		}
		if length < 8 {
			return responseFailure("微博视频文件头无效", 502)
		}
	} else {
		match := partialSyntax.FindStringSubmatch(headers["content-range"])
		if status != 206 || match == nil {
			return responseFailure("微博未按请求返回视频范围", 502)
		}
		begin, _ := decimal(match[1])
		end, _ := decimal(match[2])
		total, _ := decimal(match[3])
		if total > maxMediaBytes {
			return responseFailure("视频超过单文件 1 GiB 限制", 413)
		}
		if begin > end || end >= total || end-begin+1 != length {
			return responseFailure("微博视频范围长度不一致", 502)
		}
		expectedStart := int64(0)
		expectedEnd := total - 1
		if suffix, ok := unified.PHPInt(requested["suffix"]); ok {
			if total > suffix {
				expectedStart = total - suffix
			}
		} else {
			expectedStart, _ = unified.PHPInt(requested["start"])
			if bound, ok := unified.PHPInt(requested["end"]); ok && bound < expectedEnd {
				expectedEnd = bound
			}
		}
		if begin != expectedStart || end != expectedEnd {
			return responseFailure("微博视频范围与请求不符", 502)
		}
		start = begin
		common["Content-Range"] = "bytes " + strconv.FormatInt(begin, 10) + "-" + strconv.FormatInt(end, 10) + "/" + strconv.FormatInt(total, 10)
	}
	return unified.Object{"ok": true, "status": status, "length": length, "start": start, "headers": common}
}
