const test = require('node:test');
const assert = require('node:assert/strict');
const graph = require('./graph.js');
test('fork and join are acyclic; back edge is rejected', () => {
  const nodes = ['a', 'b', 'c', 'd'].map(id => ({id}));
  const edges = [{from:'a',to:'b'}, {from:'a',to:'c'}, {from:'b',to:'d'}, {from:'c',to:'d'}];
  assert.equal(graph.hasCycle(nodes, edges), false);
  assert.equal(graph.hasCycle(nodes, [...edges,{from:'d',to:'a'}]), true);
});
test('ambiguous names require qualified tokens or stable explicit input aliases', () => {
  const pipeline = {nodes:[{id:'a',name:'A',outputs:[{id:'oa',name:'result'}]},{id:'b',name:'B',outputs:[{id:'ob',name:'result'}]},{id:'c',inputs:{left:{nodeId:'a',outputId:'oa'}}}],edges:[{from:'a',to:'c'},{from:'b',to:'c'}]};
  const tokens = graph.availableTokens(pipeline,pipeline.nodes[2]);
  assert(!tokens.includes('result'));
  assert(tokens.includes('a.result'));
  assert(tokens.includes('b.result'));
  assert(tokens.includes('left'));
  assert.equal(graph.availableSources(pipeline,pipeline.nodes[2])[0].outputId,'oa');
});
test('only direct predecessors expose outputs and context stays reserved', () => {
  const p = {nodes:[{id:'a',outputs:[{id:'oa',name:'value'}]},{id:'b',outputs:[{id:'ob',name:'context'}]},{id:'c'}],edges:[{from:'a',to:'b'},{from:'b',to:'c'}]};
  const tokens = graph.availableTokens(p,p.nodes[2]);
  assert(!tokens.includes('value'));
  assert(tokens.includes('b.context'));
  assert.equal(tokens.filter(t=>t==='context').length,1);
});

test('execution types preserve legacy AI nodes and reject unknown types', () => {
  for (const type of ['ai', '', undefined, 'develop', 'review', 'test', 'publish', 'prompt']) {
    assert.equal(graph.executionType({type}), 'ai');
  }
  assert.equal(graph.executionType({type:'cmd'}), 'cmd');
  assert.equal(graph.executionType({type:'typo'}), '');
});
test('CMD defaults include stable stream sources independently of renamed labels', () => {
  let id = 0;
  const outputs = graph.defaultOutputs('cmd', () => `output_${++id}`);
  assert.deepEqual(outputs.map(output => output.name), ['stdout','stderr','exitCode']);
  outputs[0].name = 'report';
  assert.equal(outputs[0].source, 'stdout');
  assert.equal(new Set(outputs.map(output => output.id)).size, 3);
  assert.deepEqual(graph.defaultOutputs('ai', () => 'ai_out'), [{id:'ai_out',name:'result'}]);
});
test('JSON input accepts objects and reports invalid text and non-objects', () => {
  for (const text of ['{}', '{"value":"{{branch}}","nested":["\\\"\\n$(touch nope)"]}']) {
    assert.equal(graph.jsonInputError(text), '');
  }
  for (const text of ['', '[]', 'null', '"text"', '42', '{', '{} {}', '{"x":}']) {
    assert.notEqual(graph.jsonInputError(text), '', text);
  }
});
test('JSON reference insertion quotes values but preserves existing string quotes', () => {
  const outside = '{"value":}';
  const position = outside.indexOf('}');
  const result = outside.slice(0,position) + graph.jsonReference(outside,position,'node.stdout') + outside.slice(position);
  assert.equal(JSON.parse(result).value, '{{node.stdout}}');
  const inside = '{"value":"prefix \\"quoted\\" "}';
  assert.equal(graph.jsonReference(inside, inside.length-2, 'branch'), '{{branch}}');
});
