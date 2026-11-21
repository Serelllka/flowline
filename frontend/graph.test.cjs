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
