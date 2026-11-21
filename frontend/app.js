'use strict';
const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];
const escapeHTML = value => String(value ?? '').replace(/[&<>"']/g, char => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[char]));
const clone = value => JSON.parse(JSON.stringify(value));
const uid = prefix => `${prefix}_${crypto.randomUUID().replaceAll('-', '')}`;
const activeStatus = status => ['waiting', 'running'].includes(status);
const statusNames = {waiting:'Ожидание',running:'Выполняется',success:'Успешно',error:'Ошибка',cancelled:'Отменён',skipped:'Пропущен',interrupted:'Прерван'};
const types = {
  develop:{name:'Разработка',icon:'code',color:'#b78b5d',bg:'#fcf5ec',border:'#f0e3d0',summary:'Реализация задачи и подготовка изменений',output:'changes',prompt:'Изучи {{context}}. Реализуй задачу, следуя правилам проекта. Верни описание решения и изменённых файлов.'},
  review:{name:'Ревью',icon:'review',color:'#9580b6',bg:'#f6f1fb',border:'#e8def2',summary:'Проверка кода, архитектуры и качества',output:'review',prompt:'Проверь код и {{context}}. Оцени корректность и архитектуру. Верни замечания с приоритетами и итоговый вердикт.'},
  test:{name:'Тестирование',icon:'test',color:'#7399b4',bg:'#eef6fa',border:'#d9e9f1',summary:'Проверка сценариев и запуск тестов',output:'tests',prompt:'Изучи {{context}}. Запусти подходящие тесты. Верни команды, результаты и обнаруженные проблемы.'},
  publish:{name:'Публикация',icon:'publish',color:'#6d9b7c',bg:'#f0f7f2',border:'#dfece2',summary:'Локальное описание PR и релизные заметки',output:'release',prompt:'На основе {{context}} подготовь локальный пакет: описание PR, релизные заметки и отчёт о фактически выполненных проверках. Верни материалы текстом. Не выполняй push, создание или изменение PR, merge, выпуск релиза или другие удалённые изменения.'},
  prompt:{name:'Свой промпт',icon:'spark',color:'#7a8f87',bg:'#f2f6f4',border:'#e0e8e3',summary:'Свой этап с инструкциями для исполнителя',output:'result',prompt:'Изучи {{context}} и опиши задачу этого этапа.'}
};
let repositories = [], pipelines = [], runs = [], executors = [];
let publication = {enabled:false,detail:'Действия этапа «Публикация» ещё не настроены.'};
let activeId = '', selected = null, snapshotRun = null, view = 'editor';
let dirty = new Set(), revisions = new Map(), saved = new Set();
let contextData = {branches:[],pullRequests:[],prError:''}, contextLoading = false, contextVersion = 0;
let contextSelection = {repositoryId:'',branch:'',pr:''}, drawerVisible = false;
let busy = false, ready = false, pan = {x:0,y:0}, zoom = 1, pendingConnection = null, drag = null;
let toastTimer, lastFocus, pollTimer, polling = false, suppressClick = false, historyError = '';
let settingsSession = null;
const draft = () => pipelines.find(item => item.id === activeId);
const graph = () => snapshotRun?.snapshot || draft();
const nodeById = id => graph()?.nodes.find(node => node.id === id);
const readonly = () => !!snapshotRun;
const editable = () => ready && !readonly() && !busy;
const typeOf = node => types[node.type] || types.prompt;
const nodeStyle = type => `--node-color:${type.color};--node-bg:${type.bg};--node-border:${type.border}`;
const statusIcon = status => status === 'success' ? 'check' : ['cancelled','error','interrupted','skipped'].includes(status) ? 'stop' : 'clock';
const statusClass = status => Object.hasOwn(statusNames,status) ? status : 'waiting';
const statusChip = status => `<span class="status-chip ${statusClass(status)}">${icon(statusIcon(status))}${escapeHTML(statusNames[status] || status)}</span>`;
const formatTime = value => value ? new Date(value).toLocaleString('ru-RU', {day:'2-digit',month:'short',hour:'2-digit',minute:'2-digit',second:'2-digit'}) : '—';
const pretty = value => typeof value === 'string' ? value : JSON.stringify(value, null, 2);

async function api(path, options = {}) {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 30000);
  try {
    const response = await fetch(`/api${path}`, {...options, signal:controller.signal, headers:{Accept:'application/json', ...(options.body ? {'Content-Type':'application/json'} : {}), ...(options.method && options.method !== 'GET' ? {'X-Flowline-Request':'1'} : {}), ...options.headers}});
    const data = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error([data.error || `Ошибка HTTP ${response.status}`, ...(data.details || [])].join('\n'));
    return data;
  } catch (error) {
    if (error.name === 'AbortError') throw new Error('Сервер не ответил за 30 секунд. Проверьте соединение и повторите действие.');
    if (error instanceof TypeError) throw new Error('Нет соединения с локальным сервером. Проверьте, что Flowline запущен.');
    throw error;
  } finally { clearTimeout(timeout); }
}
function showError(error) {
  $('#app-error').textContent = error.message || String(error);
  $('#app-error').hidden = false;
}
function clearError() { $('#app-error').hidden = true; }
function toast(message) {
  clearTimeout(toastTimer);
  $('#toast').textContent = message;
  $('#toast').hidden = false;
  toastTimer = setTimeout(() => $('#toast').hidden = true, 4500);
}
function action(handler) { return async event => { try { clearError(); await handler(event); } catch (error) { showError(error); } }; }
function markDirty() {
  if (readonly()) return;
  dirty.add(activeId);
  revisions.set(activeId, (revisions.get(activeId) || 0) + 1);
  renderHeader();
}
function setBusy(value) {
  busy = value;
  renderHeader();
  $$('#inspector input,#inspector textarea,#inspector select,#inspector button:not(#close-inspector)').forEach(element => element.disabled = value || readonly());
  $('#repo-select').disabled = value || readonly() || !repositories.length;
  $('#branch-select').disabled = value || readonly() || contextLoading || !contextData.branches.length;
  $('#pr-select').disabled = value || readonly() || contextLoading || !contextData.pullRequests.length;
  $('#add-repository').disabled = value || readonly();
  $('#empty-repository').disabled = value || readonly() || !ready;
  $('#refresh-context').disabled = value || readonly() || contextLoading;
}
function makePipeline(name = 'Новый пайплайн') {
  return {id:uid('pipeline'),name,description:'',nodes:[],edges:[],viewport:{x:0,y:0,zoom:1}};
}
function makeNode(type, x, y) {
  const item = types[type];
  return {id:uid('node'),type,name:item.name,executor:executors.find(executor => executor.available)?.id || executors[0]?.id || '',prompt:item.prompt,x,y,inputs:{},outputs:[{id:uid('output'),name:item.output}]};
}
function renderSidebar() {
  $('#pipeline-count').textContent = pipelines.length;
  $('#pipeline-list').innerHTML = pipelines.map(item => `<button class="pipeline-item ${!readonly() && item.id === activeId ? 'active' : ''}" data-pipeline="${escapeHTML(item.id)}" title="${escapeHTML(item.name)}"><span class="pipeline-mark"></span><span>${escapeHTML(item.name)}${dirty.has(item.id) ? ' ·' : ''}</span></button>`).join('');
}
function renderHeader() {
  const pipeline = graph();
  if (!pipeline) return;
  $('#pipeline-title').textContent = pipeline.name;
  $('#breadcrumb-name').textContent = pipeline.name;
  const description = pipeline.description === 'Ваш сценарий работы с кодом.' ? '' : pipeline.description || '';
  $('#pipeline-description').textContent = readonly() ? `Снимок запуска · ${formatTime(snapshotRun.createdAt)}` : description;
  $('#pipeline-description').hidden = !$('#pipeline-description').textContent;
  $('#save-state').textContent = readonly() ? 'Неизменяемый снимок' : dirty.has(activeId) ? 'Есть изменения' : saved.has(activeId) ? 'Сохранено' : 'Не сохранён';
  $('#node-count').textContent = `${pipeline.nodes.length} блоков`;
  $('#edge-count').textContent = `${pipeline.edges.length} связей`;
  $('#run-count').textContent = runs.length;
  $('#rename-pipeline').hidden = readonly();
  $('#save-button').disabled = !editable();
  $('#add-block').disabled = !editable();
  $('#empty-add').disabled = !editable();
  $('#new-pipeline').disabled = !ready || busy;
  $('#run-button').disabled = !ready || busy || (readonly() ? !activeStatus(snapshotRun.status) : contextLoading || !$('#repo-select').value || !$('#branch-select').value || !pipeline.nodes.length || !!contextMismatch());
  $('#run-button').innerHTML = readonly() ? `${icon('stop')}Остановить` : `${icon('play')}Запустить`;
  $('#run-button').hidden = readonly() && !activeStatus(snapshotRun.status);
  $('#snapshot-banner').hidden = !readonly();
  $('#snapshot-label').textContent = readonly() ? `Снимок запуска ${snapshotRun.id} · ${statusNames[snapshotRun.status] || snapshotRun.status}` : '';
  $('#canvas-hint').textContent = pendingConnection ? 'Выберите вход другого блока · Esc — отмена' : '';
  $('#canvas-hint').hidden = !pendingConnection;
  $('#canvas-hint').classList.toggle('connecting', !!pendingConnection);
  $('#editor-view').classList.toggle('snapshot-mode', readonly());
}
function contextMismatch() {
  const pr = contextData.pullRequests.find(item => String(item.number) === $('#pr-select').value);
  if (!pr) return '';
  const branch = contextData.branches.find(item => item.name === $('#branch-select').value);
  if (pr.head !== branch?.name) return `PR #${pr.number}: исходная ветка ${pr.head}, а выбрана ${branch?.name || 'другая ветка'}. Выберите соответствующую ветку.`;
  if (pr.headSha && branch.sha && pr.headSha !== branch.sha) return 'Локальная ветка и исходная ветка PR указывают на разные commit SHA. Синхронизируйте репозиторий и обновите контекст.';
  return '';
}
function showContextMessage(message, error = false) {
  $('#context-message').hidden = !message;
  $('#context-message').textContent = message;
  $('#context-message').classList.toggle('error', error);
}
function updateContextMessage() {
  const mismatch = contextMismatch();
  const pr = contextData.pullRequests.find(item => String(item.number) === $('#pr-select').value);
  if (mismatch) showContextMessage(mismatch, true);
  else if (contextData.prError) showContextMessage(contextData.prError, true);
  else if (pr) showContextMessage(`PR #${pr.number}: ${pr.head} → ${pr.base} · ${pr.title}`);
  else if (!repositories.length) showContextMessage('Добавьте локальный Git-репозиторий, чтобы выбрать ветку и начать запуск.');
  else if (!contextData.branches.length) showContextMessage('В репозитории нет доступных веток с коммитами.');
  else showContextMessage(contextData.pullRequests.length ? 'PR не выбран. Запуск будет использовать локальную ветку.' : 'Открытых PR нет. Можно запустить пайплайн на локальной ветке.');
  renderHeader();
}
function renderRepositorySelect(selectedId = $('#repo-select').value) {
  const empty = !repositories.length && !readonly();
  $('#repo-select').hidden = empty;
  $('#empty-repository').hidden = !empty;
  $('#empty-repository').disabled = !editable();
  $('#repository-label').htmlFor = empty ? 'empty-repository' : 'repo-select';
  $('#repo-select').innerHTML = repositories.length ? repositories.map(repo => `<option value="${escapeHTML(repo.id)}">${escapeHTML(repo.name)}</option>`).join('') : '<option value="">Нет репозиториев</option>';
  if (repositories.some(repo => repo.id === selectedId)) $('#repo-select').value = selectedId;
  $('#repo-select').disabled = readonly() || contextLoading || !repositories.length;
}
async function loadContext() {
  if (readonly()) return;
  const version = ++contextVersion;
  const repoId = $('#repo-select').value;
  const previousBranch = contextSelection.branch;
  const previousPR = contextSelection.pr;
  contextSelection.repositoryId = repoId;
  contextLoading = true;
  contextData = {branches:[],pullRequests:[],prError:''};
  $('#branch-select').innerHTML = '<option value="">Загрузка веток…</option>';
  $('#pr-select').innerHTML = '<option value="">Загрузка PR…</option>';
  $('#branch-select').disabled = $('#pr-select').disabled = true;
  $('#refresh-context').disabled = true;
  renderHeader();
  try {
    const data = repoId ? await api(`/repositories/${encodeURIComponent(repoId)}/context`) : {branches:[],pullRequests:[]};
    if (version !== contextVersion || readonly()) return;
    contextData = {branches:data.branches || [],pullRequests:data.pullRequests || [],prError:data.prError || ''};
    $('#branch-select').innerHTML = contextData.branches.length ? contextData.branches.map(branch => `<option value="${escapeHTML(branch.name)}">${escapeHTML(branch.name)}</option>`).join('') : '<option value="">Нет веток</option>';
    if (contextData.branches.some(branch => branch.name === previousBranch)) $('#branch-select').value = previousBranch;
    $('#pr-select').innerHTML = `<option value="">Без PR · локальная ветка</option>${contextData.pullRequests.map(pr => `<option value="${pr.number}">#${pr.number} · ${escapeHTML(pr.title)} (${escapeHTML(pr.head)} → ${escapeHTML(pr.base)})</option>`).join('')}`;
    if (contextData.pullRequests.some(pr => String(pr.number) === previousPR)) $('#pr-select').value = previousPR;
    $('#branch-select').disabled = !contextData.branches.length;
    $('#pr-select').disabled = !contextData.pullRequests.length;
    contextSelection = {repositoryId:repoId,branch:$('#branch-select').value,pr:$('#pr-select').value};
  } catch (error) {
    if (version !== contextVersion) return;
    $('#branch-select').innerHTML = '<option value="">Не удалось загрузить</option>';
    $('#pr-select').innerHTML = '<option value="">Нет доступа к PR</option>';
    contextData.prError = error.message;
  } finally {
    if (version === contextVersion) {
      contextLoading = false;
      $('#refresh-context').disabled = readonly();
      updateContextMessage();
    }
  }
}
function renderNodes() {
  const pipeline = graph();
  if (!pipeline) return;
  $('#nodes').innerHTML = pipeline.nodes.map(node => {
    const type = typeOf(node), result = snapshotRun?.nodes?.[node.id], status = result?.status;
    const incoming = pipeline.edges.filter(edge => edge.to === node.id);
    const executor = FlowlineExecutors.profile(node.executor, executors, snapshotRun);
    return `<article class="node ${selected === node.id ? 'selected' : ''} ${status ? statusClass(status) : ''}" data-node="${escapeHTML(node.id)}" style="left:${Number(node.x) || 0}px;top:${Number(node.y) || 0}px;${nodeStyle(type)}" aria-label="Блок ${escapeHTML(node.name)}" tabindex="0"><span class="node-status ${status ? statusClass(status) : ''}">${status ? `${icon(statusIcon(status))}${escapeHTML(statusNames[status] || status)}` : incoming.length ? '' : 'Вход в пайплайн'}</span><button class="port port-in" data-port="in" data-node-id="${escapeHTML(node.id)}" aria-label="Вход блока ${escapeHTML(node.name)}" ${readonly() ? 'disabled' : ''}></button><div class="node-header"><span class="node-icon">${icon(type.icon)}</span><div class="node-heading"><h3>${escapeHTML(node.name)}</h3><span class="node-kind">${readonly() ? 'Результат этапа' : 'Исполнитель промпта'}</span></div><button class="node-menu" aria-label="Настройки блока ${escapeHTML(node.name)}">···</button></div><p class="node-summary">${escapeHTML(type.summary)}</p><div class="node-divider"></div><div class="node-meta"><span class="model">${icon('terminal')}${escapeHTML(executor?.name || node.executor || 'Не настроен')}</span><span>Промпт</span></div><div class="node-footer"><span>${icon('file')}${incoming.length ? `${incoming.length} входов` : 'Контекст Git'}</span><span title="${escapeHTML((node.outputs || []).map(output => output.name).join(', '))}">${icon('box')}${escapeHTML(node.outputs?.[0]?.name || 'Нет выхода')}${node.outputs?.length > 1 ? ` +${node.outputs.length - 1}` : ''}</span></div><button class="port port-out ${pendingConnection === node.id ? 'pending' : ''}" data-port="out" data-node-id="${escapeHTML(node.id)}" aria-label="Выход блока ${escapeHTML(node.name)}" ${readonly() ? 'disabled' : ''}></button></article>`;
  }).join('');
  $('#canvas-empty').hidden = !!pipeline.nodes.length;
  renderEdges(); renderMinimap();
}
function edgePath(from, to) {
  const a = {x:from.x + 236,y:from.y + 117}, b = {x:to.x,y:to.y + 117};
  if (b.x > a.x + 20) {
    const bend = Math.max(35,(b.x-a.x)/2);
    return `M ${a.x} ${a.y} C ${a.x+bend} ${a.y}, ${b.x-bend} ${b.y}, ${b.x} ${b.y}`;
  }
  const middle = a.y === b.y ? a.y-170 : (a.y+b.y)/2;
  return `M ${a.x} ${a.y} C ${a.x+85} ${a.y}, ${a.x+85} ${middle}, ${a.x+35} ${middle} L ${b.x-35} ${middle} C ${b.x-85} ${middle}, ${b.x-85} ${b.y}, ${b.x} ${b.y}`;
}
function renderEdges() {
  const pipeline = graph();
  $('#edges').innerHTML = '<defs><marker id="edge-arrow" markerWidth="7" markerHeight="7" refX="6" refY="3.5" orient="auto"><path d="M0 0 L7 3.5 L0 7" fill="none" stroke="#8aa995"/></marker></defs>' + pipeline.edges.map(edge => {
    const from = nodeById(edge.from), to = nodeById(edge.to);
    if (!from || !to) return '';
    const path = edgePath(from,to), state = snapshotRun?.nodes?.[edge.to]?.status === 'running' ? 'running' : snapshotRun?.nodes?.[edge.from]?.status === 'success' ? 'complete' : '';
    return `<g><path class="edge-hit" data-from="${escapeHTML(edge.from)}" data-to="${escapeHTML(edge.to)}" d="${path}" ${readonly() ? '' : 'tabindex="0" role="button"'} aria-label="Связь ${escapeHTML(from.name)} — ${escapeHTML(to.name)}${readonly() ? '' : '. Удалить связь'}"><title>${readonly() ? 'Зависимость запуска' : 'Нажмите, чтобы удалить связь'}</title></path><path class="edge-line ${state}" d="${path}" marker-end="url(#edge-arrow)"/></g>`;
  }).join('');
}
function renderMinimap() {
  const nodes = graph().nodes;
  const maxX = Math.max(1200,...nodes.map(node => node.x+236)), maxY = Math.max(500,...nodes.map(node => node.y+240));
  $('#minimap').innerHTML = nodes.map(node => `<span class="mini-node" style="left:${10+node.x/maxX*108}px;top:${12+node.y/maxY*65}px;width:${236/maxX*108}px;height:${Math.min(24,240/maxY*65)}px"></span>`).join('');
}
function transformWorld(persist = false) {
  $('#world').style.transform = `translate(${pan.x}px,${pan.y}px) scale(${zoom})`;
  $('#zoom-reset').textContent = `${Math.round(zoom*100)}%`;
  $('#canvas').style.backgroundPosition = `${pan.x}px ${pan.y}px`;
  $('#canvas').style.backgroundSize = `${20*zoom}px ${20*zoom}px`;
  if (persist && !readonly() && draft()) { draft().viewport = {...pan,zoom}; markDirty(); }
}
function restoreViewport() {
  const viewport = graph()?.viewport;
  pan = {x:Number(viewport?.x) || 0,y:Number(viewport?.y) || 0};
  zoom = Math.min(2,Math.max(.25,Number(viewport?.zoom) || 1));
  transformWorld();
}
function fitCanvas(persist = true) {
  const nodes = graph()?.nodes || [], rect = $('#canvas').getBoundingClientRect();
  if (!nodes.length) { pan = {x:0,y:0}; zoom = 1; transformWorld(persist); return; }
  const minX = Math.min(...nodes.map(node => node.x)), maxX = Math.max(...nodes.map(node => node.x+236));
  const minY = Math.min(...nodes.map(node => node.y)), maxY = Math.max(...nodes.map(node => node.y+240));
  const width = Math.max(300,rect.width-(selected && rect.width>800 ? 360 : 0));
  zoom = Math.max(.25,Math.min(1,(width-60)/(maxX-minX),(rect.height-150)/(maxY-minY)));
  pan = {x:(width-(maxX-minX)*zoom)/2-minX*zoom,y:Math.max(95,(rect.height-(maxY-minY)*zoom)/2)-minY*zoom};
  transformWorld(persist);
}
function changeZoom(delta) {
  const rect = $('#canvas').getBoundingClientRect(), previous = zoom;
  zoom = Math.min(2,Math.max(.25,Math.round((zoom+delta)*100)/100));
  pan.x = rect.width/2-(rect.width/2-pan.x)*zoom/previous;
  pan.y = rect.height/2-(rect.height/2-pan.y)*zoom/previous;
  transformWorld(true);
}
function renderAll() { renderSidebar(); renderHeader(); renderNodes(); renderInspector(); }
function setView(next) {
  view = next;
  $('#editor-view').hidden = next !== 'editor';
  $('#history-view').hidden = next !== 'history';
  $$('[data-view]').forEach(button => { const active = button.dataset.view === next; button.classList.toggle('active',active); if (button.hasAttribute('role')) button.setAttribute('aria-selected',String(active)); });
  if (next === 'history') renderHistory();
}
function openPipeline(id) {
  if (busy) return;
  activeId = id; snapshotRun = null; selected = null; pendingConnection = null;
  $('#run-drawer').hidden = true;
  renderAll(); setView('editor'); restoreViewport(); renderRepositorySelect(contextSelection.repositoryId); loadContext();
  $('#add-repository').disabled = false;
}
function selectNode(id) {
  if (pendingConnection) return;
  selected = id; renderNodes(); renderInspector();
}
function resultSection(title, value, open = false) {
  return `<details class="result-section" ${open ? 'open' : ''}><summary>${escapeHTML(title)}</summary><pre>${escapeHTML(pretty(value ?? '—'))}</pre></details>`;
}
function renderInspector() {
  const node = nodeById(selected), panel = $('#inspector');
  if (!node) { panel.hidden = true; return; }
  const type = typeOf(node), result = snapshotRun?.nodes?.[node.id];
  panel.hidden = false;
  panel.innerHTML = `<div class="panel-header"><span class="node-icon" style="${nodeStyle(type)}">${icon(type.icon)}</span><div><h2>${escapeHTML(node.name)}</h2><p>${readonly() ? 'Снимок блока · только чтение' : 'Настройки блока'}</p></div><button class="icon-button" id="close-inspector" aria-label="Закрыть настройки">${icon('close')}</button></div><div class="panel-body">${readonly() ? renderResult(node,result) : renderNodeForm(node)} </div><div class="panel-footer">${readonly() ? '<span>Конфигурация сохранена при запуске</span>' : `<button class="danger-button" id="delete-node">${icon('trash')}Удалить блок</button><span>Изменения в черновике</span>`}</div>`;
  $('#close-inspector').onclick = closeInspector;
  if (readonly()) return;
  $('#node-name').oninput = event => { node.name = event.target.value; markDirty(); renderNodes(); $('.panel-header h2',panel).textContent = node.name; };
  $('#node-prompt').oninput = event => { node.prompt = event.target.value; markDirty(); };
  $('#node-executor').onchange = event => { node.executor = event.target.value; markDirty(); renderNodes(); };
  $$('[data-token]',panel).forEach(button => button.onclick = () => {
    const field = $('#node-prompt');
    field.setRangeText(`{{${button.dataset.token}}}`,field.selectionStart,field.selectionEnd,'end');
    node.prompt = field.value; markDirty(); field.focus();
  });
  $$('[data-output-name]',panel).forEach(field => field.onchange = action(async () => {
    if (!editable()) return;
    const name = field.value.trim(), outputId = field.dataset.outputName;
    const before = clone(draft());
    setBusy(true);
    try {
      const updated = await api(`/pipelines/${encodeURIComponent(activeId)}/rename-output`, {method:'POST',body:JSON.stringify({pipeline:before,nodeId:node.id,outputId,name})});
      pipelines[pipelines.findIndex(item => item.id === activeId)] = updated;
      markDirty(); renderNodes(); renderInspector();
    } catch (error) { field.value = node.outputs.find(output => output.id === outputId).name; throw error; }
    finally { setBusy(false); }
  }));
  $$('[data-remove-output]',panel).forEach(button => button.onclick = () => {
    if (!editable()) return;
    node.outputs = node.outputs.filter(output => output.id !== button.dataset.removeOutput);
    markDirty(); renderNodes(); renderInspector();
  });
  $('#add-output').onclick = () => {
    if (!editable()) return;
    let name = 'result', index = 2;
    while (node.outputs.some(output => output.name === name)) name = `result_${index++}`;
    node.outputs.push({id:uid('output'),name}); markDirty(); renderNodes(); renderInspector();
  };
  $('#add-input').onclick = () => {
    const alias = $('#input-alias').value.trim(), value = $('#input-source').value;
    if (!editable() || !value) return;
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(alias) || FlowlineGraph.builtins.includes(alias)) { toast('Нужно имя из латинских букв, цифр и подчёркивания, не совпадающее с контекстом.'); return; }
    if (Object.hasOwn(node.inputs || {},alias)) { toast('Такой вход уже существует. Сначала удалите его.'); return; }
    const [nodeId,outputId] = JSON.parse(value);
    node.inputs = {...node.inputs,[alias]:{nodeId,outputId}};
    markDirty(); renderInspector();
  };
  $$('[data-remove-input]',panel).forEach(button => button.onclick = () => { if (!editable()) return; delete node.inputs[button.dataset.removeInput]; markDirty(); renderInspector(); });
  $('#delete-node').onclick = () => deleteNode(node.id);
  if (busy) $$('input,textarea,select,button:not(#close-inspector)',panel).forEach(element => element.disabled = true);
}
function renderNodeForm(node) {
  const sources = FlowlineGraph.availableSources(graph(),node), tokens = FlowlineGraph.availableTokens(graph(),node);
  const executorOptions = executors.map(executor => `<option value="${escapeHTML(executor.id)}" ${node.executor === executor.id ? 'selected' : ''}>${escapeHTML(executor.name)}${executor.available ? '' : ' · недоступен'}</option>`).join('');
  const unknownExecutor = node.executor && !executors.some(executor => executor.id === node.executor);
  return `<div class="field"><label class="field-label" for="node-name">Название</label><input class="input" id="node-name" maxlength="120" value="${escapeHTML(node.name)}"><p class="field-help node-id">ID: ${escapeHTML(node.id)}</p></div><div class="field"><label class="field-label" for="node-executor">Исполнитель</label><select id="node-executor" class="select-input"><option value="" ${!node.executor ? 'selected' : ''}>Выберите исполнителя</option>${unknownExecutor ? `<option selected value="${escapeHTML(node.executor)}">${escapeHTML(node.executor)} · недоступен</option>` : ''}${executorOptions}</select><p class="field-help">${escapeHTML(unknownExecutor ? 'Этот профиль удалён или недоступен. Выберите существующего исполнителя; исходный ID сохранён.' : executors.find(executor => executor.id === node.executor)?.detail || 'Добавьте профиль через «Исполнители» в верхней панели.')}</p></div><div class="field"><label class="field-label" for="node-prompt">Промпт</label><textarea class="input prompt-input" id="node-prompt" spellcheck="false">${escapeHTML(node.prompt)}</textarea><p class="field-help">Вставьте вход нажатием. Доступны результаты связанных предшественников.</p><div class="token-list">${tokens.map(token => `<button class="context-token" data-token="${escapeHTML(token)}">${icon('box')}${escapeHTML(token)}</button>`).join('')}</div></div><div class="field"><div class="field-label">Именованные входы</div>${Object.entries(node.inputs || {}).map(([alias,source]) => {
    const available = sources.find(item => item.nodeId === source.nodeId && item.outputId === source.outputId);
    return `<div class="input-binding"><span><strong>${escapeHTML(alias)}</strong><small>${available ? `${escapeHTML(available.nodeName)} / ${escapeHTML(available.name)}` : 'Источник недоступен — проверьте связи'}</small></span><button class="icon-button" data-remove-input="${escapeHTML(alias)}" aria-label="Удалить вход ${escapeHTML(alias)}">${icon('close')}</button></div>`;
  }).join('')}<div class="binding-editor"><input class="input" id="input-alias" placeholder="Имя входа, например changes" aria-label="Имя входа"><select class="select-input" id="input-source" aria-label="Источник входа"><option value="">Выберите результат</option>${sources.map(source => `<option value="${escapeHTML(JSON.stringify([source.nodeId,source.outputId]))}">${escapeHTML(source.nodeName)} / ${escapeHTML(source.name)}</option>`).join('')}</select><button class="button secondary" id="add-input" ${sources.length ? '' : 'disabled'}>${icon('plus')}Добавить вход</button></div><p class="field-help">Именованный вход снимает неоднозначность одинаковых имён. Он связан со стабильным ID результата.</p></div><div class="field"><div class="field-label">Выходы</div>${(node.outputs || []).map(output => `<div class="output-row"><input class="input" data-output-name="${escapeHTML(output.id)}" aria-label="Имя результата" value="${escapeHTML(output.name)}" maxlength="80"><button class="icon-button" data-remove-output="${escapeHTML(output.id)}" aria-label="Удалить результат ${escapeHTML(output.name)}">${icon('close')}</button></div>`).join('')}<button class="button secondary" id="add-output">${icon('plus')}Добавить выход</button><p class="field-help">Переименование обновляет связанные ссылки. Для нескольких выходов исполнитель возвращает JSON-объект с этими именами.</p></div>${node.type === 'publish' ? `<p class="${publication.enabled ? 'field-help' : 'form-error'}">${escapeHTML(publication.detail)}</p>` : ''}`;
}
function renderResult(node,result) {
  return `${statusChip(result?.status || 'waiting')}<p class="field-help node-id">ID: ${escapeHTML(node.id)}<br>Исполнитель: ${escapeHTML(node.executor || '—')}</p>${result?.error ? `<p class="form-error">${escapeHTML(result.error)}</p>` : ''}<p class="field-help">Начало: ${formatTime(result?.startedAt)}<br>Завершение: ${formatTime(result?.finishedAt)}<br>Код завершения: ${result?.exitCode ?? '—'}</p>${resultSection('Фактические входы',result?.inputs || {},true)}${resultSection('Результаты',result?.outputs || {},true)}${resultSection('Промпт после подстановки',result?.prompt || '')}${resultSection('Исходный промпт',node.prompt)}${resultSection('Логи и выполненные проверки',result?.logs || '')}${resultSection('Diff блока',result?.diff || '')}${resultSection('Рабочее дерево и commit',{worktree:result?.worktree || '',commit:result?.commit || ''})}`;
}
function closeInspector() { selected = null; $('#inspector').hidden = true; renderNodes(); }
function deleteNode(id) {
  if (!editable()) return;
  draft().nodes = draft().nodes.filter(node => node.id !== id);
  draft().edges = draft().edges.filter(edge => edge.from !== id && edge.to !== id);
  selected = null; pendingConnection = null; markDirty(); renderAll();
  toast('Блок удалён. Проверьте промпты и входы зависимых блоков.');
}
function connect(from,to) {
  if (!editable()) return;
  if (from === to) { toast('Нельзя связать блок с самим собой.'); return; }
  if (draft().edges.some(edge => edge.from === from && edge.to === to)) { toast('Связь уже существует.'); return; }
  const edges = [...draft().edges,{from,to}];
  if (FlowlineGraph.hasCycle(draft().nodes,edges)) { toast('Эта связь создаёт цикл. Поддерживается граф без циклов.'); return; }
  draft().edges = edges; pendingConnection = null; markDirty(); renderAll();
}
function removeEdge(from,to) {
  if (!editable()) return;
  draft().edges = draft().edges.filter(edge => edge.from !== from || edge.to !== to);
  markDirty(); renderAll();
}
function openModal(content) {
  settingsSession = null;
  $('#modal').classList.remove('settings-modal');
  lastFocus = document.activeElement;
  $('#modal').innerHTML = content; $('#modal-overlay').hidden = false;
  $$('[data-close-modal]',$('#modal')).forEach(button => button.onclick = closeModal);
  requestAnimationFrame(() => $('#modal').querySelector('input,textarea,select,button')?.focus());
}
function closeModal() { settingsSession = null; $('#modal-overlay').hidden = true; lastFocus?.focus?.(); }
function modalHeader(title) { return `<div class="modal-head"><h2 id="modal-title">${escapeHTML(title)}</h2><button class="icon-button" data-close-modal aria-label="Закрыть">${icon('close')}</button></div>`; }
function showPalette() {
  if (!editable()) return;
  openModal(`${modalHeader('Добавить блок')}<p class="modal-intro">Один блок — одна задача. Свяжите этапы в рабочий процесс.</p>${Object.entries(types).map(([key,type]) => `<button class="palette-item" data-add-type="${key}" ${key === 'publish' && !publication.enabled ? 'disabled' : ''}><span class="node-icon" style="${nodeStyle(type)}">${icon(type.icon)}</span><div><strong>${escapeHTML(type.name)}</strong><small>${escapeHTML(key === 'publish' && !publication.enabled ? publication.detail : type.summary)}</small></div>${icon('plus')}</button>`).join('')}`);
  $$('[data-add-type]').forEach(button => button.onclick = () => {
    const rect = $('#canvas').getBoundingClientRect();
    const node = makeNode(button.dataset.addType,Math.max(20,Math.round(((rect.width-330)/2-pan.x)/zoom/10)*10),Math.max(80,Math.round(((rect.height-240)/2-pan.y)/zoom/10)*10));
    let suffix = 2, base = node.outputs[0].name;
    while (draft().nodes.some(item => item.outputs.some(output => output.name === node.outputs[0].name))) node.outputs[0].name = `${base}_${suffix++}`;
    draft().nodes.push(node); markDirty(); closeModal(); selectNode(node.id); renderHeader();
  });
}
function nameModal(isNew) {
  if (busy || (!isNew && readonly())) return;
  openModal(`${modalHeader(isNew ? 'Новый пайплайн' : 'Название пайплайна')}<form id="name-form"><label class="field-label" for="pipeline-name-input">Название</label><input class="input" id="pipeline-name-input" maxlength="120" required value="${isNew ? '' : escapeHTML(draft().name)}" placeholder="Например, Проверка изменений">${isNew ? `<label class="template-choice"><input type="checkbox" id="use-template"> Начать с графа «Разработка → Ревью → Тестирование${publication.enabled ? ' → Публикация' : ''}»</label>` : ''}<div class="modal-actions"><button class="button secondary" type="button" data-close-modal>Отмена</button><button class="button primary" type="submit">${isNew ? 'Создать' : 'Готово'}</button></div></form>`);
  $('#name-form').onsubmit = event => {
    event.preventDefault(); const name = $('#pipeline-name-input').value.trim(); if (!name) return;
    if (isNew) {
      const pipeline = makePipeline(name);
      if ($('#use-template').checked) {
        const stages = publication.enabled ? ['develop','review','test','publish'] : ['develop','review','test'];
        pipeline.nodes = stages.map((type,index) => makeNode(type,40+300*index,140));
        pipeline.edges = pipeline.nodes.slice(1).map((node,index) => ({from:pipeline.nodes[index].id,to:node.id}));
        pipeline.nodes[1].prompt = 'Проверь {{changes}}. Оцени корректность кода. Верни замечания и итоговый вердикт.';
        pipeline.nodes[2].prompt = 'Учти {{review}}. Запусти подходящие проверки кода. Верни команды и их реальные результаты.';
        if (pipeline.nodes[3]) pipeline.nodes[3].prompt = 'На основе {{tests}} и {{context}} подготовь локальное описание PR, релизные заметки и отчёт о выполненных проверках. Верни материалы текстом. Не выполняй push, создание или изменение PR, merge, выпуск релиза или другие удалённые изменения.';
      }
      pipelines.push(pipeline); dirty.add(pipeline.id); closeModal(); openPipeline(pipeline.id); requestAnimationFrame(() => fitCanvas(true));
    } else { draft().name = name; markDirty(); closeModal(); renderHeader(); renderSidebar(); }
  };
}
async function savePipeline(showToast = true) {
  if (readonly() || busy || !draft()) return false;
  const current = clone(draft()), version = revisions.get(current.id) || 0;
  setBusy(true);
  try {
    const stored = await api(`/pipelines/${encodeURIComponent(current.id)}`,{method:'PUT',body:JSON.stringify(current)});
    saved.add(current.id);
    if ((revisions.get(current.id) || 0) === version) {
      pipelines[pipelines.findIndex(item => item.id === current.id)] = stored;
      dirty.delete(current.id);
    }
    if (showToast) toast('Пайплайн сохранён.');
    renderSidebar(); return true;
  } finally { setBusy(false); }
}
function addRepositoryModal() {
  if (!editable()) return;
  openModal(`${modalHeader('Добавить репозиторий')}<p class="modal-intro">Укажите абсолютный путь к локальному Git-репозиторию.</p><form id="repository-form"><label class="field-label" for="repository-path">Путь</label><input class="input" id="repository-path" required autocomplete="off" placeholder="/Users/you/projects/my-app"><p class="field-help">Запуски используют отдельные рабочие деревья. Текущая ветка и несохранённые изменения остаются на месте.</p><p class="form-error" id="repository-error" hidden></p><div class="modal-actions"><button type="button" class="button secondary" data-close-modal>Отмена</button><button class="button primary" id="submit-repository" type="submit">Добавить</button></div></form>`);
  $('#repository-form').onsubmit = async event => {
    event.preventDefault(); const path = $('#repository-path').value.trim(); if (!path) return;
    const button = $('#submit-repository'); button.disabled = true; button.textContent = 'Проверка…';
    try {
      const repository = await api('/repositories',{method:'POST',body:JSON.stringify({path})});
      if (!repositories.some(item => item.id === repository.id)) repositories.push(repository);
      contextSelection = {repositoryId:repository.id,branch:'',pr:''}; renderRepositorySelect(repository.id); closeModal(); await loadContext(); toast('Репозиторий добавлен.');
    } catch (error) { $('#repository-error').textContent = error.message; $('#repository-error').hidden = false; }
    finally { button.disabled = false; button.textContent = 'Добавить'; }
  };
}
async function startRun() {
  if (readonly()) { await cancelRun(snapshotRun.id); return; }
  if (!editable()) return;
  const mismatch = contextMismatch(); if (mismatch) throw new Error(mismatch);
  const id = activeId;
  const request = {pipelineId:id,repositoryId:$('#repo-select').value,branch:$('#branch-select').value};
  if ($('#pr-select').value) request.prNumber = Number($('#pr-select').value);
  setBusy(true);
  try {
    const validation = await api(`/pipelines/${encodeURIComponent(id)}/validate`,{method:'POST',body:JSON.stringify(draft())});
    if (validation.errors?.length) throw new Error(`Пайплайн не готов к запуску:\n${validation.errors.join('\n')}`);
  } finally { setBusy(false); }
  if (!await savePipeline(false)) return;
  setBusy(true);
  try {
    const run = await api('/runs',{method:'POST',body:JSON.stringify(request)});
    runs = [run,...runs.filter(item => item.id !== run.id)];
    showRun(run); toast('Запуск создан. Статусы поступают от локального исполнителя.'); schedulePoll(100);
  } finally { setBusy(false); }
}
async function cancelRun(id) {
  if (busy) return;
  setBusy(true);
  try {
    const run = await api(`/runs/${encodeURIComponent(id)}/cancel`,{method:'POST',body:'{}'});
    runs = [run,...runs.filter(item => item.id !== id)];
    if (snapshotRun?.id === id) { snapshotRun = run; renderAll(); renderRunDrawer(); }
    toast('Остановка запрошена. Ожидаем завершения процессов запуска.'); schedulePoll(100);
  } finally { setBusy(false); }
}
function showRun(run) {
  snapshotRun = clone(run); selected = null; pendingConnection = null; drawerVisible = true; ++contextVersion;
  contextLoading = false;
  $('#repo-select').hidden = false;
  $('#empty-repository').hidden = true;
  $('#empty-repository').disabled = true;
  $('#repository-label').htmlFor = 'repo-select';
  $('#repo-select').innerHTML = `<option>${escapeHTML(run.context?.repository?.name || 'Репозиторий')}</option>`;
  $('#branch-select').innerHTML = `<option>${escapeHTML(run.context?.branch || '')}</option>`;
  const pr = run.context?.pr;
  $('#pr-select').innerHTML = `<option>${pr ? `#${pr.number} · ${escapeHTML(pr.head)} → ${escapeHTML(pr.base)}` : 'Без PR'}</option>`;
  $('#repo-select').disabled = $('#branch-select').disabled = $('#pr-select').disabled = $('#add-repository').disabled = $('#refresh-context').disabled = true;
  showContextMessage(`Исходный commit: ${run.context?.sha || '—'}. Конфигурация этого запуска не меняется при редактировании пайплайна.`);
  setView('editor'); renderAll(); restoreViewport(); renderRunDrawer();
}
function renderHistory() {
  $('#history-view').innerHTML = `<div class="history-heading"><div><h2>История запусков</h2><p>Снимки конфигурации, Git-контекст и результаты всех пайплайнов.</p></div><button class="button secondary" id="refresh-history">Обновить</button></div>${historyError ? `<p class="form-error">${escapeHTML(historyError)}</p>` : ''}${runs.length ? `<table class="history-table"><thead><tr><th>Пайплайн / запуск</th><th>Результат</th><th class="branch-column">Репозиторий / ветка / PR</th><th class="optional-column">Время</th><th></th></tr></thead><tbody>${[...runs].sort((a,b) => b.createdAt.localeCompare(a.createdAt)).map(run => `<tr><td>${escapeHTML(run.pipelineName)}<small>${escapeHTML(run.id)}</small></td><td>${statusChip(run.status)}</td><td class="branch-column">${escapeHTML(run.context?.repository?.name || '')}<small>${escapeHTML(run.context?.branch || '')}${run.context?.pr ? ` · PR #${run.context.pr.number}` : ' · без PR'}</small></td><td class="optional-column">${formatTime(run.createdAt)}<small>${run.finishedAt ? `Завершён ${formatTime(run.finishedAt)}` : 'Ожидает завершения'}</small></td><td><button class="button secondary" data-run-detail="${escapeHTML(run.id)}">Открыть</button></td></tr>`).join('')}</tbody></table>` : '<div class="empty-history">Запусков пока нет. Сохраните пайплайн, выберите репозиторий и запустите его.</div>'}`;
  $$('[data-run-detail]').forEach(button => button.onclick = action(async () => showRun(await api(`/runs/${encodeURIComponent(button.dataset.runDetail)}`))));
  $('#refresh-history').onclick = action(async () => { runs = await api('/runs'); historyError = ''; renderHistory(); renderHeader(); });
}
function renderRunDrawer() {
  const run = snapshotRun, drawer = $('#run-drawer');
  if (!run) { drawer.hidden = true; return; }
  drawer.hidden = !drawerVisible;
  const previousScroll = $('.run-output',drawer)?.scrollTop || 0;
  drawer.innerHTML = `<div class="run-header">${statusChip(run.status)}<strong>${escapeHTML(run.pipelineName)}</strong><span class="run-note">Снимок · ${formatTime(run.createdAt)}</span>${activeStatus(run.status) ? `<button class="button secondary" id="stop-run">${icon('stop')}Стоп</button>` : ''}<button class="icon-button" id="close-run" aria-label="Скрыть результаты запуска">${icon('close')}</button></div><div class="run-body"><div class="run-steps">${run.snapshot.nodes.map(node => `<button class="run-step ${statusClass(run.nodes?.[node.id]?.status)}" data-result-node="${escapeHTML(node.id)}">${icon(statusIcon(run.nodes?.[node.id]?.status))}<span>${escapeHTML(node.name)}<small>${escapeHTML(statusNames[run.nodes?.[node.id]?.status] || 'Ожидание')}</small></span></button>`).join('')}</div><div class="run-output">${run.error ? `<p class="form-error">${escapeHTML(run.error)}</p>` : ''}<p>Выберите блок, чтобы посмотреть фактические входы, результаты, промпт и логи.</p>${resultSection('Итоговый diff',run.diff || 'Изменений пока нет')}${run.snapshot.nodes.map(node => { const result = run.nodes?.[node.id]; return `<div class="log-line"><strong>${escapeHTML(node.name)} · ${escapeHTML(statusNames[result?.status] || 'Ожидание')}</strong>${result?.error ? `<p class="form-error">${escapeHTML(result.error)}</p>` : ''}<pre>${escapeHTML(result?.logs || '')}</pre></div>`; }).join('')}</div></div>`;
  $('#close-run').onclick = () => { drawerVisible = false; drawer.hidden = true; };
  if ($('#stop-run')) $('#stop-run').onclick = action(() => cancelRun(run.id));
  $$('[data-result-node]').forEach(button => button.onclick = () => selectNode(button.dataset.resultNode));
  $('.run-output',drawer).scrollTop = previousScroll;
}
function schedulePoll(delay = 800) { clearTimeout(pollTimer); pollTimer = setTimeout(pollRuns,delay); }
async function pollRuns() {
  if (polling || !ready) { schedulePoll(); return; }
  polling = true;
  try {
    runs = await api('/runs'); historyError = '';
    if (snapshotRun) {
      const latest = runs.find(run => run.id === snapshotRun.id);
      if (latest && JSON.stringify(latest) !== JSON.stringify(snapshotRun)) {
        snapshotRun = latest; renderHeader(); renderNodes(); renderInspector(); renderRunDrawer();
      }
    }
    if (view === 'history') renderHistory();
    renderHeader();
  } catch (error) {
    historyError = error.message;
    if (snapshotRun && activeStatus(snapshotRun.status)) showError(error);
    if (view === 'history') renderHistory();
  } finally { polling = false; schedulePoll(runs.some(run => activeStatus(run.status)) ? 800 : 5000); }
}
async function showExecutorSettings() {
  openModal(`${modalHeader('Исполнители')}<p class="modal-intro">Загрузка локальных профилей…</p>`);
  $('#modal').classList.add('settings-modal');
  const session = {configs:[],infos:[],changed:new Set(),saving:false,loading:true};
  settingsSession = session;
  try {
    const data = await api('/executors');
    if (settingsSession !== session) return;
    session.loading = false;
    session.configs = FlowlineExecutors.payload(data.configs || []);
    session.infos = data.executors || [];
    executors = session.infos;
    renderExecutorSettings(session);
    renderNodes(); renderInspector();
  } catch (error) {
    if (settingsSession !== session) return;
    $('#modal').innerHTML = `${modalHeader('Исполнители')}<p class="form-error">${escapeHTML(error.message)}</p><button class="button secondary" id="retry-executors">Повторить</button>`;
    $$('[data-close-modal]',$('#modal')).forEach(button => button.onclick = closeModal);
    $('#retry-executors').onclick = action(showExecutorSettings);
  }
}
function executorField(config,key,label,placeholder = '',note = '') {
  const id = `executor-${config.id}-${key}`;
  return `<div class="field"><label class="field-label" for="${escapeHTML(id)}">${escapeHTML(label)}</label><input class="input" id="${escapeHTML(id)}" data-executor-field="${key}" value="${escapeHTML(config[key] || '')}" placeholder="${escapeHTML(placeholder)}" autocomplete="off" spellcheck="false">${note ? `<p class="field-help">${escapeHTML(note)}</p>` : ''}</div>`;
}
function renderExecutorSettings(session) {
  if (settingsSession !== session) return;
  $('#modal').innerHTML = `${modalHeader('Исполнители')}<p class="modal-intro">Создайте профили и выбирайте их в настройках каждого блока. Секреты остаются в окружении локального сервера.</p><div class="executor-list" id="executor-list">${session.configs.map(config => {
    const info = session.infos.find(item => item.id === config.id), changed = session.changed.has(config.id), apiKind = config.kind === 'openai-compatible';
    return `<section class="executor-card" data-executor-id="${escapeHTML(config.id)}"><div class="executor-card-heading"><strong data-executor-title>${escapeHTML(config.name || 'Новый исполнитель')}</strong><span class="executor-availability ${!changed && info?.available ? 'available' : ''}" data-executor-availability>${changed ? 'Не сохранён' : info?.available ? 'Доступен' : 'Недоступен'}</span><button class="icon-button" type="button" data-remove-executor="${escapeHTML(config.id)}" aria-label="Удалить профиль ${escapeHTML(config.name)}">${icon('trash')}</button></div><p class="field-help" data-executor-detail>${escapeHTML(changed ? 'Сохраните профиль, чтобы проверить доступность.' : info?.detail || 'Сохраните профиль, чтобы проверить доступность.')}</p><div class="executor-fields">${executorField(config,'name','Название','Например, Codex · основная модель')}<div class="field"><label class="field-label" for="executor-${escapeHTML(config.id)}-kind">Тип исполнителя</label><select class="select-input" id="executor-${escapeHTML(config.id)}-kind" data-executor-kind>${[['codex','Codex CLI'],['claude','Claude Code'],['openai-compatible','OpenAI-совместимый API']].map(([kind,name]) => `<option value="${kind}" ${config.kind === kind ? 'selected' : ''}>${name}</option>`).join('')}</select></div>${apiKind ? executorField(config,'baseUrl','Base URL','https://api.deepseek.com','Адрес API без ключа, логина и параметров.')+executorField(config,'apiKeyEnv','Переменная окружения с API-ключом','DEEPSEEK_API_KEY','Только имя переменной. Её значение задайте при запуске backend; браузер его не получает.') : executorField(config,'command','Команда или путь к CLI',config.kind === 'claude' ? 'claude' : 'codex','Только исполняемый файл, без shell-команды и аргументов. Авторизуйтесь в CLI отдельно.')} ${executorField(config,'model',apiKind ? 'Модель API' : 'Модель (необязательно)',apiKind ? 'deepseek-chat' : 'По умолчанию CLI',apiKind ? 'API-профиль возвращает текст; доступ к файлам и инструментам зависит от backend.' : 'Пустое поле использует модель, настроенную в CLI.')}</div><p class="field-help node-id">ID: ${escapeHTML(config.id)}</p></section>`;
  }).join('') || '<p class="empty-executors">Профилей пока нет. Добавьте CLI или API-исполнителя.</p>'}</div><div class="executor-add"><label class="field-label" for="new-executor-kind">Новый профиль</label><select class="select-input" id="new-executor-kind"><option value="codex">Codex CLI</option><option value="claude">Claude Code</option><option value="openai-compatible">OpenAI-совместимый API</option></select><button type="button" class="button secondary" id="add-executor">${icon('plus')}Добавить</button></div><p class="field-help">«Доступен» означает, что backend нашёл CLI или требуемую переменную окружения. Авторизация и ответ провайдера проверяются при реальном запуске. Удаление профиля не заменяет исполнителя в существующих блоках.</p><p class="form-error" id="executor-error" role="alert" hidden></p><div class="modal-actions"><button class="button secondary" id="refresh-executors" type="button">Проверить сохранённые</button><button class="button primary" id="save-executors" type="button">Сохранить профили</button></div>`;
  $$('[data-close-modal]',$('#modal')).forEach(button => button.onclick = closeModal);
  $$('[data-executor-id]').forEach(card => {
    const config = session.configs.find(item => item.id === card.dataset.executorId);
    const changed = () => {
      session.changed.add(config.id);
      $('[data-executor-availability]',card).textContent = 'Не сохранён';
      $('[data-executor-availability]',card).classList.remove('available');
      $('[data-executor-detail]',card).textContent = 'Сохраните профиль, чтобы проверить доступность.';
      $('[data-executor-title]',card).textContent = config.name || 'Новый исполнитель';
    };
    $$('[data-executor-field]',card).forEach(field => field.oninput = () => { config[field.dataset.executorField] = field.value; changed(); });
    $('[data-executor-kind]',card).onchange = event => {
      config.kind = event.target.value;
      if (config.kind === 'openai-compatible') { config.command = ''; config.baseUrl ||= ''; config.apiKeyEnv ||= ''; }
      else { config.command = config.kind; config.baseUrl = ''; config.apiKeyEnv = ''; }
      changed(); renderExecutorSettings(session);
    };
  });
  $$('[data-remove-executor]').forEach(button => button.onclick = () => {
    session.configs = session.configs.filter(config => config.id !== button.dataset.removeExecutor);
    renderExecutorSettings(session);
  });
  $('#add-executor').onclick = () => {
    const kind = $('#new-executor-kind').value;
    const config = {id:uid('executor'),name:kind === 'codex' ? 'Codex CLI' : kind === 'claude' ? 'Claude Code' : 'API-исполнитель',kind,command:kind === 'openai-compatible' ? '' : kind,model:'',baseUrl:'',apiKeyEnv:''};
    session.configs.push(config); session.changed.add(config.id); renderExecutorSettings(session);
    $$('[data-executor-id]').at(-1)?.scrollIntoView({block:'center',behavior:'smooth'});
  };
  $('#save-executors').onclick = () => saveExecutorSettings(session);
  $('#refresh-executors').onclick = async () => {
    setExecutorSettingsBusy(session,true);
    try {
      const data = await api('/executors');
      executors = data.executors || [];
      if (settingsSession !== session) return;
      session.infos = executors; renderExecutorSettings(session); renderNodes(); renderInspector();
    } catch (error) { executorSettingsError(session,error.message); }
    finally { setExecutorSettingsBusy(session,false); }
  };
  setExecutorSettingsBusy(session,session.saving);
}
function setExecutorSettingsBusy(session,value) {
  session.saving = value;
  if (settingsSession !== session) return;
  $$('input,select,button:not([data-close-modal])',$('#modal')).forEach(element => element.disabled = value);
  $('#save-executors').textContent = value ? 'Проверка…' : 'Сохранить профили';
}
function executorSettingsError(session,message) {
  if (settingsSession !== session) return;
  $('#executor-error').textContent = message;
  $('#executor-error').hidden = false;
}
async function saveExecutorSettings(session) {
  if (settingsSession !== session || session.saving || session.loading) return;
  const errors = FlowlineExecutors.validate(session.configs);
  if (errors.length) { executorSettingsError(session,errors.join('\n')); return; }
  setExecutorSettingsBusy(session,true);
  try {
    const data = await api('/executors',{method:'POST',body:JSON.stringify({configs:FlowlineExecutors.payload(session.configs)})});
    executors = data.executors || [];
    renderNodes(); renderInspector();
    if (settingsSession !== session) return;
    session.configs = FlowlineExecutors.payload(data.configs || []); session.infos = executors; session.changed.clear();
    renderExecutorSettings(session); toast('Профили исполнителей сохранены.');
  } catch (error) { executorSettingsError(session,error.message); }
  finally { setExecutorSettingsBusy(session,false); }
}

$$('[data-icon]').forEach(element => element.innerHTML = icon(element.dataset.icon));
$('#repo-select').onchange = action(async () => { contextSelection = {repositoryId:$('#repo-select').value,branch:'',pr:''}; await loadContext(); });
$('#branch-select').onchange = () => { contextSelection.branch = $('#branch-select').value; updateContextMessage(); };
$('#pr-select').onchange = () => { contextSelection.pr = $('#pr-select').value; updateContextMessage(); };
$('#refresh-context').onclick = action(loadContext);
$('#add-repository').onclick = addRepositoryModal;
$('#empty-repository').onclick = addRepositoryModal;
$('#pipeline-list').onclick = event => { const button = event.target.closest('[data-pipeline]'); if (button) openPipeline(button.dataset.pipeline); };
$$('[data-view]').forEach(button => button.onclick = () => { if (button.dataset.view === 'editor' && readonly()) openPipeline(activeId); else setView(button.dataset.view); });
$('.brand').onclick = event => { event.preventDefault(); openPipeline(activeId); };
$('#return-editor').onclick = () => openPipeline(activeId);
$('#add-block').onclick = showPalette; $('#empty-add').onclick = showPalette;
$('#save-button').onclick = action(() => savePipeline()); $('#run-button').onclick = action(startRun);
$('#new-pipeline').onclick = () => nameModal(true); $('#rename-pipeline').onclick = () => nameModal(false);
$('#executor-settings').onclick = action(showExecutorSettings);
$('#zoom-in').onclick = () => changeZoom(.1); $('#zoom-out').onclick = () => changeZoom(-.1);
$('#zoom-reset').onclick = () => changeZoom(1-zoom); $('#fit-canvas').onclick = () => fitCanvas(true);
$('#modal-overlay').onclick = event => { if (event.target === $('#modal-overlay')) closeModal(); };
$('#nodes').onclick = event => {
  if (suppressClick) return;
  const port = event.target.closest('[data-port]');
  if (port) {
    if (!editable()) return;
    const id = port.dataset.nodeId;
    if (port.dataset.port === 'out') { pendingConnection = pendingConnection === id ? null : id; renderHeader(); renderNodes(); }
    else if (pendingConnection) connect(pendingConnection,id);
    else toast('Сначала выберите выход справа у исходного блока.');
    return;
  }
  const card = event.target.closest('[data-node]'); if (card) selectNode(card.dataset.node);
};
$('#edges').onclick = event => { const edge = event.target.closest('[data-from]'); if (edge) removeEdge(edge.dataset.from,edge.dataset.to); };
$('#edges').onkeydown = event => { const edge = event.target.closest('[data-from]'); if (edge && ['Enter',' '].includes(event.key)) { event.preventDefault(); removeEdge(edge.dataset.from,edge.dataset.to); } };
$('#nodes').onkeydown = event => {
  if (event.target.closest('button')) return;
  const card = event.target.closest('[data-node]');
  if (card && ['Enter',' '].includes(event.key)) { event.preventDefault(); selectNode(card.dataset.node); }
  if (card && editable() && ['ArrowUp','ArrowDown','ArrowLeft','ArrowRight'].includes(event.key)) {
    event.preventDefault(); const node = nodeById(card.dataset.node), distance = event.shiftKey ? 50 : 10;
    if (event.key === 'ArrowUp') node.y = Math.max(0,node.y-distance);
    if (event.key === 'ArrowDown') node.y += distance;
    if (event.key === 'ArrowLeft') node.x = Math.max(0,node.x-distance);
    if (event.key === 'ArrowRight') node.x += distance;
    markDirty(); renderNodes(); $$('[data-node]').find(item => item.dataset.node === node.id)?.focus();
  }
};
$('#canvas').addEventListener('pointerdown',event => {
  if (event.button !== 0 || event.target.closest('button,.edge-hit')) return;
  const header = event.target.closest('.node-header');
  if (header && editable()) {
    const node = nodeById(header.closest('[data-node]').dataset.node);
    drag = {type:'node',id:node.id,startX:event.clientX,startY:event.clientY,x:node.x,y:node.y,moved:false};
  } else if (!event.target.closest('.node')) { drag = {type:'pan',startX:event.clientX,startY:event.clientY,x:pan.x,y:pan.y,moved:false}; $('#canvas').classList.add('panning'); }
  if (drag) $('#canvas').setPointerCapture(event.pointerId);
});
$('#canvas').addEventListener('pointermove',event => {
  if (!drag) return;
  const dx = event.clientX-drag.startX, dy = event.clientY-drag.startY;
  if (Math.abs(dx)+Math.abs(dy)>4) drag.moved = true;
  if (drag.type === 'pan') { pan.x = drag.x+dx; pan.y = drag.y+dy; transformWorld(); }
  else if (drag.moved) {
    const node = nodeById(drag.id); node.x = Math.max(0,Math.round((drag.x+dx/zoom)/10)*10); node.y = Math.max(0,Math.round((drag.y+dy/zoom)/10)*10);
    const card = $$('[data-node]').find(item => item.dataset.node === node.id); card.style.left = `${node.x}px`; card.style.top = `${node.y}px`; renderEdges(); renderMinimap();
  }
});
function finishDrag(event) {
  if (!drag) return;
  if (drag.moved) { if (drag.type === 'pan') transformWorld(true); else markDirty(); suppressClick = true; setTimeout(() => suppressClick = false,100); }
  else if (drag.type === 'pan') { pendingConnection = null; closeInspector(); renderHeader(); }
  drag = null; $('#canvas').classList.remove('panning');
  if ($('#canvas').hasPointerCapture(event.pointerId)) $('#canvas').releasePointerCapture(event.pointerId);
}
$('#canvas').addEventListener('pointerup',finishDrag);
$('#canvas').addEventListener('pointercancel',finishDrag);
$('#canvas').addEventListener('wheel',event => { event.preventDefault(); if (event.ctrlKey || event.metaKey) changeZoom(event.deltaY>0 ? -.05 : .05); else { pan.x -= event.deltaX; pan.y -= event.deltaY; transformWorld(true); } },{passive:false});
document.addEventListener('keydown',event => {
  const typing = /INPUT|TEXTAREA|SELECT/.test(event.target.tagName);
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 's') { event.preventDefault(); if (settingsSession) saveExecutorSettings(settingsSession); else action(() => savePipeline())(event); return; }
  if (event.key === 'Escape') { if (!$('#modal-overlay').hidden) closeModal(); else if (pendingConnection) { pendingConnection = null; renderHeader(); renderNodes(); } else closeInspector(); }
  if (!typing && event.key === 'Delete' && selected && $('#modal-overlay').hidden) deleteNode(selected);
  if (event.key === 'Tab' && !$('#modal-overlay').hidden) {
    const focusable = $$('button,input,select,textarea,a[href]',$('#modal')).filter(element => !element.disabled && !element.hidden);
    const first = focusable[0], last = focusable.at(-1);
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus(); }
  }
});
window.addEventListener('beforeunload',event => { if (dirty.size) { event.preventDefault(); event.returnValue = ''; } });
async function bootstrap() {
  $('#run-button').disabled = $('#save-button').disabled = $('#add-block').disabled = true;
  showContextMessage('Подключение к локальному серверу…');
  try {
    const data = await api('/bootstrap');
    repositories = data.repositories || []; pipelines = data.pipelines || []; runs = data.runs || []; executors = data.executors || [];
    publication = data.publication || publication;
    saved = new Set(pipelines.map(item => item.id));
    if (!pipelines.length) pipelines.push(makePipeline());
    activeId = pipelines[0].id; ready = true;
    renderAll(); restoreViewport(); renderRepositorySelect(); await loadContext(); schedulePoll();
  } catch (error) {
    showError(error); showContextMessage('Не удалось загрузить приложение. Запустите локальный сервер и перезагрузите страницу.',true);
  }
}
bootstrap();
