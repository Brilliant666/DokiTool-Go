package xiaohongshu

import (
	"encoding/json"
	"sort"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// Preserve the real flags2097472 JSON encode/decode boundary without NUMERIC_CHECK.
func legacyWire(value unified.Object)(unified.Object,error){
	body,err:=unified.PHPJSON(wireValue(value),true);if err!=nil{return nil,err}
	decoded,err:=unified.Decode(body);if err!=nil{return nil,err}
	return unified.Map(canonicalZero(decoded)),nil
}
func wireValue(value any)any{
	if fields:=unified.Map(value);fields!=nil{
		if len(fields)==0{return unified.Array{}}
		keys:=make([]string,0,len(fields));for key:=range fields{keys=append(keys,key)};sort.Strings(keys)
		out:=make(unified.PHPObject,0,len(keys));for _,key:=range keys{out=append(out,unified.PHPField{Key:key,Value:wireValue(fields[key])})};return out
	}
	if values:=unified.Slice(value);values!=nil{out:=make(unified.Array,0,len(values));for _,child:=range values{out=append(out,wireValue(child))};return out}
	return value
}
func canonicalZero(value any)any{
	if value==json.Number("-0"){return json.Number("0")}
	if fields:=unified.Map(value);fields!=nil{for key,child:=range fields{fields[key]=canonicalZero(child)}}
	if values:=unified.Slice(value);values!=nil{for index,child:=range values{values[index]=canonicalZero(child)}}
	return value
}
