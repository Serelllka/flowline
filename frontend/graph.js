'use strict';
(function (root) {
  const builtins = ['context', 'repository', 'branch', 'commit', 'pull_request'];
  function hasCycle(nodes, edges) {
    const waiting = new Set(nodes.map(node => node.id));
    const done = new Set();
    while (waiting.size) {
      const ready = [...waiting].filter(id => edges.filter(edge => edge.to === id).every(edge => done.has(edge.from)));
      if (!ready.length) return true;
      ready.forEach(id => { waiting.delete(id); done.add(id); });
    }
    return false;
  }
  function availableSources(pipeline, node) {
    return pipeline.edges.filter(edge => edge.to === node.id).flatMap(edge => {
      const source = pipeline.nodes.find(item => item.id === edge.from);
      return (source?.outputs || []).map(output => ({nodeId: source.id, outputId: output.id, name: output.name, nodeName: source.name}));
    });
  }
  function availableTokens(pipeline, node) {
    const sources = availableSources(pipeline, node);
    const tokens = [...builtins];
    for (const source of sources) {
      if (sources.filter(item => item.name === source.name).length === 1 && !builtins.includes(source.name)) tokens.push(source.name);
      tokens.push(`${source.nodeId}.${source.name}`);
    }
    return [...new Set([...tokens, ...Object.keys(node.inputs || {})])];
  }
  const graph = {builtins, hasCycle, availableSources, availableTokens};
  if (typeof module !== 'undefined' && module.exports) module.exports = graph;
  else root.FlowlineGraph = graph;
})(globalThis);
