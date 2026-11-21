const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const crypto = require('node:crypto');

const decode = text => text.replace(/&(amp|lt|gt|quot|#39);/g, (_, entity) => ({amp:'&',lt:'<',gt:'>',quot:'"','#39':"'"}[entity]));

// Deliberately bounded DOM fixture: parse actual rendered controls, reject unknown
// selectors, and dispatch property handlers. This does not model browser layout.
function inspector(type) {
  let elements = [];
  let focused;
  const matches = (element, selector) => {
    if (selector.startsWith('#')) return element.id === selector.slice(1);
    if (/^\[data-[\w-]+\]$/.test(selector)) return Object.hasOwn(element.attributes, selector.slice(1,-1));
    if (selector === '.panel-header h2') return element.tagName === 'h2';
    if (/^[a-z]+$/.test(selector)) return element.tagName === selector;
    if (selector === 'button:not(#close-inspector)') return element.tagName === 'button' && element.id !== 'close-inspector';
    throw new Error(`Unsupported fixture selector: ${selector}`);
  };
  const panel = {
    hidden:true,
    querySelectorAll(selector) { return elements.filter(element => selector.split(',').some(part => matches(element,part))); },
    querySelector(selector) { return this.querySelectorAll(selector)[0] || null; },
    set innerHTML(html) {
      elements = [...html.matchAll(/<([a-z][\w-]*)\b([^>]*)>/g)].map(match => {
        const attributes = Object.fromEntries([...match[2].matchAll(/([\w-]+)(?:="([^"]*)")?/g)].map(([,name,value]) => [name,decode(value || '')]));
        const content = html.slice(match.index + match[0].length).split(`</${match[1]}>`)[0];
        return {
          tagName:match[1], attributes, id:attributes.id,
          dataset:Object.fromEntries(Object.entries(attributes).filter(([name]) => name.startsWith('data-')).map(([name,value]) => [name.slice(5).replace(/-([a-z])/g,(_,letter) => letter.toUpperCase()),value])),
          value:match[1] === 'textarea' ? decode(content) : attributes.value || '',
          hidden:Object.hasOwn(attributes,'hidden'), disabled:Object.hasOwn(attributes,'disabled'),
          selectionStart:0, selectionEnd:0,
          setAttribute(name,value) { this.attributes[name] = value; },
          setRangeText(text,start,end) { this.value = this.value.slice(0,start) + text + this.value.slice(end); this.selectionStart = this.selectionEnd = start + text.length; },
          dispatchEvent(event) { this[`on${event.type}`]?.({target:this}); },
          focus() { focused = this; }
        };
      });
    }
  };
  const document = {
    querySelector:selector => selector === '#inspector' ? panel : panel.querySelector(selector),
    querySelectorAll:selector => panel.querySelectorAll(selector),
    getElementById:id => panel.querySelector(`#${id}`)
  };
  const source = fs.readFileSync(`${__dirname}/app.js`,'utf8');
  const context = vm.createContext({FlowlineGraph:require('./graph.js'),crypto,icon:() => '',document,Event});
  const run = code => vm.runInContext(code,context);
  run(source.slice(0,source.indexOf("\n$$('[data-icon]')")));
  // Keep the real inspector, form, dirty-state and graph logic; isolate unrelated
  // canvas/header rendering, which needs the rest of the application document.
  run(`renderHeader = () => {}; renderNodes = () => {};
    pipelines = [makePipeline()]; activeId = pipelines[0].id; ready = true;
    executors = [{id:'first',name:'First',available:true},{id:'second',name:'Second',available:true}];
    draft().nodes.push(makeNode(${JSON.stringify(type)},0,0)); selected = draft().nodes[0].id;
    renderInspector();`);
  return {run,panel,get:selector => document.querySelector(selector),focused:() => focused};
}

for (const type of ['ai','cmd']) {
  test(`${type} inspector wires edits only to rendered execution controls`, () => {
    const {run,get} = inspector(type);
    const edit = (selector,value,event = 'input') => {
      const field = get(selector);
      assert.ok(field,`${selector} exists`);
      assert.equal(typeof field[`on${event}`],'function',`${selector} has a handler`);
      field.value = value; field.dispatchEvent(new Event(event));
    };
    edit('#node-name','Edited');
    assert.equal(run('nodeById(selected).name'),'Edited');
    if (type === 'ai') {
      assert.equal(get('#node-command'),null);
      edit('#node-prompt','Use the supplied input');
      edit('#node-executor','second','change');
      assert.equal(run('nodeById(selected).prompt'),'Use the supplied input');
      assert.equal(run('nodeById(selected).executor'),'second');
    } else {
      assert.equal(get('#node-prompt'),null);
      assert.equal(get('#node-executor'),null);
      edit('#node-command','printf "%s" "{{literal}}"');
      assert.equal(run('nodeById(selected).command'),'printf "%s" "{{literal}}"');
    }
    edit('#node-input-json','{');
    assert.equal(run('nodeById(selected).inputJson'),'{');
    assert.equal(get('#node-input-json').attributes['aria-invalid'],'true');
    assert.equal(get('#json-error').hidden,false);
    edit('#node-input-json','{"value":1}');
    assert.equal(get('#node-input-json').attributes['aria-invalid'],'false');
    assert.equal(get('#json-error').hidden,true);
    assert.equal(run('dirty.has(activeId)'),true);
  });

  test(`${type} JSON token insertion dispatches input and updates the draft`, () => {
    const {run,get,panel,focused} = inspector(type);
    const field = get('#node-input-json');
    field.value = '{"branch":}'; field.selectionStart = field.selectionEnd = 10;
    const button = panel.querySelectorAll('[data-token]').find(element => element.dataset.target === 'node-input-json' && element.dataset.token === 'branch');
    assert.ok(button);
    button.dispatchEvent(new Event('click'));
    assert.equal(field.value,'{"branch":"{{branch}}"}');
    assert.equal(run('nodeById(selected).inputJson'),field.value);
    assert.equal(field.attributes['aria-invalid'],'false');
    assert.equal(focused(),field);
  });

  test(`${type} history renders without editable fields or mutation bindings`, () => {
    const {run,get,panel} = inspector(type);
    run('snapshotRun = {snapshot:clone(draft()),nodes:{}}; renderInspector();');
    assert.equal(panel.querySelectorAll('input,textarea,select').length,0);
    assert.equal(panel.querySelectorAll('button').length,1);
    assert.equal(get('#delete-node'),null);
    assert.equal(run('dirty.size'),0);
    get('#close-inspector').dispatchEvent(new Event('click'));
    assert.equal(panel.hidden,true);
    assert.equal(run('selected'),null);
    assert.equal(run('dirty.size'),0);
  });
}

test('AI prompt tokens dispatch input to the prompt only', () => {
  const {run,get,panel} = inspector('ai');
  const field = get('#node-prompt');
  field.value = 'Use '; field.selectionStart = field.selectionEnd = 4;
  const button = panel.querySelectorAll('[data-token]').find(element => element.dataset.target === 'node-prompt' && element.dataset.token === 'branch');
  button.dispatchEvent(new Event('click'));
  assert.equal(run('nodeById(selected).prompt'),'Use {{branch}}');
  assert.equal(run('nodeById(selected).inputJson'),'{}');
});

test('CMD stream selections update their matching stable output IDs', () => {
  const {run,panel} = inspector('cmd');
  const fields = panel.querySelectorAll('[data-output-source]');
  assert.equal(fields.length,3);
  for (const [index,field] of fields.entries()) {
    field.value = ['stderr','exitCode','stdout'][index];
    field.dispatchEvent(new Event('change'));
    assert.equal(run(`nodeById(selected).outputs.find(output => output.id === ${JSON.stringify(field.dataset.outputSource)}).source`),field.value);
  }
  assert.equal(run('dirty.has(activeId)'),true);
});
