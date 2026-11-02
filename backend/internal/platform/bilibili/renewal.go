package bilibili

import (
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

var uposPath = regexp.MustCompile(`^/upgcxcode/[0-9]{2}/[0-9]{2}/([1-9][0-9]*)/([1-9][0-9]*)-[0-9]+-[0-9]+\.mp4$`)
var traceToken = regexp.MustCompile(`^[0-9a-f]{31}h$`)
var signature = regexp.MustCompile(`^[0-9a-f]{32}$`)
var deadline = regexp.MustCompile(`^[0-9]{10}$`)
var routeVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]$`)

// Only this observed signature layout is understood. Extra fields, duplicate
// keys and unfamiliar encodings keep the original source identity.
const signedFieldNames = "deadline,e,gen,mid,nbs,og,oi,os,platform,trid,uipk"
const completeFieldNames = "V,agrr,build,buvid,bvc,bw,deadline,dl,e,f,gen,lrs,mid,nbs,nettype,og,oi,orderid,os,platform,trid,uipk,uparams,upsig"

func uposObject(raw, cid string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" ||
		!strings.HasPrefix(u.Host, "upos-") || !strings.HasSuffix(u.Host, ".bilivideo.com") {
		return ""
	}
	parts := uposPath.FindStringSubmatch(u.EscapedPath())
	if parts == nil || parts[1] != cid || parts[2] != cid {
		return ""
	}
	fields := strings.Split(u.RawQuery, "&")
	allowed := strings.Split(completeFieldNames, ",")
	if len(fields) != len(allowed) {
		return ""
	}
	seen := make(map[string]bool, len(fields))
	for i, field := range fields {
		name, value, ok := strings.Cut(field, "=")
		if !ok || seen[name] || !slices.Contains(allowed, name) {
			return ""
		}
		seen[name] = true
		switch name {
		case "deadline":
			if !deadline.MatchString(value) {
				return ""
			}
			fields[i] = name
		case "upsig":
			if !signature.MatchString(value) {
				return ""
			}
			fields[i] = name
		case "trid":
			if !traceToken.MatchString(value) {
				return ""
			}
			fields[i] = name
		case "V":
			if !routeVersion.MatchString(value) {
				return ""
			}
			fields[i] = field[:strings.LastIndexByte(field, '.')+1]
		case "uparams":
			members := strings.Split(value, ",")
			slices.Sort(members)
			if strings.Join(members, ",") != signedFieldNames {
				return ""
			}
			fields[i] = name + "=" + signedFieldNames
		}
	}
	slices.Sort(fields)
	base, _, _ := strings.Cut(raw, "?")
	return base + "?" + strings.Join(fields, "&")
}

func markSingleDURL(candidate object, bvid string, page int64, cid string, play, part object) {
	qn, size := intValue(play["quality"]), intValue(part["size"])
	format := unified.Str(play["format"])
	if !videoID.MatchString(bvid) || page <= 0 || qn <= 0 || !strictInteger(play["quality"], qn) ||
		size <= 0 || !strictInteger(part["size"], size) || strings.TrimSpace(format) == "" {
		return
	}
	sources := unified.Slice(candidate["sources"])
	groups := make(map[string][]object)
	var primary string
	for i, value := range sources {
		source := unified.Map(value)
		if source == nil {
			source = object{"url": value}
		}
		identity := uposObject(unified.Str(source["url"]), cid)
		if identity != "" {
			sources[i] = source
			groups[identity] = append(groups[identity], source)
			if i == 0 {
				primary = identity
			}
		}
	}
	for identity, group := range groups {
		// Coexisting distinct routes cannot share an explicit choice. Keep
		// the original identity for every member of that ambiguous group.
		first := unified.SourceIdentity(unified.Str(group[0]["url"]))
		if slices.ContainsFunc(group[1:], func(s object) bool { return unified.SourceIdentity(unified.Str(s["url"])) != first }) {
			continue
		}
		key := unified.PHPIdentityJSON(array{"bilibili-upos-file", bvid, page, cid, qn, format, size, identity}, false)
		for _, source := range group {
			source["private_source_identity"] = key
		}
		if identity == primary {
			// Backup churn does not replace the representation. Replacing its
			// primary object (or making it ambiguous) still fails closed.
			candidate["private_media_identity"] = key
		}
	}
}
