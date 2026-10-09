const assert = require('node:assert/strict');
const { test } = require('node:test');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync(require('node:path').join(__dirname, '../internal/server/assets/app.js'), 'utf8');
function harness(extra = {}) {
  const context = {
    t: s => s,
    esc: v => String(v ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])),
    ...extra,
  };
  vm.createContext(context);
  vm.runInContext(source.slice(source.indexOf('function previewValue('), source.indexOf('function renderDetail(')) + '\nglobalThis.pretty = pretty;', context);
  return context;
}

test('expanding a large browser screenshot does not insert or serialize its image payload', () => {
  const h = harness();
  const part = {type:'image', mimeType:'image/png', data:'A'.repeat(32 * 1024 * 1024)};
  const result = {content:[{type:'text', text:'Screenshot captured'}, part]};
  const html = h.outputView({category:'browser', result}) + h.pretty(result);
  assert.ok(html.length < 2000);
  assert.ok(html.includes('data-browser-image="1"'));
  assert.ok(!html.includes('<img'));
  assert.ok(!html.includes('AAAA'));
  assert.equal(result.content[1].data.length, 32 * 1024 * 1024, 'copy retains the original payload');
});

test('large text and nested responses produce bounded, escaped previews', () => {
  const h = harness();
  const huge = '<script>'.repeat(1024 * 1024);
  const json = h.pretty({items:Array(10000).fill(huge)});
  assert.ok(json.length < 100000);
  assert.ok(!json.includes('<script>'));
  const text = h.outputView({category:'browser',result:{content:[{type:'text',text:huge}]}});
  assert.ok(text.length < 12000);
  let nested = {};
  for (let i = 0; i < 100; i++) nested = {nested};
  assert.ok(h.pretty(nested).length < 3000);
  assert.equal(h.pretty({ok:true,count:2}), '{\n  &quot;ok&quot;: true,\n  &quot;count&quot;: 2\n}');
});

test('image decoding is deferred until requested and object URLs are released', () => {
  const released = [], images = [];
  const h = harness({
    state:{detail:{call:{result:{content:[{type:'image',mimeType:'image/png',data:'aGVsbG8='}]}}}},
    atob: data => Buffer.from(data, 'base64').toString('binary'), Blob,
    URL:{createObjectURL: blob => {assert.equal(blob.size, 5);return 'blob:test'},revokeObjectURL:url=>released.push(url)},
    document:{createElement:()=>({})},
  });
  h.showBrowserImage({dataset:{browserImage:'0'},replaceWith:img=>images.push(img)});
  assert.equal(images[0].src, 'blob:test');
  assert.equal(images[0].decoding, 'async');
  h.clearDetailImages();h.clearDetailImages();
  assert.deepEqual(released, ['blob:test']);
});

test('completed call details are reused while running calls keep refreshing', async () => {
  let requests = 0, renders = 0;
  const h = harness({
    state:{selected:'one',detailVersion:0,detail:{call:{id:'one',status:'success'}}},
    api:async()=>{requests++;return {call:{id:'one',status:'running'}}},
    renderCalls:()=>{},renderDetail:()=>renders++,toast:()=>{},
  });
  vm.runInContext(source.slice(source.indexOf('async function loadDetail('), source.indexOf('// Bound previews')), h);
  await h.loadDetail();await h.loadDetail();
  assert.equal(requests, 0);assert.equal(renders, 2);
  h.state.detail.call.status='running';
  await h.loadDetail();
  assert.equal(requests, 1);
});

// The Safari card and the rule for which browser a tool waits for.
function safariHarness(data) {
  const element = () => ({ textContent: '', classList: { toggle(name, on) { this.ready = on; } } });
  const els = { status: [element()], message: [element()], diagnostic: [element()], indicator: [element()] };
  const selectors = { '[data-safari-status]': els.status, '[data-safari-message]': els.message, '[data-safari-diagnostic]': els.diagnostic, '[data-safari-indicator]': els.indicator };
  const context = {
    t: (s, v) => v ? s.replace(/\{(\d+)\}/g, (_, n) => v[n]) : s,
    state: { data },
    document: { querySelectorAll: selector => selectors[selector] || [] },
  };
  vm.createContext(context);
  vm.runInContext(source.slice(source.indexOf('function browserPending('), source.indexOf('function renderChrome(')) + '\nglobalThis.browserPending = browserPending; globalThis.renderSafari = renderSafari;', context);
  return { context, els };
}

test('a browser tool waits for its own browser, not for the other one', () => {
  const { context } = safariHarness({});
  const chromeTool = { name: 'chrome_take_snapshot', category: 'browser' };
  const safariTool = { name: 'safari_create_tab', category: 'safari' };
  const idle = { chrome: { state: 'waiting', message: 'Waiting for Chrome' }, safari: { state: 'ready' } };
  assert.equal(context.browserPending(chromeTool, idle), 'Waiting for Chrome');
  assert.equal(context.browserPending(safariTool, idle), '', 'Safari tools run while Chrome is down');
  const other = { chrome: { state: 'ready' }, safari: { state: 'unavailable', message: 'No Safari MCP' } };
  assert.equal(context.browserPending(chromeTool, other), '');
  assert.equal(context.browserPending(safariTool, other), 'No Safari MCP');
  // Safari still takes a call while it waits for remote automation: the answer says how to turn it on.
  assert.equal(context.browserPending(safariTool, { safari: { state: 'permission_required' } }), '');
  assert.equal(context.browserPending(chromeTool, { chrome: { state: 'permission_required' } }) !== '', true, 'Chrome keeps its stricter rule');
  assert.equal(context.browserPending({ name: 'read_file', category: 'files' }, {}), '');
  assert.equal(context.browserPending(safariTool, {}), 'Safari MCP 尚未启动', 'a missing status counts as not started');
});

test('the Safari card shows each state, and pause or a closed browser switch win over it', () => {
  const show = (safari, extra = {}) => {
    const h = safariHarness({ enabled: { safari: true }, paused: false, safari, ...extra });
    h.context.renderSafari();
    return { status: h.els.status[0].textContent, message: h.els.message[0].textContent, diagnostic: h.els.diagnostic[0].textContent, ready: h.els.indicator[0].classList.ready };
  };
  const ready = show({ state: 'ready', tools: 17, message: 'ok' });
  assert.equal(ready.status, '已接入');
  assert.ok(ready.message.includes('17'), 'the tool count is shown');
  assert.equal(ready.ready, true);
  const permission = show({ state: 'permission_required', tools: 17, message: 'Turn it on' });
  assert.equal(permission.status, '需要授权');
  assert.equal(permission.diagnostic, 'Turn it on');
  assert.equal(permission.ready, false);
  assert.equal(show({ state: 'unavailable', message: 'needs Safari 27' }).status, '需要设置');
  assert.equal(show({ state: 'error', message: 'x' }).status, '连接失败');
  assert.equal(show({ state: 'ready', tools: 17 }, { paused: true }).status, '已暂停');
  const off = show({ state: 'ready', tools: 17 }, { enabled: { safari: false } });
  assert.equal(off.status, '已关闭');
  assert.equal(off.ready, false);
  // A server that does not report Safari yet is shown as waiting, not as a crash.
  const none = safariHarness({ enabled: { safari: true }, paused: false });
  none.context.renderSafari();
  assert.equal(none.els.status[0].textContent, '等待检测');
});
