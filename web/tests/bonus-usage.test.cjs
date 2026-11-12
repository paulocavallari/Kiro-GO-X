const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const root = path.join(__dirname, '..');
let source = fs.readFileSync(path.join(root, 'app.js'), 'utf8');
source = source.replace("  if (document.readyState === 'loading') {", "  globalThis.testUI = { render(accounts, locale) { accountsData = accounts; dict.en = locale; currentLang = 'en'; renderAccounts(); }, renderBonusUsage };\n  if (document.readyState === 'loading') {");
const container = { innerHTML: '', querySelectorAll() { return []; } };
const storage = { getItem() { return null; }, removeItem() {} };
const document = {
  readyState: 'loading', addEventListener() {},
  getElementById(id) { return id === 'accountsList' ? container : null; },
  createElement() { return { set textContent(value) { this.innerHTML = String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;'); } }; }
};
const context = { document, location: {origin: ''}, localStorage: storage, sessionStorage: storage, window: {matchMedia() { return {addEventListener() {}}; }} };
vm.runInNewContext(source, context);
const locale = JSON.parse(fs.readFileSync(path.join(root, 'locales/en.json')));
const account = {id:'synthetic', usageCurrent:5, usageLimit:10, usagePercent:0.5, trialUsageCurrent:1, trialUsageLimit:2, trialUsagePercent:0.5, bonusUsageKnown:true, bonusUsageCurrent:2.5, bonusUsageLimit:10};
function render(a) { context.testUI.render([a], locale); return container.innerHTML; }
let html = render(account);
assert(html.indexOf('Main Quota') < html.indexOf('Bonus credits'));
assert(html.indexOf('Bonus credits') < html.indexOf('Trial Quota'));
assert.match(html, /<div class="usage-text"><span>2\.50 \/ 10\.00<\/span><span>25\.0%<\/span><\/div>/);
assert.doesNotMatch(html, /Remaining: 7\.50/);
assert.match(html, /role="progressbar"[^>]*aria-valuenow="25"/);
assert.match(html, /data-usage-pct="25"/);
for (const extra of [{}, {bonusUsageKnown:false}, {bonusUsageKnown:true, bonusUsageCurrent:null, bonusUsageLimit:10}, {bonusUsageKnown:true, bonusUsageCurrent:NaN, bonusUsageLimit:10}]) {
  html = render({...account, bonusUsageKnown:undefined, bonusUsageCurrent:undefined, bonusUsageLimit:undefined, ...extra});
  assert.match(html, /Unavailable/); assert.doesNotMatch(html, /role="progressbar"/);
}
html = render({...account, bonusUsageCurrent:0, bonusUsageLimit:0});
assert.match(html, /0\.00 \/ 0\.00<\/span><span>0\.0%/); assert.match(html, /aria-valuenow="0"/);
html = render({...account, bonusUsageCurrent:1, bonusUsageLimit:0});
assert.match(html, /Unavailable/); assert.doesNotMatch(html, /role="progressbar"/);
html = render({...account, bonusUsageCurrent:12, bonusUsageLimit:10});
assert.match(html, /aria-valuenow="100"/); assert.match(html, /12\.00 \/ 10\.00<\/span><span>120\.0%/); assert.match(html, /usage-fill critical/);
for (const lang of ['en', 'zh']) { const d = JSON.parse(fs.readFileSync(path.join(root, `locales/${lang}.json`))); for (const k of ['bonusQuota','bonusUnavailable','bonusUsed','bonusRemaining']) assert(d['accounts.'+k]); }
console.log('bonus usage synthetic rendering: PASS');
