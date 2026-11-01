package kuaishou

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

var initState = regexp.MustCompile(`(?s)window\.INIT_STATE\s*=\s*(.*?)</script>`)
var apolloState = regexp.MustCompile(`(?s)window\.__APOLLO_STATE__\s*=\s*(.*?)</script>`)
var apolloFunction = regexp.MustCompile(`function\s*\([^)]*\)\s*{[^}]*}`)
var trailingComma = regexp.MustCompile(`,\s*([}\]])`)

func fromInitState(html string) (unified.Object, error) {
	match := initState.FindStringSubmatch(html)
	if match == nil {
		return nil, nil
	}
	raw := strings.TrimRight(strings.Trim(match[1], " \t\r\n\v\x00"), ";")
	data, err := decodePHPJSON(raw)
	if err != nil {
		cleaned := stripslashes(raw)
		cleaned = strings.NewReplacer(
			`"{"err_msg":"launchApplication:fail"}"`, `"err_msg","launchApplication:fail"`,
			`"{"err_msg":"system:access_denied"}"`, `"err_msg","system:access_denied"`,
		).Replace(cleaned)
		data, err = decodePHPJSON(cleaned)
		if err != nil {
			return failure(500, "JSON解析错误: "+err.Error()), nil
		}
	}
	if _, ok := arrayValues(data); !ok {
		return nil, fmt.Errorf("kuaishou INIT_STATE root is not an array")
	}
	var first any
	if object, ok := data.(*phpObject); ok {
		for _, key := range object.keys {
			value := object.fields[key]
			if strings.HasPrefix(key, "tusjoh") && (field(value, "fid") != nil || field(value, "photo") != nil) {
				first = value
				break
			}
		}
	}
	if first == nil {
		return nil, nil
	}
	photo := fallback(field(first, "photo"), unified.Array{})
	var musicValue any = unified.Array{}
	musicSource := fallback(field(photo, "music"), field(photo, "soundTrack"), unified.Array{})
	if truthy(musicSource) {
		musicValue = unified.Object{
			"name":   plain(fallback(field(musicSource, "name"), "")),
			"artist": plain(fallback(field(musicSource, "artist"), "")),
			"cover":  plain(fallback(field(musicSource, "imageUrls", "0", "url"), field(musicSource, "avatarUrls", "0", "url"), "")),
			"url":    plain(fallback(field(musicSource, "audioUrls", "0", "url"), "")),
		}
	}
	images := fallback(field(photo, "ext_params", "atlas", "list"), unified.Array{})
	if truthy(images) {
		list, ok := arrayValues(images)
		if !ok {
			return nil, fmt.Errorf("kuaishou atlas list is not an array")
		}
		pictures := make(unified.Array, 0, len(list))
		for _, value := range list {
			path, err := scalarString(value)
			if err != nil {
				return nil, err
			}
			pictures = append(pictures, "http://tx2.a.yximgs.com/"+path)
		}
		musicPath, err := scalarString(fallback(field(photo, "ext_params", "atlas", "music"), ""))
		if err != nil {
			return nil, err
		}
		result := baseData(photo)
		result["type"], result["count"], result["music"], result["images"], result["api"] = "image", len(list), "http://txmov2.a.kwimgs.com"+musicPath, pictures, 1
		return success("success", result), nil
	}
	if field(photo, "photoType") == "SINGLE_PICTURE" || field(photo, "singlePicture") == true {
		picture := field(photo, "coverUrls", "0", "url")
		if truthy(picture) {
			result := baseData(photo)
			result["type"], result["cover"], result["url"] = "image", plain(picture), plain(picture)
			result["images"], result["music"] = unified.Array{plain(picture)}, musicValue
			return success("解析成功", result), nil
		}
	}
	if field(photo, "mainMvUrls") != nil || field(photo, "manifest") != nil || field(photo, "photoType") == "VIDEO" {
		candidates, err := mediaCandidates(fallback(field(photo, "manifest"), unified.Array{}), fallback(field(photo, "mainMvUrls"), unified.Array{}))
		if err != nil {
			return nil, err
		}
		if len(candidates) > 0 {
			result := baseData(photo)
			result["type"], result["cover"] = "video", plain(fallback(field(photo, "coverUrls", "0", "url"), ""))
			result["url"] = field(candidates[0], "sources", "0", "url")
			result["video_candidates"], result["music"] = candidates, musicValue
			return success("解析成功", result), nil
		}
	}
	return nil, nil
}

func baseData(photo any) unified.Object {
	return unified.Object{
		"author": plain(fallback(field(photo, "userName"), "")),
		"avatar": plain(fallback(field(photo, "headUrl"), "")),
		"like":   plain(fallback(field(photo, "likeCount"), 0)),
		"time":   plain(fallback(field(photo, "timestamp"), 0)),
		"title":  plain(fallback(field(photo, "caption"), "")),
	}
}

func fromApolloState(html, id, contentType string) (unified.Object, error) {
	match := apolloState.FindStringSubmatch(html)
	if match == nil {
		return nil, nil
	}
	raw := apolloFunction.ReplaceAllString(match[1], ":")
	raw = trailingComma.ReplaceAllString(raw, "$1")
	raw = strings.ReplaceAll(raw, ";(:());", "")
	data, err := decodePHPJSON(raw)
	if err != nil {
		return nil, nil
	}
	info := field(data, "defaultClient")
	if !truthy(info) {
		return nil, nil
	}
	video := field(info, "VisionVideoDetailPhoto:"+id)
	if !truthy(video) {
		return nil, nil
	}
	var author any
	if object, ok := info.(*phpObject); ok {
		for _, key := range object.keys {
			if strings.HasPrefix(key, "VisionVideoDetailAuthor:") {
				author = object.fields[key]
				break
			}
		}
	}
	candidates, err := mediaCandidates(fallback(field(video, "manifestH265", "json"), unified.Array{}),
		unified.Array{&phpObject{keys: []string{"url"}, fields: unified.Object{"url": fallback(field(video, "photoUrl"), "")}}})
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	kind := "video"
	if contentType == "photo" {
		kind = "image"
	}
	return success("解析成功", unified.Object{
		"type": kind, "author": plain(fallback(field(author, "name"), "")),
		"avatar": plain(fallback(field(author, "headerUrl"), "")),
		"title":  plain(fallback(field(video, "caption"), "")),
		"cover":  plain(fallback(field(video, "coverUrl"), "")),
		"url":    field(candidates[0], "sources", "0", "url"), "video_candidates": candidates,
	}), nil
}

func mediaCandidates(manifest, main any) (unified.Array, error) {
	if raw, ok := manifest.(string); ok {
		// PHP tolerates a malformed optional manifest, keeping valid main URLs.
		manifest, _ = decodePHPJSON(raw)
	}
	mainList, ok := arrayValues(main)
	if !ok {
		return nil, fmt.Errorf("kuaishou mainMvUrls is not an array")
	}
	out, sources := unified.Array{}, unified.Array{}
	for _, source := range mainList {
		if _, array := arrayValues(source); array && unified.MediaURL(field(source, "url")) != "" {
			sources = append(sources, plain(source))
		}
	}
	if len(sources) > 0 {
		out = append(out, unified.Object{"sources": sources})
	}
	sets := unified.Array{}
	if _, ok := arrayValues(manifest); ok {
		var valid bool
		sets, valid = arrayValues(fallback(field(manifest, "adaptationSet"), unified.Array{}))
		if !valid {
			return nil, fmt.Errorf("kuaishou adaptationSet is not an array")
		}
	}
	for _, set := range sets {
		representations, ok := arrayValues(fallback(field(set, "representation"), unified.Array{}))
		if !ok {
			return nil, fmt.Errorf("kuaishou representations are not an array")
		}
		for _, representation := range representations {
			sources := unified.Array{}
			if source := fallback(field(representation, "url"), ""); unified.MediaURL(source) != "" {
				sources = append(sources, unified.Object{"url": plain(source)})
			}
			backups, ok := arrayValues(fallback(field(representation, "backupUrl"), unified.Array{}))
			if !ok {
				return nil, fmt.Errorf("kuaishou backupUrl is not an array")
			}
			for _, backup := range backups {
				var source any = backup
				if _, array := arrayValues(backup); !array {
					source = &phpObject{keys: []string{"url"}, fields: unified.Object{"url": backup}}
				}
				if unified.MediaURL(field(source, "url")) != "" {
					sources = append(sources, plain(source))
				}
			}
			if len(sources) == 0 {
				continue
			}
			candidate := unified.Object{"sources": sources}
			for _, key := range []string{"width", "height"} {
				value := field(representation, key)
				if number, ok := unified.Numeric(value); ok && number > 0 {
					candidate[key], _ = unified.PHPInt(value)
				}
			}
			if codec, ok := field(representation, "codec").(string); ok {
				candidate["codec"] = codec
			}
			// avgBitrate/maxBitrate have no established unit or audio scope.
			// Preserve that absence rather than guessing total/video bps.
			out = append(out, candidate)
		}
	}
	return out, nil
}

func success(message string, data unified.Object) unified.Object {
	return unified.Object{"code": 200, "msg": message, "data": data}
}
