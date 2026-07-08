package serve

// pageHTML is the whole web dashboard: one self-contained mobile-friendly
// page that polls /api/state and drives /api/answer and /api/tasks.
const pageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>HYDRA</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; margin: 0; }
  body { background:#0d1117; color:#e6edf3; font:15px/1.45 system-ui,sans-serif; max-width:720px; margin:0 auto; padding:14px; }
  h1 { font-size:19px; letter-spacing:2px; color:#d2a8ff; }
  h1 small { color:#8b949e; letter-spacing:0; font-weight:normal; margin-left:8px; }
  h2 { font-size:13px; color:#8b949e; text-transform:uppercase; letter-spacing:1px; margin:20px 0 8px; }
  .card { background:#161b22; border:1px solid #30363d; border-radius:10px; padding:10px 12px; margin-bottom:8px; }
  .row { display:flex; align-items:center; gap:8px; flex-wrap:wrap; }
  .dot { width:10px; height:10px; border-radius:50%; flex:none; }
  .needs-you .dot { background:#f85149; box-shadow:0 0 8px #f85149; }
  .working .dot { background:#3fb950; } .done .dot { background:#39c5cf; }
  .started .dot { background:#d29922; } .idle .dot, .ended .dot { background:#484f58; }
  .queued .dot { background:#d29922; } .running .dot { background:#3fb950; } .failed .dot { background:#f85149; }
  .proj { font-weight:600; }
  .meta { color:#8b949e; font-size:13px; }
  .detail { color:#c9d1d9; font-size:13px; margin-top:4px; width:100%; }
  button { background:#21262d; color:#e6edf3; border:1px solid #30363d; border-radius:8px; padding:7px 14px; font-size:14px; cursor:pointer; }
  button.ok { background:#238636; border-color:#2ea043; }
  button.no { background:#6e1e22; border-color:#f85149; }
  .btns { margin-left:auto; display:flex; gap:6px; }
  pre { background:#0d1117; border:1px solid #21262d; border-radius:8px; padding:8px; font-size:12px; overflow-x:auto; margin-top:8px; white-space:pre-wrap; }
  pre .you { color:#39c5cf; } pre .claude { color:#e6edf3; } pre .tool { color:#d29922; } pre .result { color:#8b949e; } pre .info { color:#d2a8ff; }
  form { display:flex; flex-direction:column; gap:6px; }
  textarea, input, select { background:#0d1117; color:#e6edf3; border:1px solid #30363d; border-radius:8px; padding:8px; font-size:14px; width:100%; }
  #flash { position:fixed; bottom:14px; left:50%; transform:translateX(-50%); background:#1f6feb; padding:8px 16px; border-radius:20px; display:none; font-size:14px; max-width:90%; }
</style>
</head>
<body>
<h1>HYDRA <small>many heads, one brain</small></h1>
<h2>Sessions</h2><div id="sessions"><div class="meta">loading…</div></div>
<h2>Queue a task</h2>
<div class="card"><form id="taskform">
  <textarea id="prompt" rows="2" placeholder="What should Claude do? (runs headless)"></textarea>
  <input id="dir" placeholder="working directory (absolute path)">
  <select id="mode">
    <option value="acceptEdits" selected>acceptEdits — can edit files, asks for the rest</option>
    <option value="default">default — asks for everything (may stall headless)</option>
    <option value="plan">plan — read-only planning</option>
    <option value="bypassPermissions">bypassPermissions — DANGER: no asking at all</option>
  </select>
  <button class="ok" type="submit">Run task</button>
</form></div>
<h2>Tasks</h2><div id="tasks"><div class="meta">none yet</div></div>
<div id="flash"></div>
<script>
const token = new URLSearchParams(location.search).get('token') || '';
const api = (path, opts) => fetch(path + (path.includes('?') ? '&' : '?') + 'token=' + token, opts).then(r => r.json());
let open = null; // session id whose transcript is expanded

function flash(msg) {
  const el = document.getElementById('flash');
  el.textContent = msg; el.style.display = 'block';
  setTimeout(() => el.style.display = 'none', 3000);
}
function age(s) { return s < 60 ? s + 's' : s < 3600 ? (s/60|0) + 'm' : (s/3600|0) + 'h' + ((s%3600)/60|0) + 'm'; }
function esc(t) { const d = document.createElement('div'); d.textContent = t; return d.innerHTML; }

async function answer(id, approve) {
  const res = await api('/api/answer', {method:'POST', body: JSON.stringify({session_id:id, approve})});
  flash(res.message);
}
async function toggle(id) {
  open = (open === id) ? null : id;
  refresh();
}
async function refresh() {
  let st;
  try { st = await api('/api/state'); } catch (e) { return; }
  const sdiv = document.getElementById('sessions');
  sdiv.innerHTML = '';
  for (const s of st.sessions) {
    const card = document.createElement('div');
    card.className = 'card ' + s.state;
    let btns = '';
    if (s.state === 'needs-you' && s.tmux) {
      btns = '<span class="btns"><button class="ok" onclick="event.stopPropagation();answer(\'' + s.id + '\',true)">Approve</button>' +
             '<button class="no" onclick="event.stopPropagation();answer(\'' + s.id + '\',false)">Reject</button></span>';
    }
    card.innerHTML = '<div class="row"><span class="dot"></span><span class="proj">' + esc(s.project) + '</span>' +
      '<span class="meta">' + s.state + ' · ' + age(s.age_seconds) + (s.tmux ? ' · tmux' : '') + '</span>' + btns + '</div>' +
      (s.detail ? '<div class="detail">' + esc(s.detail) + '</div>' : '') +
      '<div class="tr" id="tr-' + s.id + '"></div>';
    card.onclick = () => toggle(s.id);
    sdiv.appendChild(card);
    if (open === s.id) {
      api('/api/transcript?session_id=' + s.id).then(t => {
        const el = document.getElementById('tr-' + s.id);
        if (el) el.innerHTML = '<pre>' + t.lines.map(l => '<span class="' + l.kind + '">' + esc(l.text) + '</span>').join('\n') + '</pre>';
      });
    }
  }
  if (!st.sessions.length) sdiv.innerHTML = '<div class="meta">no live sessions</div>';
  const tdiv = document.getElementById('tasks');
  tdiv.innerHTML = '';
  for (const t of (st.tasks || []).slice(0, 20)) {
    const card = document.createElement('div');
    card.className = 'card ' + t.status;
    card.innerHTML = '<div class="row"><span class="dot"></span><span class="proj">' + esc(t.dir.split('/').pop()) + '</span>' +
      '<span class="meta">' + t.status + (t.cost_usd ? ' · $' + t.cost_usd.toFixed(3) : '') + '</span></div>' +
      '<div class="detail">' + esc(t.prompt) + '</div>' +
      (t.result ? '<div class="detail meta">→ ' + esc(t.result.slice(0, 300)) + '</div>' : '');
    tdiv.appendChild(card);
  }
  if (!(st.tasks || []).length) tdiv.innerHTML = '<div class="meta">none yet</div>';
}
document.getElementById('taskform').onsubmit = async e => {
  e.preventDefault();
  const res = await api('/api/tasks', {method:'POST', body: JSON.stringify({
    prompt: document.getElementById('prompt').value,
    dir: document.getElementById('dir').value,
    permission_mode: document.getElementById('mode').value })});
  flash(res.error ? res.error : 'queued task ' + res.id);
  if (!res.error) document.getElementById('prompt').value = '';
  refresh();
};
refresh();
setInterval(refresh, 2500);
</script>
</body>
</html>`
