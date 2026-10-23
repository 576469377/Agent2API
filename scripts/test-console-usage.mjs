import test from "node:test";
import assert from "node:assert/strict";
import { buildUsage, cachedUsage, curveSeries, curvePath, DAY, modelColor, modelKey, lifetimeUsage, usageComposition, heatmapColumns, liveGeneration, sampleLive } from "../internal/web/static/usage.js";
import { usageView, usageTooltip, lifetimeUsageView, liveUsageView, usageMethodologyView, usageDayView, usageWeekView, usageTokenValue } from "../internal/web/static/usage-view.js";
import { escapeHTML, number, compact, icon } from "../internal/web/static/core.js";
const now=Date.parse("2026-10-15T10:00:00Z");
const row=(platform,model,tokens,total=1)=>({platform,model,total,ok:total,failed:0,input_tokens:tokens,output_tokens:0});
const fixture={total:99999,usage_history:{timezone:"UTC",tracking_since:"2026-10-09T12:00:00Z",retention_days:366,legacy_requests:100,
 days:[{date:"2026-10-08",models:[row("workbuddy","hy3",20)]},{date:"2026-10-10",models:[row("workbuddy","hy3",60),row("workbuddy-global","hy3",90)]},{date:"2026-10-15",models:[row("workbuddy-global","hy3",5,0)]}]}};
const state={range:"7",platform:"all",now,metric:"tokens",heatMetric:"tokens",hidden:new Set()};
const ui={e:escapeHTML,n:number,compact,icon,L:(zh)=>zh};
test("derived usage is reused per snapshot and refreshed for filters, allocations, midnight and new data",()=>{
 const metrics=structuredClone(fixture), options={...state,allocation:{enabled:false,days:7}};
 const first=cachedUsage(metrics,options);
 assert.deepEqual(first,buildUsage(metrics,options));
 assert.equal(cachedUsage(metrics,{...options,metric:"total",hidden:new Set(["m"])}),first);
 const domestic=cachedUsage(metrics,{...options,platform:"workbuddy"});
 assert.equal(domestic.tokens,60);assert.notEqual(domestic,first);
 options.range="30";
 assert.equal(cachedUsage(metrics,options).days.length,30);
 options.allocation.enabled=true;
 const allocated=cachedUsage(metrics,options);
 assert.ok(allocated.hasEstimates);
 options.allocation.days=3;
 assert.notEqual(cachedUsage(metrics,options).allocation.start,allocated.allocation.start);
 const nextDay=cachedUsage(metrics,{...options,now:Math.floor(now/DAY)*DAY+DAY});
 assert.equal(nextDay.days.at(-1).date,"2026-10-16");
 const updated=structuredClone(metrics);
 updated.usage_history.days.at(-1).models[0].input_tokens=123;
 assert.notEqual(cachedUsage(updated,state).tokens,first.tokens);
 assert.equal(cachedUsage(null,state),null);
 assert.deepEqual(metrics,fixture);
});
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
 const evil={...fixture,usage_history:{...fixture.usage_history,sources:[{kind:"snapshot",name:'<img src=x onerror=alert(1)>',imported_requests:1,snapshot_requests:2,snapshot_input:3,snapshot_output:4}],days:[{date:"2026-10-15",models:[row("p",'<img src=x onerror=alert(1)>',10)]}]}};
 const html=usageView(evil,state,ui);
 assert.ok(html.includes("每日用量"));assert.ok(html.includes("模型用量"));
 assert.ok(!html.includes("部分记录"));assert.ok(!html.includes("usage-coverage-banner"));
 const methodology=usageMethodologyView(evil,state,ui);
 assert.ok(methodology.includes("输入 Token + 输出 Token"));assert.ok(methodology.includes("数据覆盖"));
 assert.ok(!methodology.includes("<img src=x"));assert.ok(methodology.includes("&lt;img"));
 assert.ok(methodology.includes("7 tokens"));assert.ok(methodology.includes("历史快照"));
 assert.ok(!methodology.includes("已导入"));assert.ok(!methodology.includes("已恢复"));
 assert.ok(!html.includes("<img src=x"));assert.ok(html.includes("&lt;img"));
 assert.ok(usageTooltip(fixture,state,1,ui).includes("150"));
 assert.ok(usageTooltip(fixture,{...state,range:"30"},0,ui).includes("暂无记录"));
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
 assert.ok(!html.includes("<details"));assert.ok(html.includes('data-action="usage-methodology"'));
 assert.equal(lifetimeUsage(undefined),null);
 const daily=usageView(metrics,state,ui);
 assert.ok(daily.includes("Token 用量"));assert.ok(!daily.includes("最近 200 条样本"));
 assert.ok(usageMethodologyView(metrics,state,ui).includes("最近 200 条请求样本"));
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

test("heatmap daily, weekly and cumulative keep natural-week alignment and actual totals",()=>{
 const calendar=Array.from({length:14},(_,i)=>({date:new Date(now-(13-i)*DAY).toISOString().slice(0,10),ms:Math.floor((now-(13-i)*DAY)/DAY)*DAY,tokens:i+1,total:1,coverage:i<2?"unknown":"complete"}));
 const weekly=heatmapColumns(calendar,"weekly"),cumulative=heatmapColumns(calendar,"cumulative");
 const sum=calendar.slice(2).reduce((s,d)=>s+d.tokens,0);
 assert.equal(weekly.reduce((s,c)=>s+c.value,0),sum);assert.equal(cumulative.at(-1).value,sum);
 assert.equal(cumulative[0].coverage,"unknown");assert.equal(cumulative[1].coverage,"partial");
 assert.equal(heatmapColumns(calendar,"daily")[0].cells[0].coverage,"blank");
 assert.equal(weekly[0].start,"2026-10-02");
 const currentWeek=heatmapColumns(calendar.slice(-2),"weekly").at(-1);
 assert.equal(currentWeek.cells.filter(c=>c.level>0&&c.coverage!=="blank").length,7);
 assert.equal(currentWeek.cells.at(-1).day,null); // Bar rows are not future dates.
 const html=usageView(fixture,{...state,heatMode:"weekly",calendarExpanded:true},ui);
 assert.ok(html.includes("每日"));assert.ok(html.includes("每周"));assert.ok(html.includes("累计"));assert.ok(html.includes('data-action="usage-week"'));
});
test("recovered activity is visible but missing token records never become a false zero curve",()=>{
 const metrics={usage_history:{timezone:"UTC",tracking_since:"2026-10-15",days:[{date:"2026-10-11",models:[{...row("p","m",0,5),unknown_token_requests:5}]}]}};
 const usage=buildUsage(metrics,state);assert.equal(usage.activeDays,1);assert.equal(usage.unknown_token_requests,5);
 assert.equal(curveSeries(usage,"tokens")[0].values[2],null);assert.equal(curveSeries(usage,"total")[0].values[2],5);
 const html=usageView(metrics,{...state,calendarExpanded:true},ui);assert.ok(html.includes("activity-only"));assert.ok(html.includes("Token 未上报"));
 const day=usage.days[2];
 assert.equal(usageTokenValue(day,number),"—");
 assert.ok(!usageTooltip(metrics,state,2,ui).includes("0 tokens"));
 assert.ok(usageDayView(day,ui).includes("Token 未上报"));
 const unrecorded={usage_history:{...metrics.usage_history,days:[{date:"2026-10-11",models:[{...row("","m",0,5),unknown_token_requests:5}]}]}};
 assert.equal(buildUsage(unrecorded,{...state,platform:"unknown"}).total,5);
});

test("missing usage from one model preserves today's measured tokens and other model curves",()=>{
 const metrics={usage_history:{timezone:"UTC",tracking_since:"2026-10-15",days:[{date:"2026-10-15",models:[
  row("workbuddy","hy3",100),
  {...row("trae-cn","flash",200,3),output_tokens:20,unknown_token_requests:1},
  {...row("kiro","glm-5",0,4),unknown_token_requests:3},
  row("cursor","composer",0),
 ]}]}};
 const before=JSON.stringify(metrics),usage=buildUsage(metrics,state),curves=curveSeries(usage);
 const point=(platform,model)=>curves.find(series=>series.key===modelKey(platform,model)).values.at(-1);
 assert.equal(usage.tokens,320);assert.equal(usage.total,9);assert.equal(usage.unknown_token_requests,4);
 assert.equal(point("workbuddy","hy3"),100);
 assert.equal(point("trae-cn","flash"),220);
 assert.equal(point("kiro","glm-5"),null);
 assert.equal(point("cursor","composer"),0);
 const html=usageView(metrics,{...state,calendarExpanded:true},ui),tooltip=usageTooltip(metrics,state,6,ui);
 assert.ok(html.includes("2026-10-15 UTC · ≥ 320 tokens"));
 assert.ok(tooltip.includes("workbuddy/hy3"));assert.ok(tooltip.includes("trae-cn/flash"));
 assert.ok(tooltip.includes("≥ 220"));assert.ok(tooltip.includes("≥ 320 tokens"));
 assert.ok(!tooltip.includes("kiro/glm-5"));
 const day=usage.days.at(-1),details=usageDayView(day,ui),week=usageWeekView([day],false,ui);
 assert.ok(details.includes("≥ 220 tokens"));assert.ok(details.includes("100 tokens"));
 assert.equal((details.match(/Token 未上报/g)||[]).length,1);
 assert.ok(week.includes("≥ 320 tokens"));assert.ok(!week.includes("明细不完整"));
 assert.ok(!html.includes("Token 明细未留存"));
 const allocatedState={...state,range:"all",allocation:{enabled:true,days:7}};
 const allocated=buildUsage({...metrics,total:39,input_tokens:600,output_tokens:20},allocatedState);
 assert.ok(allocated.hasEstimates);assert.equal(usageTokenValue(allocated,number),"≥ 620");
 for (const mode of ["weekly","cumulative"]) {
  const column=heatmapColumns(usage.calendar,mode).at(-1);
  assert.equal(column.value,320);assert.equal(column.unknown_token_requests,4);
  assert.ok(usageView(metrics,{...state,calendarExpanded:true,heatMode:mode},ui).includes("≥ 320 tokens"));
 }
 assert.equal(buildUsage(metrics,{...state,platform:"workbuddy"}).tokens,100);
 assert.equal(JSON.stringify(metrics),before);
});

test("other-model totals retain measured usage when one of their models has no tokens",()=>{
 const models=Array.from({length:9},(_,i)=>row("p","m"+i,(i+1)*10));
 models.push({...row("kiro","glm",0,2),unknown_token_requests:2});
 const metrics={usage_history:{timezone:"UTC",tracking_since:"2026-10-15",days:[{date:"2026-10-15",models}]}};
 const usage=buildUsage(metrics,state),curves=curveSeries(usage),other=curves.find(series=>series.key==="other");
 assert.equal(other.values.at(-1),60);assert.equal(other.missingTokens.at(-1),true);
 assert.equal(curves.reduce((sum,series)=>sum+series.values.at(-1),0),450);
 assert.ok(usageTooltip(metrics,state,6,ui).includes("≥ 60"));
});
test("live samples distinguish calibrated token estimates from actual character speed and reject duplicate snapshots",()=>{
 const data={sampled_at:new Date(now).toISOString(),in_flight:1,active:[{id:1,platform:"p",model:"m",output_characters:100,characters_per_second:20}],last_completed:{output_tokens:40,gen_ms:2000,time:new Date(now-DAY).toISOString()}};
 assert.equal(liveGeneration(data).tokensPerSecond,null);assert.equal(liveGeneration(data).lastSpeed,20);
 const points=sampleLive([],data);assert.equal(sampleLive(points,data).length,1);
 const calibrated={...data,active:[{...data.active[0],estimated_tokens_per_second:5,estimated_output_tokens:25}]};
 assert.equal(liveGeneration(calibrated).tokensPerSecond,5);
 const html=liveUsageView(calibrated,"",points,{...ui,timestamp:()=>"time"});assert.ok(html.includes("接收速度 · 估算"));assert.ok(html.includes("≈ 25"));
 assert.ok(liveUsageView(data,"",points,{...ui,timestamp:()=>"time"}).includes("Token 待结算"));
});


test("live inspection keeps request details available without confusing estimates, errors or idle state",()=>{
 const data={sampled_at:new Date(now).toISOString(),in_flight:1,active:[{platform:"global",model:'<img src=x onerror=alert(1)>',output_characters:17,characters_per_second:2}],last_completed:null};
 const html=liveUsageView(data,"",[],{...ui,timestamp:()=>"time"});
 assert.ok(html.includes('<details id="liveRequestDetails"'));
 assert.ok(html.includes('id="liveRequestToggle"'));
 assert.ok(!html.includes('<img src=x'));
 assert.ok(html.includes('&lt;img'));
 assert.ok(html.includes('Token 待结算'));
 assert.ok(!html.includes('≈ 17'));
 const idle=liveUsageView({...data,in_flight:0,active:[]},"",[],{...ui,timestamp:()=>"time"});
 assert.ok(!idle.includes('id="liveRequestDetails"'));
 assert.ok(idle.includes('暂无进行中的请求'));
 const failed=liveUsageView(data,"Unavailable",[],{...ui,timestamp:()=>"time"});
 assert.ok(failed.includes('data-action="refresh-live"'));
 assert.ok(!failed.includes('live-request-details'));
});

test("stalled and stale live snapshots are not presented as ongoing generation",()=>{
 const data={sampled_at:new Date(now).toISOString(),in_flight:1,active:[{model:"m",output_characters:10,characters_per_second:0,last_output_at:new Date(now-4000).toISOString()}]};
 const html=liveUsageView(data,"",[],{...ui,timestamp:()=>"time"});
 assert.ok(html.includes("等待后续输出"));assert.ok(!html.includes("生成中"));
 const stale=liveUsageView(data,"",[],{...ui,timestamp:()=>"time",stale:true});
 assert.ok(stale.includes("实时数据已过期"));assert.ok(!stale.includes("live-request-list"));assert.ok(!stale.includes("0.0"));
 const first=sampleLive([],data);
 const afterGap=sampleLive(first,{...data,sampled_at:new Date(now+5000).toISOString()});
 assert.equal(afterGap.length,3);assert.equal(afterGap[1].value,null);
 const graph=liveUsageView({...data,in_flight:0,active:[]},"",afterGap,{...ui,timestamp:()=>"time"});
 assert.ok(!graph.includes("NaN"));assert.ok(!graph.includes("Infinity"));
});
