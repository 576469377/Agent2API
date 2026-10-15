import test from "node:test";
import assert from "node:assert/strict";
import { buildUsage, curveSeries, curvePath, DAY, modelColor, modelKey, lifetimeUsage, usageComposition } from "../internal/web/static/usage.js";
import { usageView, usageTooltip, lifetimeUsageView } from "../internal/web/static/usage-view.js";
import { escapeHTML, number, compact, icon } from "../internal/web/static/core.js";
const now=Date.parse("2026-10-15T10:00:00Z");
const row=(platform,model,tokens,total=1)=>({platform,model,total,ok:total,failed:0,input_tokens:tokens,output_tokens:0});
const fixture={total:99999,usage_history:{timezone:"UTC",tracking_since:"2026-10-09T12:00:00Z",retention_days:366,legacy_requests:100,
 days:[{date:"2026-10-08",models:[row("workbuddy","hy3",20)]},{date:"2026-10-10",models:[row("workbuddy","hy3",60),row("workbuddy-global","hy3",90)]},{date:"2026-10-15",models:[row("workbuddy-global","hy3",5,0)]}]}};
const state={range:"7",platform:"all",now,metric:"tokens",heatMetric:"tokens",hidden:new Set()};
const ui={e:escapeHTML,n:number,compact,icon,L:(zh)=>zh};
test("calendar totals use durable days, preserve regional identity and never use lifetime as daily usage",()=>{
 const usage=buildUsage(fixture,state);
 assert.equal(usage.days.length,7);assert.equal(usage.tokens,155);assert.equal(usage.total,2);
 assert.equal(usage.models.length,2);assert.equal(usage.models[0].platform,"workbuddy-global");
 assert.equal(buildUsage(fixture,{...state,platform:"workbuddy"}).tokens,60);
 assert.equal(usage.days[0].coverage,"partial");assert.equal(usage.days[1].coverage,"complete");
 assert.equal(usage.days[2].tokens,0);assert.equal(usage.activeDays,1);
 assert.equal(usage.peak.date,"2026-10-10");
});
test("missing legacy dates are unknown rather than zero; a partial sample is explicitly identified",()=>{
 const usage=buildUsage(fixture,{...state,range:"30"});
 assert.equal(usage.days.length,30);
 assert.equal(usage.days[0].coverage,"unknown");
 assert.equal(usage.days.find(d=>d.date==="2026-10-08").coverage,"partial");
 const curves=curveSeries(usage);assert.equal(curves[0].values[0],null);
 const legacy=buildUsage({total:99999,recent:[{time:"2026-10-14T23:30:00-08:00",platform:"workbuddy",model:"hy3",ok:true,input_tokens:12}]},state);
 assert.equal(legacy.tokens,12);assert.equal(legacy.preview,true);
 assert.equal(legacy.days.at(-1).coverage,"partial");
 assert.equal(legacy.days.at(-2).coverage,"unknown");
});
test("top curves include the remainder and preserve all daily usage with stable model colors",()=>{
 const models=Array.from({length:9},(_,i)=>row("p","m"+i,(i+1)*10));
 const usage=buildUsage({usage_history:{timezone:"UTC",tracking_since:"2026-10-01T00:00:00Z",days:[{date:"2026-10-15",models}]}},state);
 const curves=curveSeries(usage);assert.equal(curves.length,7);assert.equal(curves.at(-1).key,"other");
 assert.equal(curves.reduce((sum,m)=>sum+m.values.at(-1),0),450);
 assert.equal(modelColor(modelKey("p","m1")),buildUsage({usage_history:{timezone:"UTC",tracking_since:"2026-10-01",days:[{date:"2026-10-15",models:[models[1]]}]}},state).models[0].color);
 const hidden=new Set([curves[0].key]);assert.equal(curveSeries(usage,"tokens",hidden)[0].hidden,true);
 assert.equal(usage.tokens,450);
});
test("unknown gaps never become interpolated lines and the midnight boundary uses UTC",()=>{
 const path=curvePath([0,10,null,20,0],i=>i*10,v=>100-v);
 assert.equal((path.match(/M/g)||[]).length,2);assert.equal((path.match(/ C/g)||[]).length,2);
 const midnight=buildUsage(fixture,{...state,now:Date.parse("2026-10-15T00:00:00Z")});
 assert.equal(midnight.days.at(-1).date,"2026-10-15");
 assert.equal(buildUsage(undefined,state),null);
});
test("charts expose token denominator, partial coverage and escaped model text; tooltip is readable",()=>{
 const evil={...fixture,usage_history:{...fixture.usage_history,days:[{date:"2026-10-15",models:[row("p",'<img src=x onerror=alert(1)>',10)]}]}};
 const html=usageView(evil,state,ui);
 assert.ok(html.includes("每日模型趋势"));assert.ok(html.includes("模型 Token 占比"));
 assert.ok(html.includes("斜纹格表示未知历史"));assert.ok(html.includes("Token 不等于积分"));
 assert.ok(!html.includes("<img src=x"));assert.ok(html.includes("&lt;img"));
 assert.ok(usageTooltip(fixture,state,1,ui).includes("150"));
 assert.ok(usageTooltip(fixture,{...state,range:"30"},0,ui).includes("历史未保留"));
});

test("lifetime usage survives daily filters and the 200-request buffer without adding reasoning twice",()=>{
 const metrics={total:47249,input_tokens:325755877,output_tokens:6203673,reasoning_tokens:1000000,recent:Array.from({length:200},()=>({time:new Date(now).toISOString(),model:"recent",input_tokens:1,ok:true}))};
 const totals=lifetimeUsage(metrics);
 assert.equal(totals.tokens,331959550);assert.equal(totals.total,47249);
 assert.equal(buildUsage(metrics,state).tokens,200);
 assert.equal(buildUsage(metrics,{...state,range:"30",platform:"missing"}).tokens,0);
 assert.deepEqual(lifetimeUsage(metrics),totals);
 const html=lifetimeUsageView(metrics,ui);
 assert.ok(html.includes("331,959,550"));assert.ok(html.includes('data-lifetime="total"'));
 assert.ok(!html.includes("<details"));assert.ok(html.includes("不受最近 200 条日志限制"));
 assert.equal(lifetimeUsage(undefined),null);
 const daily=usageView(metrics,state,ui);
 assert.ok(daily.includes("样本 Token"));assert.ok(daily.includes("留存样本"));
 assert.ok(daily.includes("完整历史累计请看页面上方"));
});
test("lifetime token composition uses all persisted groups and remains distinct from request shares",()=>{
 const groups=Array.from({length:8},(_,i)=>({key:"m"+i,total:8-i,input_tokens:i*10,output_tokens:10}));
 const before=JSON.stringify(groups),tokens=usageComposition(groups,"tokens"),calls=usageComposition(groups,"total");
 assert.equal(tokens.total,360);assert.equal(tokens.top[0].key,"m7");
 assert.equal(calls.total,36);assert.equal(calls.top[0].key,"m0");
 assert.equal(tokens.top.reduce((s,r)=>s+r.value,0)+tokens.other,tokens.total);
 assert.equal(JSON.stringify(groups),before);
 assert.deepEqual(usageComposition([{key:"empty",total:10}],"tokens"),{total:0,top:[],other:0});
});
