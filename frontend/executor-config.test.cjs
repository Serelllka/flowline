const test = require('node:test');
const assert = require('node:assert/strict');
const config = require('./executor-config.js');
test('executor payload preserves stable IDs and includes only nonsecret contract fields', () => {
  const result = config.payload([{id:'stable',name:' DeepSeek ',kind:'openai-compatible',model:'deepseek-chat',baseUrl:'https://api.deepseek.com',apiKeyEnv:'DEEPSEEK_API_KEY',apiKey:'must-not-send',token:'must-not-send',command:'unused'}]);
  assert.deepEqual(result,[{id:'stable',name:'DeepSeek',kind:'openai-compatible',model:'deepseek-chat',baseUrl:'https://api.deepseek.com',apiKeyEnv:'DEEPSEEK_API_KEY',command:''}]);
  assert.deepEqual(config.validate(result),[]);
});
test('API configuration rejects embedded credentials and secret instead of env name', () => {
  const errors = config.validate([{id:'api',name:'API',kind:'openai-compatible',model:'m',baseUrl:'https://user:password@example.com/v1?api_key=secret',apiKeyEnv:'sk-secret-value'}]);
  assert.equal(errors.length,2);
});
test('CLI profiles preserve model and command but strip unrelated API settings', () => {
  const result = config.payload([{id:'cli',name:'Codex',kind:'codex',model:'chosen-model',command:'/path with spaces/codex',baseUrl:'https://unused',apiKeyEnv:'UNUSED'}]);
  assert.equal(result[0].command,'/path with spaces/codex');
  assert.equal(result[0].apiKeyEnv,'');
  assert.equal(result[0].baseUrl,'');
  assert.deepEqual(config.validate(result),[]);
});
test('missing model, unknown executor kinds and duplicate IDs fail visibly', () => {
  const errors = config.validate([{id:'same',name:'One',kind:'unknown',command:'x'},{id:'same',name:'Two',kind:'openai-compatible',model:'',baseUrl:'https://example.com',apiKeyEnv:'KEY'}]);
  assert(errors.some(error=>error.includes('неизвестный тип')));
  assert(errors.some(error=>error.includes('уникальный ID')));
  assert(errors.some(error=>error.includes('модель API')));
});
test('historical executor label remains from snapshot after profile rename or deletion', () => {
  const snapshot = {executorConfigs:[{id:'codex',name:'Original',model:'original-model'}]};
  const current = [{id:'codex',name:'Renamed',model:'new-model'}];
  assert.equal(config.profile('codex',current,snapshot).name,'Original');
  assert.equal(config.profile('codex',[],snapshot).model,'original-model');
  assert.equal(config.profile('codex',current,null).name,'Renamed');
  assert.equal(config.profile('codex',current,{executorConfigs:[]}),undefined);
});
