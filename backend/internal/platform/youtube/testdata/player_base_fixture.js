var _yt_player={};
(function(g){
  var window=this;
  g.sts={signatureTimestamp:20760};
  function U(u){this.m={};}
  U.prototype.set=function(k,v){this.m[k]=v;};
  U.prototype.get=function(k){return this.m[k];};
  U.prototype.clone=function(){return new U("");};
  U.prototype.apply=function(){
    if(this.m.s){var d=decodeURIComponent(this.m.s);this.m.s=encodeURIComponent("SOLVED_SIG_"+d.split("").reverse().join(""));}
    if(this.m.n){this.m.n="SOLVED_N_"+this.m.n;}
  };
  var makeUrl=function(raw,k,v){
    var u=new U(raw);
    u.set("alr","yes");
    if(k&&v!==undefined){u.set(k,v);}
    return u;
  };
})(_yt_player);
