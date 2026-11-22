const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const crypto = require('node:crypto');

// Load the declarations without browser event wiring; render the real forms.
function app() {
  const source = fs.readFileSync(`${__dirname}/app.js`, 'utf8');
  const context = vm.createContext({FlowlineGraph:require('./graph.js'), crypto, icon:() => ''});
  vm.runInContext(source.slice(0,source.indexOf("\n$$('[data-icon]')")),context);
  vm.runInContext("pipelines = [makePipeline()]; activeId = pipelines[0].id;",context);
  return code => vm.runInContext(code,context);
}

test('palette has two types and templates create AI nodes with JSON input', () => {
  const run = app();
  assert.equal(run('Object.keys(types).join()'), 'ai,cmd');
  assert.equal(run("makeNode('develop',0,0).type"), 'ai');
  assert.equal(run("makeNode('review',0,0).inputJson"), '{}');
  assert.equal(run("makeNode('cmd',0,0).executor"), '');
  assert.equal(run("makeNode('cmd',0,0).command"), 'input=$(cat)\nprintf \'%s\\n\' "$input"');
});
test('AI and CMD inspectors expose only their relevant execution fields', () => {
  const run = app();
  const cmd = run("renderNodeForm(makeNode('cmd',0,0))");
  assert.match(cmd,/id="node-command"/);
  assert.match(cmd,/id="node-input-json"/);
  assert.match(cmd,/data-output-source=/);
  assert.match(cmd,/stdin/);
  assert.doesNotMatch(cmd,/id="node-executor"|id="node-prompt"/);
  const ai = run("renderNodeForm(makeNode('ai',0,0))");
  assert.match(ai,/id="node-executor"/);
  assert.match(ai,/id="node-prompt"/);
  assert.doesNotMatch(ai,/id="node-command"|data-output-source=/);
});
test('legacy form rendering preserves absence of JSON field; history escapes code and has no editable fields', () => {
  const run = app();
  run("legacy = {id:'old',type:'develop',prompt:'old',outputs:[]}; renderNodeForm(legacy)");
  assert.equal(run("Object.hasOwn(legacy,'inputJson')"),false);
  const history = run("renderResult({id:'cmd',type:'cmd',command:'<script>alert(1)</script>',outputs:[]},{status:'error',exitCode:7,inputJson:{data:{value:'<img>'}},stdout:'result',stderr:'failure'})");
  assert.match(history,/&lt;script&gt;/);
  assert.match(history,/&lt;img&gt;/);
  assert.match(history,/stdout/);
  assert.match(history,/stderr/);
  assert.match(history,/7/);
  assert.doesNotMatch(history,/<input|<textarea|<select|<script>/);
});

test('actual JSON history preserves numeric tokens across browser JSON parsing', () => {
  const run = app();
  const raw = '{"data":{"large":9007199254740993,"exponent":1e400,"html":"<img>"}}';
  const response = `{"inputJson":${raw},"inputJsonText":${JSON.stringify(raw)}}`;
  run(`result = JSON.parse(${JSON.stringify(response)})`);
  const history = run("renderResult({id:'cmd',type:'cmd',outputs:[]},result)");
  assert.match(history,/9007199254740993/);
  assert.match(history,/1e400/);
  assert.match(history,/&lt;img&gt;/);
  assert.doesNotMatch(history,/9007199254740992|<img>/);
});
