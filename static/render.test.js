const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');

const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
const script = html.match(/<script>([\s\S]*?)<\/script>/)[1];
const esc = script.match(/^const esc = .*;$/m)[0];
const uiStart = script.indexOf('function displayCaption(');
const uiEnd = script.indexOf('\nfunction renderApprovalButtons(', uiStart);
const uiSource = script.slice(uiStart, uiEnd);
const renderers = new Function(`${esc}\nlet components = {}; let dataModel = {};\n${uiSource}\nreturn {displayCaption, renderComponent, renderMarkdown, components, dataModel};`)();

test('maps protocol captions to product labels', () => {
  assert.equal(renderers.displayCaption('You'), '你');
  assert.equal(renderers.displayCaption('Agent'), 'VSJin AI');
  assert.equal(renderers.displayCaption('tool call'), '调用工具');
  assert.equal(renderers.displayCaption('tool result'), '工具结果');
  Object.assign(renderers.components, {
    card: {Card:{children:['column']}},
    column: {Column:{children:['role']}},
    role: {Text:{value:'You', usageHint:'caption'}},
  });
  const card = renderers.renderComponent('card');
  assert.match(card, /data-role="You"/);
  assert.match(card, />你<\/div>/);
});

test('renders simple Markdown tables and inline formatting safely', () => {
  const output = renderers.renderMarkdown([
    '| 项目 | 说明 |',
    '| --- | --- |',
    '| 资料 | **已整理** |',
    '| <img src=x onerror=alert(1)> | [外链](https://example.com/?a=1&b=2) |',
  ].join('\n'));

  assert.match(output, /<table>/);
  assert.match(output, /<th>项目<\/th>/);
  assert.match(output, /<td><strong>已整理<\/strong><\/td>/);
  assert.match(output, /&lt;img src=x onerror=alert\(1\)&gt;/);
  assert.doesNotMatch(output, /<img\b/i);
  assert.match(output, /href="https:\/\/example\.com\/\?a=1&amp;b=2"/);
});

test('keeps escaped pipes inside a Markdown table cell', () => {
  const output = renderers.renderMarkdown('| 名称 | 备注 |\n| --- | --- |\n| A\\|B | 正常 |');
  assert.match(output, /<td>A\|B<\/td>/);
  assert.match(output, /<td>正常<\/td>/);
});

test('does not create unsafe links from Markdown table cells', () => {
  const output = renderers.renderMarkdown('| 链接 |\n| --- |\n| [恶意](javascript:alert\(1\)) |');
  assert.doesNotMatch(output, /<a\b/);
  assert.doesNotMatch(output, /<script\b|onerror=/i);
});
