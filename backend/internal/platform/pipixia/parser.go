// Package pipixia ports the fixed PHP inert-page parser. Only metadata is
// requested; comments cannot supply the work and media bytes are never fetched.
package pipixia

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type Parser struct { executor fetch.Executor; clock func() time.Time }
func New(executor fetch.Executor) *Parser { return NewWithClock(executor,time.Now) }
func NewWithClock(executor fetch.Executor, clock func() time.Time) *Parser { return &Parser{executor:executor,clock:clock} }

var itemPath=regexp.MustCompile(`^/(?:ppx/)?item/([1-9][0-9]{0,19})/?$`)
var itemID=regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

func AllowedURL(raw string) bool {
	if unified.MediaURL(raw)=="" {return false}
	u,err:=url.Parse(raw);if err!=nil||u.User!=nil||u.Opaque!="" {return false}
	host:=strings.ToLower(u.Hostname());if host!="pipix.com"&&!strings.HasSuffix(host,".pipix.com"){return false}
	return (strings.EqualFold(u.Scheme,"http")||strings.EqualFold(u.Scheme,"https"))&&(u.Port()==""||u.Port()=="80"||u.Port()=="443")
}
func ItemID(raw string) string {
	if !AllowedURL(raw){return ""};u,_:=url.Parse(raw)
	if matched:=itemPath.FindStringSubmatch(u.EscapedPath());matched!=nil{return matched[1]}
	value,exists,scalar:=queryValue(u.RawQuery,"item_id");if exists&&scalar&&itemID.MatchString(value){return value};return ""
}
func upgrade(raw string) string {if len(raw)>=7&&strings.EqualFold(raw[:7],"http://"){return "https://"+raw[7:]};return raw}
func redirect(from,location string) string {
	if strings.HasPrefix(location,"//"){location="https:"+location}else if strings.HasPrefix(location,"/"){base,err:=url.Parse(from);if err!=nil{return ""};location="https://"+base.Hostname()+location}
	if !AllowedURL(location){return ""};return upgrade(location)
}

func (p *Parser) Parse(ctx context.Context,input string,now int64)(unified.Object,error){
	legacy,err:=p.parseLegacy(ctx,input);if err!=nil{return nil,err};wire,err:=legacyWire(legacy);if err!=nil{return nil,err};return adaptLegacy(wire,input,now),nil
}
func (p *Parser) parseLegacy(parent context.Context,input string)(unified.Object,error){
	if err:=parent.Err();err!=nil{return nil,err}
	if !AllowedURL(input){return failure(400,"请输入有效的皮皮虾分享链接","INVALID_URL"),nil}
	if p==nil||p.executor==nil{return nil,fmt.Errorf("pipixia request executor is not configured")}
	clock:=p.clock;if clock==nil{clock=time.Now};deadline:=clock().Add(20*time.Second)
	ctx,cancel:=context.WithTimeout(parent,20*time.Second);defer cancel()
	current:=upgrade(input);id:=ItemID(current)
	for count:=0;id==""&&count<5;count++{
		response,problem:=p.request(ctx,current,deadline,clock,"pipixia-share")
		if err:=parent.Err();err!=nil{return nil,err};if problem!=nil{return problem,nil}
		if response.StatusCode<300||response.StatusCode>=400{break}
		location:="";for _,value:=range response.Header.Values("Location"){location=strings.Trim(value," \t\n\r\x00\v")}
		current=redirect(current,location);if current==""{return failure(502,"皮皮虾分享链接跳转异常","UPSTREAM_REDIRECT_INVALID"),nil}
		id=ItemID(current)
		if !clock().Before(deadline){return failure(504,"皮皮虾上游请求超时","UPSTREAM_TIMEOUT"),nil}
	}
	if id==""{return failure(400,"无法从皮皮虾分享链接识别作品，请重新复制分享链接","ITEM_ID_MISSING"),nil}
	target:="https://h5.pipix.com/ppx/item/"+id
	response,problem:=p.request(ctx,target,deadline,clock,"pipixia-page")
	if err:=parent.Err();err!=nil{return nil,err};if problem!=nil{return problem,nil}
	item:=pageItem(string(response.Body));if item==nil{return failure(502,"皮皮虾页面未返回作品数据，可能暂时限制访问，请稍后重试","UPSTREAM_DATA_MISSING"),nil}
	return itemResponse(item,id),nil
}

const mobileUA="Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Mobile/15E148 Safari/604.1"
func (p *Parser) request(ctx context.Context,target string,deadline time.Time,clock func()time.Time,purpose string)(fetch.Response,unified.Object){
	seconds:=math.Ceil(deadline.Sub(clock()).Seconds());if seconds<1{seconds=1}
	policy:=fetch.Policy{Purpose:purpose,AllowedURL:func(raw string)bool{return raw==target&&AllowedURL(raw)&&strings.HasPrefix(strings.ToLower(raw),"https://")},Timeout:time.Duration(seconds)*time.Second,MaxBytes:2<<20,ReadRedirectBody:purpose=="pipixia-page"}
	response,err:=p.executor.Do(ctx,fetch.Request{URL:target,Method:http.MethodGet,Header:http.Header{"Accept":{"text/html,application/json"},"Referer":{"https://h5.pipix.com/"},"User-Agent":{mobileUA}},Policy:policy})
	if err!=nil||int64(len(response.Body))>policy.MaxBytes||(response.URL!=""&&response.URL!=target){
		code:=502;var timeout net.Error;if errors.Is(err,context.DeadlineExceeded)||(errors.As(err,&timeout)&&timeout.Timeout()){code=504}
		return response,failure(code,"皮皮虾上游请求失败，请稍后重试","UPSTREAM_REQUEST_FAILED")
	}
	if response.StatusCode==401||response.StatusCode==403||response.StatusCode==429{return response,failure(502,"皮皮虾暂时限制访问，请稍后重试","UPSTREAM_RESTRICTED")}
	if response.StatusCode<200||response.StatusCode>=400{return response,failure(502,"皮皮虾页面暂时不可用，请检查分享链接或稍后重试","UPSTREAM_HTTP_ERROR")}
	return response,nil
}
func failure(code int,message,reason string)unified.Object{return unified.Object{"code":code,"msg":message,"error":unified.Object{"code":reason,"platform":"pipixia"}}}

func adaptLegacy(legacy unified.Object,source string,now int64)unified.Object{
	code,ok:=unified.PHPInt(legacy["code"]);if !ok{return unified.ContractError("UPSTREAM_INVALID_RESPONSE","解析器返回格式异常",false)}
	if code!=200||unified.Map(legacy["data"])==nil{
		kind:="UPSTREAM_UNAVAILABLE";switch code{case -1,0,201,400:kind="INVALID_INPUT";case 401,403,412:kind="UPSTREAM_BLOCKED";case 404:kind="CONTENT_UNAVAILABLE";case 429:kind="RATE_LIMITED";case 408,504:kind="UPSTREAM_TIMEOUT";case 502:kind="UPSTREAM_INVALID_RESPONSE"}
		return unified.ContractError(kind,unified.Str(legacy["msg"]),kind=="RATE_LIMITED"||kind=="UPSTREAM_TIMEOUT"||kind=="UPSTREAM_UNAVAILABLE")
	}
	data:=unified.SimpleData("pipixia",unified.Map(legacy["data"]),source,now)
	if len(unified.Slice(data["items"]))==0{return unified.ContractError("NO_MEDIA","作品未提供可用媒体",false)}
	return unified.FinalizeData(data,now)
}
