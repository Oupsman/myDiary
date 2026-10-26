'use strict';
// myDiary web frontend. The diary, its key, encryption, merging and sync all
// live in the WebAssembly core (cmd/mydiary-wasm); this file is only the UI.

const $ = (id) => document.getElementById(id);

const saved = {
  get(k) { try { return localStorage.getItem(k); } catch { return null; } },
  set(k, v) { try { localStorage.setItem(k, v); } catch { /* private mode */ } },
  del(k) { try { localStorage.removeItem(k); } catch { /* private mode */ } },
};

// core calls a synchronous function of the WebAssembly module, which
// returns {v} or {e: message}.
function core(name, ...args) {
  const r = globalThis.mydiary[name](...args);
  if (r.e !== undefined) throw new Error(r.e);
  return r.v;
}
const coreJSON = (name, ...args) => JSON.parse(core(name, ...args));

const errText = (e) => (e && e.message) || String(e);

// ---- boot and unlock ---------------------------------------------------------

async function boot() {
  try {
    const go = new Go();
    const ready = new Promise((r) => addEventListener('mydiary-ready', r, { once: true }));
    let inst;
    try {
      inst = await WebAssembly.instantiateStreaming(fetch('mydiary.wasm'), go.importObject);
    } catch {
      const buf = await (await fetch('mydiary.wasm')).arrayBuffer();
      inst = await WebAssembly.instantiate(buf, go.importObject);
    }
    go.run(inst.instance);
    await ready;
  } catch (e) {
    $('loading').textContent = 'Could not load myDiary: ' + errText(e);
    return;
  }
  $('loading').hidden = true;
  $('unlock-form').hidden = false;
  $('unlock-form').addEventListener('submit', onUnlock);
  $('forget-btn').addEventListener('click', () => {
    saved.del('token');
    askToken();
  });

  const token = saved.get('token');
  if (!token) {
    askToken();
    return;
  }
  try {
    core('configure', token);
    await loadDiaries();
  } catch (e) {
    unlockError(e);
  }
}

let diariesLoaded = false;

function askToken() {
  diariesLoaded = false;
  $('token-field').hidden = false;
  $('diary-field').hidden = true;
  $('pass-field').hidden = true;
  $('forget-btn').hidden = true;
  $('unlock-btn').textContent = 'Continue';
  $('token').focus();
}

async function loadDiaries() {
  const list = JSON.parse(await globalThis.mydiary.list());
  if (list.length === 0) {
    throw new Error('No diary on the server yet. Upload one from the desktop app first.');
  }
  const sel = $('diary');
  const last = saved.get('diary');
  sel.replaceChildren(...list.map((d) => {
    const o = document.createElement('option');
    o.value = d.id;
    o.textContent = `${d.short} · ${formatSize(d.size)} · ${new Date(d.updated).toLocaleDateString()}`;
    o.selected = d.id === last;
    return o;
  }));
  diariesLoaded = true;
  $('token-field').hidden = true;
  $('diary-field').hidden = list.length === 1;
  $('pass-field').hidden = false;
  $('forget-btn').hidden = false;
  $('unlock-btn').textContent = 'Unlock';
  $('pass').focus();
}

function formatSize(n) {
  if (n < 1 << 20) return Math.max(1, Math.round(n / 1024)) + ' KB';
  return (n / (1 << 20)).toFixed(1) + ' MB';
}

function unlockError(e) {
  $('unlock-error').textContent = errText(e);
  if (/device token/.test(errText(e))) {
    saved.del('token');
    askToken();
  }
}

async function onUnlock(ev) {
  ev.preventDefault();
  const btn = $('unlock-btn');
  $('unlock-error').textContent = '';
  btn.disabled = true;
  try {
    if (!diariesLoaded) {
      const token = $('token').value.trim();
      if (!token) return;
      core('configure', token);
      await loadDiaries();
      saved.set('token', token);
      $('token').value = '';
      return;
    }
    btn.textContent = 'Unlocking…';
    const id = $('diary').value;
    await globalThis.mydiary.unlock(id, $('pass').value);
    $('pass').value = '';
    saved.set('diary', id);
    enterApp();
  } catch (e) {
    unlockError(e);
    if (diariesLoaded) btn.textContent = 'Unlock';
  } finally {
    btn.disabled = false;
  }
}

// ---- main screen -----------------------------------------------------------

let currentId = null;   // entry shown in the editor
let entryDirty = false; // editor content not yet given to the core
let loadedModified = null;
let moods = [];
const imageURLs = new Map();

function enterApp() {
  moods = coreJSON('moods');
  const sel = $('entry-mood');
  sel.replaceChildren(option('', 'No mood'), ...moods.map((m) => option(m.key, `${m.emoji}  ${m.label}`)));

  $('diary-title').textContent = core('title') || 'myDiary';
  document.title = $('diary-title').textContent + ' — myDiary';
  $('unlock').hidden = true;
  $('app').hidden = false;

  $('search').addEventListener('input', debounce(renderList, 150));
  $('new-btn').addEventListener('click', newEntry);
  $('back-btn').addEventListener('click', () => history.state?.entry ? history.back() : showList());
  $('delete-btn').addEventListener('click', deleteEntry);
  $('lock-btn').addEventListener('click', lock);
  $('sync-status').addEventListener('click', () => { syncPaused = false; syncNow(); });
  $('entry-title').addEventListener('input', edited);
  $('entry-mood').addEventListener('change', () => { edited(); commit(); });
  setupEditor();

  addEventListener('popstate', () => { if (!history.state?.entry) showList(); });
  document.addEventListener('visibilitychange', () => syncNow());
  addEventListener('pagehide', commit);
  addEventListener('beforeunload', (e) => {
    commit();
    if (core('pending')) e.preventDefault();
  });
  setInterval(() => { if (!syncPaused) syncNow(); }, 120000);

  renderList();
  showEntry(null);
  updateStatus();
}

function option(value, text) {
  const o = document.createElement('option');
  o.value = value;
  o.textContent = text;
  return o;
}

function debounce(f, ms) {
  let t;
  return (...a) => { clearTimeout(t); t = setTimeout(() => f(...a), ms); };
}

function moodOf(key) {
  return moods.find((m) => m.key === key);
}

function renderList() {
  const items = coreJSON('entries', $('search').value);
  $('no-entries').hidden = items.length > 0 || $('search').value !== '';
  $('entries').replaceChildren(...items.map((e) => {
    const li = document.createElement('li');
    li.dataset.id = e.id;
    li.tabIndex = 0;
    li.classList.toggle('selected', e.id === currentId);
    const head = div('row-head');
    head.append(span('row-title', e.title.trim() || 'Untitled'));
    const m = moodOf(e.mood);
    if (m) {
      const ms = span('row-mood', m.emoji);
      ms.title = m.label;
      head.append(ms);
    }
    li.append(head, div('row-date dim', new Date(e.created).toLocaleDateString(undefined,
      { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric' })));
    if (e.snippet) li.append(div('row-snippet dim', e.snippet));
    li.addEventListener('click', () => openEntry(e.id));
    li.addEventListener('keydown', (ev) => { if (ev.key === 'Enter') openEntry(e.id); });
    return li;
  }));
}

function div(cls, text) {
  const d = document.createElement('div');
  d.className = cls;
  if (text !== undefined) d.textContent = text;
  return d;
}

function span(cls, text) {
  const s = document.createElement('span');
  s.className = cls;
  s.textContent = text;
  return s;
}

function openEntry(id) {
  commit();
  showEntry(id ? coreJSON('entry', id) : null);
  if (currentId) {
    document.body.classList.add('show-entry');
    if (!history.state?.entry) history.pushState({ entry: true }, '');
  }
}

function showList() {
  commit();
  document.body.classList.remove('show-entry');
  renderList();
}

function showEntry(e) {
  currentId = e ? e.id : null;
  for (const li of $('entries').children) li.classList.toggle('selected', li.dataset.id === currentId);
  $('entry').hidden = !e;
  $('entry-empty').hidden = !!e;
  $('delete-btn').hidden = !e;
  if (!e) return;

  $('entry-title').value = e.title;
  const sel = $('entry-mood');
  sel.querySelector('option[data-unknown]')?.remove();
  if (e.mood && !moodOf(e.mood)) {
    // Written by a newer version: keep it rather than clear it.
    const o = option(e.mood, 'Other mood');
    o.dataset.unknown = '';
    sel.append(o);
  }
  sel.value = e.mood || '';
  $('entry-date').textContent = new Date(e.created).toLocaleString(undefined,
    { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric', hour: '2-digit', minute: '2-digit' });
  renderRuns(e.content || []);
  entryDirty = false;
  loadedModified = e.modified;
}

function edited() {
  if (!currentId) return;
  entryDirty = true;
  setStatus('Unsaved', 'warn');
  commitSoon();
}

const commitSoon = debounce(() => { commit(); syncNow(); }, 2000);

// commit hands the editor content to the core, which records it only if
// something changed.
function commit() {
  if (!currentId || !entryDirty) return;
  entryDirty = false;
  const changed = core('saveEntry', JSON.stringify({
    id: currentId,
    title: $('entry-title').value,
    mood: $('entry-mood').value,
    content: serialize($('editor')),
  }));
  if (changed) {
    loadedModified = coreJSON('entry', currentId)?.modified;
    renderList();
  }
  updateStatus();
}

function newEntry() {
  commit();
  const id = core('newEntry');
  $('search').value = '';
  renderList();
  openEntry(id);
  $('editor').focus();
  syncSoon();
}

function deleteEntry() {
  if (!currentId) return;
  const title = $('entry-title').value.trim() || 'this entry';
  if (!confirm(`Delete “${title}”?`)) return;
  core('deleteEntry', currentId);
  entryDirty = false;
  showEntry(null);
  if (history.state?.entry) history.back(); else showList();
  renderList();
  syncSoon();
}

async function lock() {
  commit();
  if (core('pending')) await syncNow();
  if (core('pending') && !confirm('Some changes are not on the server yet and will be lost. Lock anyway?')) return;
  core('lock');
  location.reload();
}

// ---- sync ------------------------------------------------------------------

let syncing = false;
let syncAgain = false;
let syncPaused = false; // waiting for the user (new passphrase)
let retryTimer = null;
const syncSoon = debounce(() => syncNow(), 500);

async function syncNow() {
  commit();
  if (syncPaused) return;
  if (syncing) {
    syncAgain = true;
    return;
  }
  syncing = true;
  clearTimeout(retryTimer);
  setStatus('Syncing…', 'busy');
  try {
    const r = JSON.parse(await globalThis.mydiary.sync());
    if (r.status === 'passphrase') {
      syncAgain = await askNewPassphrase();
      syncPaused = !syncAgain;
      // rekey merged the server copy already: the next sync sees no change.
      if (syncAgain) applyRemote();
    } else if (r.changed) {
      applyRemote();
    }
    updateStatus();
  } catch (e) {
    setStatus('Not synced', 'error', errText(e) + ' — tap to retry');
    retryTimer = setTimeout(syncNow, 30000);
  } finally {
    syncing = false;
    if (syncAgain) {
      syncAgain = false;
      syncNow();
    }
  }
}

// applyRemote shows changes merged from the server.
function applyRemote() {
  renderList();
  if (!currentId) return;
  const e = coreJSON('entry', currentId);
  if (!e) {
    showEntry(null);
    if (history.state?.entry) history.back();
  } else if (!entryDirty && e.modified !== loadedModified) {
    showEntry(e);
  }
}

function updateStatus() {
  if (syncPaused) {
    setStatus('Sync paused', 'error', 'The passphrase was changed on another computer — tap to enter it');
  } else if (entryDirty) {
    setStatus('Unsaved', 'warn');
  } else if (core('pending')) {
    setStatus('Not synced', 'warn');
  } else {
    setStatus('Synced', 'ok');
  }
}

function setStatus(text, kind, title) {
  const s = $('sync-status');
  s.textContent = text;
  s.dataset.kind = kind;
  s.title = title || '';
}

// askNewPassphrase asks for the passphrase set on another computer and
// adopts it. It resolves to true when sync can go on.
function askNewPassphrase() {
  return new Promise((resolve) => {
    const dlg = document.createElement('dialog');
    dlg.className = 'dialog';
    const form = document.createElement('form');
    const p = document.createElement('p');
    p.textContent = 'The passphrase of this diary was changed on another computer. Enter the new one to keep syncing.';
    const input = document.createElement('input');
    input.type = 'password';
    input.autocomplete = 'current-password';
    const err = div('error');
    const ok = document.createElement('button');
    ok.type = 'submit';
    ok.className = 'primary';
    ok.textContent = 'Unlock';
    const cancel = document.createElement('button');
    cancel.type = 'button';
    cancel.textContent = 'Later';
    const buttons = div('buttons');
    buttons.append(cancel, ok);
    form.append(p, input, err, buttons);
    dlg.append(form);
    document.body.append(dlg);

    const done = (v) => { dlg.close(); dlg.remove(); resolve(v); };
    cancel.addEventListener('click', () => done(false));
    dlg.addEventListener('cancel', () => done(false));
    form.addEventListener('submit', async (ev) => {
      ev.preventDefault();
      ok.disabled = true;
      ok.textContent = 'Unlocking…';
      try {
        await globalThis.mydiary.rekey(input.value);
        done(true);
      } catch (e) {
        err.textContent = errText(e);
        ok.disabled = false;
        ok.textContent = 'Unlock';
      }
    });
    dlg.showModal();
    input.focus();
  });
}

// ---- editor ----------------------------------------------------------------
//
// An entry is a list of runs: {t: text, f: font, s: size in pt, b, i, u} or
// {img: picture ID}. Line breaks are "\n" in the text. Fonts and sizes set by
// the desktop app are kept on <span data-font data-size> so they survive an
// edit made here.

const BLOCKS = new Set(['DIV', 'P', 'LI', 'UL', 'OL', 'BLOCKQUOTE', 'H1', 'H2', 'H3', 'H4', 'H5', 'H6', 'PRE']);

function setupEditor() {
  const ed = $('editor');
  try { document.execCommand('styleWithCSS', false, false); } catch { /* unsupported */ }
  ed.addEventListener('input', edited);
  // Only plain text comes in: foreign HTML, styles and files are dropped.
  ed.addEventListener('paste', (e) => {
    e.preventDefault();
    document.execCommand('insertText', false, e.clipboardData.getData('text/plain'));
  });
  ed.addEventListener('drop', (e) => e.preventDefault());

  for (const b of document.querySelectorAll('.toolbar button')) {
    // Keep the selection and the keyboard while tapping a button.
    b.addEventListener('pointerdown', (e) => e.preventDefault());
    b.addEventListener('click', () => {
      document.execCommand(b.dataset.cmd);
      updateToolbar();
    });
  }
  document.addEventListener('selectionchange', updateToolbar);
}

function updateToolbar() {
  for (const b of document.querySelectorAll('.toolbar button')) {
    let on = false;
    try { on = document.queryCommandState(b.dataset.cmd); } catch { /* unsupported */ }
    b.classList.toggle('active', on);
  }
}

function renderRuns(runs) {
  const frag = document.createDocumentFragment();
  for (const r of runs) {
    if (r.img) {
      frag.append(imageElement(r.img));
      continue;
    }
    (r.t || '').split('\n').forEach((part, i) => {
      if (i > 0) frag.append(document.createElement('br'));
      if (part) frag.append(styled(part, r));
    });
  }
  $('editor').replaceChildren(frag);
}

function styled(text, r) {
  let n = document.createTextNode(text);
  const wrap = (tag) => {
    const el = document.createElement(tag);
    el.append(n);
    n = el;
  };
  if (r.u) wrap('u');
  if (r.i) wrap('i');
  if (r.b) wrap('b');
  if (r.f || r.s) {
    wrap('span');
    if (r.f) {
      n.dataset.font = r.f;
      n.style.fontFamily = JSON.stringify(r.f);
    }
    if (r.s) {
      n.dataset.size = r.s;
      n.style.fontSize = r.s + 'pt';
    }
  }
  return n;
}

function imageElement(id) {
  const img = document.createElement('img');
  img.dataset.img = id;
  img.contentEditable = 'false';
  img.alt = '';
  let url = imageURLs.get(id);
  if (!url) {
    const data = core('image', id);
    if (data) {
      url = URL.createObjectURL(new Blob([data.data], { type: data.mime }));
      imageURLs.set(id, url);
    }
  }
  if (url) img.src = url;
  return img;
}

function sameStyle(a, b) {
  return (a.f || '') === (b.f || '') && (a.s || 0) === (b.s || 0) &&
    !!a.b === !!b.b && !!a.i === !!b.i && !!a.u === !!b.u;
}

function serialize(root) {
  const runs = [];
  let pendingBreak = false; // a block ended: the next content starts a line
  const last = () => runs[runs.length - 1];
  const endsWithNL = () => !!last() && !last().img && last().t.endsWith('\n');
  const add = (t, st) => {
    const l = last();
    if (l && !l.img && sameStyle(l, st)) l.t += t;
    else runs.push({ t, ...st });
  };
  const breakLine = (st) => {
    if (pendingBreak && runs.length && !endsWithNL()) add('\n', st);
    pendingBreak = false;
  };

  const walk = (node, st) => {
    for (const n of node.childNodes) {
      if (n.nodeType === Node.TEXT_NODE) {
        if (n.data) {
          breakLine(st);
          add(n.data.replace(/ /g, ' '), st);
        }
        continue;
      }
      if (n.nodeType !== Node.ELEMENT_NODE) continue;
      if (n.tagName === 'BR') {
        breakLine(st);
        add('\n', st);
      } else if (n.tagName === 'IMG') {
        if (n.dataset.img) {
          breakLine(st);
          runs.push({ img: n.dataset.img });
        }
      } else if (BLOCKS.has(n.tagName)) {
        if (runs.length && !endsWithNL()) add('\n', st);
        pendingBreak = false;
        walk(n, styleOf(n, st));
        pendingBreak = true;
      } else {
        walk(n, styleOf(n, st));
      }
    }
  };
  walk(root, {});
  return runs;
}

// styleOf returns the style inside el, given the style around it: the
// innermost element that says something wins.
function styleOf(el, st) {
  const s = { ...st };
  const tag = el.tagName;
  if (tag === 'B' || tag === 'STRONG') s.b = true;
  if (tag === 'I' || tag === 'EM') s.i = true;
  if (tag === 'U') s.u = true;
  const css = el.style;
  if (css.fontWeight) s.b = css.fontWeight === 'bold' || css.fontWeight === 'bolder' || Number(css.fontWeight) >= 600;
  if (css.fontStyle) s.i = css.fontStyle === 'italic' || css.fontStyle === 'oblique';
  if ((css.textDecorationLine || css.textDecoration || '').includes('underline')) s.u = true;
  if (el.dataset.font) s.f = el.dataset.font;
  if (el.dataset.size) s.s = Number(el.dataset.size);
  for (const k of ['b', 'i', 'u']) if (!s[k]) delete s[k];
  return s;
}

boot();
