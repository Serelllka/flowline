'use strict';
(function(root) {
  const kinds = ['codex','claude','openai-compatible'];
  function payload(configs) {
    return configs.map(config => {
      const isAPI = config.kind === 'openai-compatible';
      return {
        id:String(config.id || '').trim(),name:String(config.name || '').trim(),kind:config.kind,
        command:isAPI ? '' : String(config.command || '').trim(),model:String(config.model || '').trim(),
        baseUrl:isAPI ? String(config.baseUrl || '').trim() : '',apiKeyEnv:isAPI ? String(config.apiKeyEnv || '').trim() : ''
      };
    });
  }
  function validate(configs) {
    const errors = [], ids = new Set();
    for (const [index,config] of payload(configs).entries()) {
      const label = config.name || `Исполнитель ${index+1}`;
      if (!config.id || ids.has(config.id)) errors.push(`${label}: нужен уникальный ID.`);
      ids.add(config.id);
      if (!config.name) errors.push(`${label}: укажите название.`);
      if (!kinds.includes(config.kind)) errors.push(`${label}: неизвестный тип исполнителя.`);
      if (config.kind === 'openai-compatible') {
        if (!config.model) errors.push(`${label}: укажите модель API.`);
        if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(config.apiKeyEnv)) errors.push(`${label}: укажите имя переменной окружения, например DEEPSEEK_API_KEY, а не сам ключ.`);
        try {
          const url = new URL(config.baseUrl);
          if (!['http:','https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) throw new Error();
        } catch { errors.push(`${label}: нужен HTTP(S) Base URL без логина, пароля и параметров.`); }
      } else if (!config.command) errors.push(`${label}: укажите имя команды или путь к исполняемому файлу.`);
    }
    return errors;
  }
  function profile(id, current, snapshot) {
    const profiles = snapshot ? (snapshot.executorConfigs || []) : current;
    return profiles.find(item => item.id === id);
  }
  const helpers = {kinds,payload,validate,profile};
  if (typeof module !== 'undefined' && module.exports) module.exports = helpers;
  else root.FlowlineExecutors = helpers;
})(globalThis);
