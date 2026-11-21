# Flowline implementation contracts

Module: flowline, Go 1.27. Shared domain contracts: backend/internal/model. Implementations are split into config/app, api/http, service/run, service/graph, repository/{sqlite,git,settings,executorconfig}, client/{github,chatcompletion}, cli/{codex,claude,process}. The service entry point is cmd/service; executorconfig only reads the legacy format.
Frontend: buildless HTML/CSS/JS in frontend, served same-origin by backend.

JSON uses camelCase. IDs UUID/random hex, output names identifiers.

Types (exported Go field names follow normal acronyms):
- Repository: ID, Name, Path, Remote (all string).
- Branch: Name, SHA (string).
- PullRequest: Number(int), Title, URL, Head, Base, HeadSHA, BaseSHA, Repository (string).
- GitContext: Repository(Repository), Branch, SHA (string), PR(*PullRequest).
- Output: ID, Name, Source (string); optional source is stdout/stderr/exitCode for CMD only, inferred from the standard name if omitted.
- Source: NodeID, OutputID (string).
- Node: ID, Type, Name, Executor, Prompt, Command (string); InputJSON(*string, JSON field `inputJson`, optional text); X,Y(float64); Outputs([]Output); Inputs(map[string]Source).
- Edge: From, To(string).
- Viewport: X,Y,Zoom(float64).
- Pipeline: ID, Name, Description(string), Nodes([]Node), Edges([]Edge), Viewport(Viewport), UpdatedAt(string).
- NodeRun: NodeID, Status, StartedAt, FinishedAt, Error, Prompt, Command, Stdout, Stderr, Logs, Worktree, Commit, Diff(string); InputJSON(json.RawMessage, resolved envelope); Inputs(map[string]string); Outputs(map[string]string); ExitCode(*int).
- Run: ID, PipelineID, PipelineName, Status, CreatedAt, FinishedAt, Error, Diff(string); Snapshot(Pipeline); Context(GitContext); Nodes(map[string]*NodeRun).
States: waiting,running,success,error,cancelled,skipped,interrupted. Run terminal success/error/cancelled/interrupted.
- ExecutorInfo (API only): id,name,available,detail.

Graph API:
ValidateGraph(Pipeline) []string. Empty nodes invalid for run; SavePipeline may store incomplete draft.
ResolveInputs(Pipeline, Node, GitContext, map[string]*NodeRun) (map[string]string,error).
RenderPrompt(prompt string, inputs map[string]string) (string,error).
ResolveInputJSON(Pipeline, Node, GitContext, map[string]*NodeRun, map[string]string) (json.RawMessage,error).
RenameOutput(*Pipeline,nodeID,outputID,newName string) error. Bindings use stable output IDs; rewrite shorthand/qualified AI prompt and JSON string-value tokens only when reference resolves to renamed output. Preserve aliases, JSON keys, CMD source identity and literal scripts; qualify collisions atomically.
Only direct consumers need reference rewrites. Incomplete JSON in unrelated draft nodes does not block a rename; invalid JSON in an affected consumer still rejects it atomically.
Builtin tokens: context,repository,branch,commit,pull_request. Direct predecessors only. Qualified token: nodeID.outputName. Implicit shorthand is ambiguous if two direct predecessors expose same name, unless explicit Node.Inputs alias resolves it. Validate missing sources, outputs, direct edges, duplicate IDs/edges, cycles, invalid names, reserved aliases, malformed tokens. ValidateGraph requires a nonempty Executor and Prompt only for AI; actual profile availability belongs to engine. CMD requires a nonempty Command and valid stream sources. Unknown types fail in draft and execution validation.
ResolveInputs provides builtins, unambiguous predecessor outputs, qualified output names, explicit aliases. Outputs map keyed by output NAME; bindings by output ID mapped via snapshot.

SQLite repository API:
OpenStore(path string)(*Store,error), Close() error.
Repositories()([]Repository,error), SaveRepository(Repository) error, Repository(id)(Repository,error).
Pipelines()([]Pipeline,error), SavePipeline(Pipeline) error, Pipeline(id)(Pipeline,error).
Runs()([]Run,error), Run(id)(Run,error), SaveRun(Run) error.
RecoverRuns() error: mark previously running/waiting runs interrupted and active nodes interrupted, waiting nodes skipped. Preserve history/snapshots; SaveRun must reject changing existing snapshot/context. Use embedded SQL migrations, WAL, busy timeout. sql.ErrNoRows on missing.

HTTP JSON API:
GET /api/bootstrap -> {repositories:[],pipelines:[],runs:[],executors:[],publication:{enabled:true,detail:"local preparation only"}}
POST /api/repositories {path} -> Repository
GET /api/repositories/:id/context -> {branches:[],pullRequests:[],prError:"..."} (branches can succeed even if PR access fails)
PUT /api/pipelines/:id Pipeline -> Pipeline (draft allowed)
POST /api/pipelines/:id/validate Pipeline -> {errors:[]}
POST /api/pipelines/:id/rename-output {pipeline,nodeId,outputId,name} -> Pipeline
POST /api/runs {pipelineId,repositoryId,branch,prNumber?} -> Run (validates saved pipeline and actual context)
GET /api/runs -> []Run
GET /api/runs/:id -> Run
POST /api/runs/:id/cancel -> Run
Errors HTTP >=400: {error:string,details?:[]}. UI polls active run every 800ms; no mock timers/results.
CSRF: frontend sends X-Flowline-Request: 1 on all mutations; backend validates Origin/Host loopback same-origin.

UI starts empty (templates allowed as unsaved graphs, never fake histories/repositories/results). Run snapshot shown separately with return-to-editor button; historical graph is immutable, node inputs/output/logs visible. Current editing doesn't affect Run.Snapshot.

## Executor configuration

GET /api/executors -> {configs:[],executors:[]}; POST /api/executors {configs:[]} saves nonsecret definitions atomically. Config fields: id,name,kind,command,model,baseUrl,apiKeyEnv. kind is codex,claude,openai-compatible. APIKeyEnv names a backend environment variable; keys are never returned. A run includes immutable executorConfigs and retains executor instances selected at creation.

Application settings are read from `~/.flowline/settings.json` (or `--settings`).
SQLite, run worktrees and the instance lock default to the same global
`~/.flowline` directory, regardless of cwd or an alternate settings file location.
Explicit `dataDir`/`--data` may select another directory. Project-local state is
not automatically merged into this database.
The document contains optional `listen`, `dataDir`, `frontendDir` and `executors`.
Explicit CLI flags override server/path settings. File-relative paths resolve from
the settings directory; CLI-relative paths resolve from cwd; `~/` is supported.
POST /api/executors updates only `executors` in that document and preserves server
settings. Startup reads without rewriting. If the implicit default file is absent,
legacy `<data>/executors.json` is read; malformed settings never fall back silently.

Publication means only local preparation (description, checks summary, release notes and diff). Flowline has no push, remote PR mutation, merge or release endpoint.

## Execution types and JSON input

`ai` and `cmd` are execution types. `develop`, `review`, `test`, `publish`, `prompt`
and the empty type remain AI-compatible. New templates create AI nodes with stage
names. No storage migration or historical payload rewrite is required.

New nodes default inputJson to the text `{}`. The backend also uses `{}` when the
field is absent on ai/cmd. Explicit empty/invalid/non-object JSON is a run validation
error; incomplete JSON drafts can still save. On legacy nodes, absent inputJson
preserves exact prompt-only execution; setting the field opts into the new envelope.

Envelope fields: `context` (GitContext), `predecessors` (node ID -> output name ->
string), `inputs` (existing resolved builtin/shorthand/qualified/alias bindings),
`data` (resolved user JSON object). References are substituted once in JSON string
values recursively, preserving JSON escaping and numeric precision. Keys remain
literal. Reference validation uses existing direct-edge and stable-ID rules.

AI appends the serialized envelope to the rendered instructions and keeps the
existing named-output protocol. CMD runs literal Command using `/bin/bash
--noprofile --norc -e -o pipefail -c`, receives the envelope plus a newline on stdin,
and needs no profile. Commands use the same isolated worktree, process guardian,
30-minute timeout, cancellation, dependency failure and Git snapshot paths.

CMD creation in the palette supplies three stable output definitions: stdout,
stderr, exitCode. Each can map to any of those sources via Source; empty stdout and
stderr are valid results. Renaming an inferred standard source makes Source explicit.
AI rejects Source fields. CMD output values are strings, including exitCode;
NodeRun.ExitCode remains numeric. On error, both streams, outputs, code, logs,
resolved input and script remain available. RunProcessStreams returns bounded raw
stdout/stderr separately; RunProcess retains the existing stdout/code/error API.
Callbacks redact complete lines, and execution redacts streams and JSON structurally
before persistence. Stored source configuration remains the immutable user snapshot.

HTTP run responses (including bootstrap, list, start and cancel) also expose
`nodes[id].inputJsonText` when resolved input exists. This is the exact stored,
already-redacted JSON as a string so history can display numbers beyond JavaScript
precision without rounding or converting them to null. The existing `inputJson`
object stays unchanged for API consumers; `inputJsonText` is derived at the HTTP
boundary and is not duplicated in storage.
