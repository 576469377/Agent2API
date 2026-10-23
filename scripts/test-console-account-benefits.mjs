import { test } from "node:test";
import assert from "node:assert/strict";
import { benefitAmount, renderAccountBenefits, accountBenefitEndpoint, renderCheckinOverview } from "../internal/web/static/account-benefits.js";

test("unknown allowance is distinct from zero USD and credits",()=>{
  assert.equal(benefitAmount(undefined,"USD"),"—");
  assert.equal(benefitAmount(0,"USD"),"$0");
  assert.equal(benefitAmount(1.03,"USD"),"$1.03");
  assert.equal(benefitAmount(-1,"credits"),"—");
  const html=renderAccountBenefits({platform:"cursor",label:"a",benefits:{status:"ready",balances:[{id:"plan",name:"Plan",unit:"USD",used:1.03}]}},"en");
  assert.match(html,/\$1.03/);assert.match(html,/<dd>—<\/dd>/);
});
test("pooled account actions require platform and label with correct encoding",()=>{
  const url=new URL(accountBenefitEndpoint("checkin",{platform:"workbuddy",label:'a & b/"'},true),"http://localhost");
  assert.equal(url.searchParams.get("label"),'a & b/"');assert.equal(url.searchParams.get("platform"),"workbuddy");
  assert.throws(()=>accountBenefitEndpoint("checkin",{platform:"workbuddy"}));
  assert.throws(()=>accountBenefitEndpoint("unknown",{platform:"workbuddy",label:"a"}));
});
test("all check-in accounts have their own controls and escaped identity",()=>{
  const html=renderCheckinOverview([{platform:"workbuddy",label:'"><script>x</script>',checkin:{enabled:true,time:"09:00"}},{platform:"trae-cn",label:"b",checkin:{available:false}}]);
  assert.match(html,/data-platform="workbuddy"/);assert.match(html,/data-platform="trae-cn"/);assert.doesNotMatch(html,/<script>/);assert.match(html,/签到未开放/);
});
test("stale balances remain explicitly marked and do not become live measurements",()=>{
  const html=renderAccountBenefits({benefits:{status:"stale",balances:[{name:"积分余额",remaining:220,unit:"credits"}]},checkin:{checked_in:true}},"zh",true);
  assert.match(html,/220 积分/);assert.match(html,/上次读数/);assert.match(html,/今日已签到/);
});
