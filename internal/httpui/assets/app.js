// udisend browser-side runtime — Telegram-style UI.
//
// Architecture:
//   - SSE (/api/events) delivers Go-side pushes (incoming sessions,
//     signaling traffic from peers, errors).
//   - REST endpoints under /api/* drive contact / history / session
//     management.
//   - WebRTC PeerConnection lives in this script. Each chat peer gets one
//     RTCPeerConnection with a DataChannel for text/files; calls add
//     audio+video tracks via renegotiation.
//   - Call UX is Telegram-style: a single global modal in #call-root
//     drives outgoing / incoming / active states and floating /
//     fullscreen / minimized layouts.

const TOKEN = new URLSearchParams(location.search).get('token') || sessionStorage.getItem('udisend_token');
if (TOKEN) sessionStorage.setItem('udisend_token', TOKEN);

const STATE = {
  identity: null,
  contacts: [],
  selectedHash: null,
  // peer hash → connection wrapper
  peers: new Map(),
  // peer hash → { lastBody, lastDirection, lastTs, unread }
  preview: new Map(),
  // active call info or null
  call: null,
  searchQuery: '',
  iceServers: null,
};

const els = {
  app:           document.getElementById('app'),
  myAlias:       document.getElementById('my-alias'),
  myHash:        document.getElementById('my-hash'),
  myFp:          document.getElementById('my-fingerprint'),
  myAddr:        document.getElementById('my-address'),
  myAvatar:      document.getElementById('my-avatar'),
  connStatus:    document.getElementById('conn-status'),

  contacts:      document.getElementById('contacts'),
  contactsEmpty: document.getElementById('chat-list-empty'),
  addContact:    document.getElementById('add-contact-btn'),

  searchInput:   document.getElementById('search-input'),
  searchClear:   document.getElementById('search-clear'),

  chatPane:      document.getElementById('chat-pane'),
  emptyState:    document.getElementById('empty-state'),
  chatPeerBtn:   document.getElementById('chat-peer-btn'),
  chatPeerAvatar:document.getElementById('chat-peer-avatar'),
  chatPeerAlias: document.getElementById('chat-peer-alias'),
  chatPeerStatus:document.getElementById('chat-peer-status'),
  chatPeerHash:  document.getElementById('chat-peer-hash'),
  chatBack:      document.getElementById('chat-back'),
  history:       document.getElementById('chat-history'),
  msgInput:      document.getElementById('msg-input'),
  sendBtn:       document.getElementById('send-btn'),
  fileInput:     document.getElementById('file-input'),
  emojiBtn:      document.getElementById('emoji-btn'),
  callBtn:       document.getElementById('call-btn'),
  verifyBtn:     document.getElementById('verify-btn'),
  deleteBtn:     document.getElementById('delete-btn'),

  modalRoot:     document.getElementById('modal-root'),
  toastRoot:     document.getElementById('toast-root'),
  themeToggle:   document.getElementById('theme-toggle'),
  logoutBtn:     document.getElementById('logout-btn'),
  menuBtn:       document.getElementById('menu-btn'),

  callRoot:      document.getElementById('call-root'),
  callPeerName:  document.getElementById('call-peer-name'),
  callPeerStatus:document.getElementById('call-peer-status'),
  callPrering:   document.getElementById('call-prering'),
  callPreLabel:  document.getElementById('call-prering-label'),
  callRemote:    document.getElementById('call-remote-video'),
  callLocal:     document.getElementById('call-local-video'),
  callPip:       document.getElementById('call-pip-restore'),
  callPipVideo:  document.getElementById('call-pip-video'),
  callMinimize:  document.getElementById('call-minimize'),
  callFullscreen:document.getElementById('call-fullscreen'),
  callAccept:    document.getElementById('call-accept'),
  callDecline:   document.getElementById('call-decline'),
  callCancel:    document.getElementById('call-cancel'),
  callHangup:    document.getElementById('call-hangup'),
  callMute:      document.getElementById('call-mute'),
  callCam:       document.getElementById('call-cam'),
};

// ──────────────────────────────────────────────────────────────────
// HTTP helpers
// ──────────────────────────────────────────────────────────────────
async function api(path, opts = {}) {
  const headers = Object.assign({}, opts.headers || {}, {
    'X-Requested-With': 'udisend',
  });
  // Bearer token only in loopback mode. In public mode auth is via the
  // udisend_session cookie set by /login; sending a bogus Bearer header
  // ("Bearer null") would mask future auth-stack regressions.
  if (TOKEN) {
    headers['Authorization'] = 'Bearer ' + TOKEN;
  }
  if (opts.body && typeof opts.body === 'object' && !(opts.body instanceof FormData)) {
    headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(opts.body);
  }
  const r = await fetch(path, { ...opts, headers, credentials: 'same-origin' });
  if (!r.ok) {
    const t = await r.text();
    throw new Error(`${path}: ${r.status} ${t}`);
  }
  if (r.status === 204) return null;
  const ct = r.headers.get('content-type') || '';
  if (ct.includes('application/json')) return r.json();
  return r.text();
}

// ──────────────────────────────────────────────────────────────────
// Boot
// ──────────────────────────────────────────────────────────────────
async function boot() {
  applyStoredTheme();
  // Probe the snapshot endpoint first. In loopback mode it carries the
  // URL token via api(); in public mode it carries the session cookie.
  // A 401 here means the user lost their session — bounce to /login;
  // a missing token in loopback mode falls into the same handler since
  // requireAuth there returns 401 for missing/invalid tokens.
  try {
    await loadSnapshot();
  } catch (err) {
    if (String(err && err.message).includes('401')) {
      window.location.href = '/login';
      return;
    }
    throw err;
  }
  startEventStream();
  attachUIHandlers();
}
boot().catch(err => {
  console.error('boot', err);
  toast('Failed to load: ' + err.message, 'error');
});

function applyStoredTheme() {
  const stored = localStorage.getItem('udisend_theme');
  if (stored === 'light') {
    document.body.classList.remove('theme-dark');
    document.body.classList.add('theme-light');
  } else {
    document.body.classList.remove('theme-light');
    document.body.classList.add('theme-dark');
  }
}
function toggleTheme() {
  const isLight = document.body.classList.contains('theme-light');
  if (isLight) {
    document.body.classList.replace('theme-light', 'theme-dark');
    localStorage.setItem('udisend_theme', 'dark');
  } else {
    document.body.classList.replace('theme-dark', 'theme-light');
    localStorage.setItem('udisend_theme', 'light');
  }
}

// ──────────────────────────────────────────────────────────────────
// Snapshot + contact list
// ──────────────────────────────────────────────────────────────────
// signOut posts to /logout (cookie cleared server-side) and reloads. The
// reload lands on /login because the cookie no longer satisfies
// requireAuth on the SPA root.
async function signOut() {
  try {
    await fetch('/logout', {
      method: 'POST',
      credentials: 'include',
      headers: { 'X-Requested-With': 'udisend' },
      redirect: 'manual',
    });
  } catch (e) {
    console.warn('logout fetch failed', e);
  }
  window.location.href = '/login';
}

async function loadSnapshot() {
  const snap = await api('/api/snapshot');
  STATE.identity = snap.identity;
  STATE.contacts = snap.contacts;
  STATE.authMode = snap.auth_mode || 'loopback';
  STATE.iceServers = (snap.ice_servers && snap.ice_servers.length)
    ? snap.ice_servers
    : ICE_SERVERS_FALLBACK;
  els.myHash.textContent = snap.identity.hash;
  els.myFp.textContent = formatFingerprint(snap.identity.fingerprint);
  els.myAddr.textContent = snap.identity.address;
  els.myAlias.textContent = 'You';
  paintAvatar(els.myAvatar, 'You', snap.identity.hash, 'sm');
  if (els.logoutBtn) {
    els.logoutBtn.hidden = STATE.authMode !== 'public';
  }
  await preloadPreviews();
  renderContacts();
}

async function preloadPreviews() {
  const tasks = STATE.contacts.map(async c => {
    try {
      const items = await api(`/api/history?peer=${encodeURIComponent(c.hash)}&limit=1`);
      if (Array.isArray(items) && items.length) {
        const last = items[items.length - 1];
        STATE.preview.set(c.hash, {
          lastBody: previewText(last),
          lastDirection: last.direction,
          lastTs: last.timestamp / 1e6,
          unread: 0,
        });
      }
    } catch { /* non-fatal */ }
  });
  await Promise.all(tasks);
}

function previewText(entry) {
  if (entry.kind === 100) return '📎 ' + (entry.body || 'File');
  if (entry.kind === 0)   return entry.body;
  return entry.body;
}

function renderContacts() {
  els.contacts.innerHTML = '';
  const q = STATE.searchQuery.trim().toLowerCase();
  const list = STATE.contacts
    .filter(c => {
      if (!q) return true;
      const alias = (c.alias || '').toLowerCase();
      const hash = (c.hash || '').toLowerCase();
      return alias.includes(q) || hash.includes(q);
    })
    .sort((a, b) => {
      const pa = STATE.preview.get(a.hash);
      const pb = STATE.preview.get(b.hash);
      const ta = pa?.lastTs || 0;
      const tb = pb?.lastTs || 0;
      return tb - ta;
    });

  for (const c of list) {
    const li = document.createElement('li');
    li.className = 'chat-list-item';
    li.dataset.hash = c.hash;
    if (c.hash === STATE.selectedHash) li.classList.add('active');

    const avatar = document.createElement('div');
    paintAvatar(avatar, c.alias || c.hash, c.hash);

    const body = document.createElement('div');
    body.style.minWidth = '0';

    const nameRow = document.createElement('div');
    nameRow.className = 'name-row';
    const nameEl = document.createElement('div');
    nameEl.className = 'name';
    nameEl.append(document.createTextNode(c.alias || abbrev(c.hash)));
    if (c.verified) {
      nameEl.appendChild(verifiedIcon());
    }
    const timeEl = document.createElement('div');
    timeEl.className = 'time';
    const preview = STATE.preview.get(c.hash);
    timeEl.textContent = preview?.lastTs ? formatChatListTime(new Date(preview.lastTs)) : '';
    nameRow.append(nameEl, timeEl);

    const metaRow = document.createElement('div');
    metaRow.className = 'meta-row';
    const previewEl = document.createElement('div');
    previewEl.className = 'preview';
    if (preview?.lastBody) {
      if (preview.lastDirection === 'out') {
        const prefix = document.createElement('span');
        prefix.className = 'preview-prefix';
        prefix.textContent = 'You: ';
        previewEl.append(prefix, document.createTextNode(preview.lastBody));
      } else {
        previewEl.textContent = preview.lastBody;
      }
    } else {
      previewEl.textContent = c.fingerprint ? `safety #${formatFingerprint(c.fingerprint).slice(0, 12)}…` : '—';
      previewEl.style.color = 'var(--color-text-tertiary)';
    }
    metaRow.append(previewEl);
    if (preview?.unread) {
      const badge = document.createElement('div');
      badge.className = 'badge';
      badge.textContent = preview.unread > 99 ? '99+' : preview.unread;
      metaRow.append(badge);
    }

    body.append(nameRow, metaRow);

    const presence = document.createElement('span');
    presence.className = 'presence-pin';
    presence.dataset.state = peerPresenceState(c.hash);

    li.append(avatar, body, presence);
    li.addEventListener('click', () => selectContact(c.hash));
    els.contacts.appendChild(li);
  }

  els.contactsEmpty.hidden = STATE.contacts.length > 0;
}

function peerPresenceState(hash) {
  const peer = STATE.peers.get(hash);
  if (!peer) return 'offline';
  if (peer.state === 'open') return 'online';
  if (peer.state === 'connecting') return 'connecting';
  if (peer.state === 'failed') return 'failed';
  return 'offline';
}

function selectContact(hash) {
  STATE.selectedHash = hash;
  const c = STATE.contacts.find(x => x.hash === hash);
  if (!c) return;

  // Reset unread for the opened chat.
  const p = STATE.preview.get(hash);
  if (p && p.unread) { p.unread = 0; }

  els.emptyState.hidden = true;
  els.chatPane.hidden = false;
  els.app.dataset.view = 'chat';

  paintAvatar(els.chatPeerAvatar, c.alias || hash, hash, 'md');
  els.chatPeerAlias.textContent = c.alias || abbrev(hash);
  // Verified marker in header
  if (c.verified) {
    els.chatPeerAlias.appendChild(document.createTextNode(' '));
    els.chatPeerAlias.appendChild(verifiedIcon());
  }
  updatePeerStatus(hash);

  els.chatPeerHash.innerHTML = '';
  els.chatPeerHash.hidden = true;

  loadHistory(hash);
  renderContacts();
  ensurePeer(hash).catch(err => systemMsg('open session: ' + err.message));
  // Focus composer
  setTimeout(() => els.msgInput.focus(), 50);
}

function updatePeerStatus(hash) {
  const peer = STATE.peers.get(hash);
  let cls = '', text = 'offline';
  if (peer) {
    if (peer.state === 'open')        { cls = 'online';     text = 'online'; }
    else if (peer.state === 'connecting') { cls = 'connecting'; text = 'connecting…'; }
    else if (peer.state === 'failed') { cls = 'failed';     text = 'connection failed'; }
    else                              { cls = '';           text = 'offline'; }
  }
  els.chatPeerStatus.className = 'chat-peer-status ' + cls;
  els.chatPeerStatus.textContent = text;
}

async function loadHistory(hash) {
  els.history.innerHTML = '';
  const inner = document.createElement('div');
  inner.className = 'chat-history-inner';
  els.history.appendChild(inner);

  const items = await api(`/api/history?peer=${encodeURIComponent(hash)}`);
  let prevDay = null;
  let prevDir = null;
  let bubblesInGroup = [];
  const flushGroup = () => {
    if (bubblesInGroup.length === 0) return;
    if (bubblesInGroup.length === 1) {
      bubblesInGroup[0].el.classList.add('group-only');
    } else {
      bubblesInGroup.forEach((b, i) => {
        if (i === 0) b.el.classList.add('group-first');
        else if (i === bubblesInGroup.length - 1) b.el.classList.add('group-last');
        else b.el.classList.add('group-mid');
      });
    }
    bubblesInGroup = [];
  };

  for (const e of items) {
    const when = new Date(e.timestamp / 1e6);
    const day = dayKey(when);
    if (day !== prevDay) {
      flushGroup();
      const sep = document.createElement('div');
      sep.className = 'date-separator';
      sep.dataset.day = day;
      sep.textContent = formatDateSeparator(when);
      inner.appendChild(sep);
      prevDay = day;
      prevDir = null;
    }
    const dir = e.kind === 0 ? 'system' : e.direction;
    if (dir !== prevDir || e.kind === 0) flushGroup();
    const el = makeMessageEl(dir, e.body, when, e.kind);
    inner.appendChild(el);
    if (e.kind !== 0) {
      bubblesInGroup.push({ el, dir });
      prevDir = dir;
    } else {
      prevDir = 'system';
    }
  }
  flushGroup();
  scrollHistory();
}

function makeMessageEl(direction, body, when, kind) {
  const div = document.createElement('div');
  if (kind === 0 || direction === 'system') {
    div.className = 'msg system';
    div.textContent = body;
    return div;
  }
  if (kind === 100) {
    div.className = 'msg file ' + direction;
    const row = document.createElement('div');
    row.className = 'file-row';
    const icon = document.createElement('div');
    icon.className = 'file-icon';
    icon.textContent = '📎';
    const meta = document.createElement('div');
    const name = document.createElement('div');
    name.textContent = body;
    const sub = document.createElement('div');
    sub.className = 'file-meta';
    sub.textContent = formatTime(when);
    meta.append(name, sub);
    row.append(icon, meta);
    div.append(row);
    return div;
  }
  div.className = 'msg ' + direction;
  const bodyEl = document.createElement('span');
  bodyEl.className = 'body';
  bodyEl.textContent = body;
  const meta = document.createElement('span');
  meta.className = 'meta';
  meta.append(document.createTextNode(formatTime(when)));
  if (direction === 'out') {
    meta.appendChild(checkmarkIcon());
  }
  div.append(bodyEl, meta);
  return div;
}

function appendOutgoingMessage(body, when, kind) {
  return appendLiveMessage('out', body, when, kind);
}
function appendIncomingMessage(body, when, kind) {
  return appendLiveMessage('in', body, when, kind);
}

function appendLiveMessage(direction, body, when, kind) {
  const inner = ensureHistoryInner();
  const lastMsg = inner.querySelector('.msg:last-child');
  const lastDir = lastMsg?.classList.contains('out') ? 'out'
                : lastMsg?.classList.contains('in') ? 'in'
                : lastMsg?.classList.contains('system') ? 'system' : null;
  // Date separator: derive current day cursor from the last message in DOM
  // (more robust than a dataset flag against races with async loadHistory).
  const day = dayKey(when);
  const allChildren = inner.children;
  let lastDayInDom = null;
  for (let i = allChildren.length - 1; i >= 0; i--) {
    const c = allChildren[i];
    if (c.dataset && c.dataset.day) { lastDayInDom = c.dataset.day; break; }
  }
  if (day !== lastDayInDom) {
    const sep = document.createElement('div');
    sep.className = 'date-separator';
    sep.dataset.day = day;
    sep.textContent = formatDateSeparator(when);
    inner.appendChild(sep);
  }

  const el = makeMessageEl(direction, body, when, kind);

  // Update grouping: if last bubble matches direction, demote it to group-first/mid then append as last.
  if (kind !== 0 && lastDir === direction && lastMsg) {
    if (lastMsg.classList.contains('group-only')) {
      lastMsg.classList.remove('group-only');
      lastMsg.classList.add('group-first');
    } else if (lastMsg.classList.contains('group-last')) {
      lastMsg.classList.remove('group-last');
      lastMsg.classList.add('group-mid');
    }
    el.classList.add('group-last');
  } else if (kind !== 0) {
    el.classList.add('group-only');
  }

  inner.appendChild(el);
  scrollHistory();
  return el;
}

function ensureHistoryInner() {
  let inner = els.history.querySelector('.chat-history-inner');
  if (!inner) {
    inner = document.createElement('div');
    inner.className = 'chat-history-inner';
    els.history.appendChild(inner);
  }
  return inner;
}

function systemMsg(text) {
  const inner = ensureHistoryInner();
  const div = document.createElement('div');
  div.className = 'msg system';
  div.textContent = text;
  inner.appendChild(div);
  scrollHistory();
}

function scrollHistory() {
  requestAnimationFrame(() => {
    els.history.scrollTop = els.history.scrollHeight;
  });
}

function updatePreview(hash, body, direction, ts, isUnreadIncrement) {
  const cur = STATE.preview.get(hash) || { unread: 0 };
  cur.lastBody = body;
  cur.lastDirection = direction;
  cur.lastTs = ts;
  if (isUnreadIncrement) cur.unread = (cur.unread || 0) + 1;
  STATE.preview.set(hash, cur);
  renderContacts();
}

// ──────────────────────────────────────────────────────────────────
// SSE events from Go
// ──────────────────────────────────────────────────────────────────
function startEventStream() {
  // Public mode authenticates via the session cookie (browsers always send
  // cookies on EventSource); loopback mode needs ?token= because the SSE
  // handler accepts it as the auth-token query param.
  const eventsURL = TOKEN
    ? '/api/events?token=' + encodeURIComponent(TOKEN)
    : '/api/events';
  const es = new EventSource(eventsURL, { withCredentials: true });
  es.onopen = () => {
    els.connStatus.dataset.state = 'connected';
    els.connStatus.title = 'connected';
  };
  es.onerror = () => {
    els.connStatus.dataset.state = 'disconnected';
    els.connStatus.title = 'reconnecting…';
  };
  es.onmessage = ev => {
    let env;
    try { env = JSON.parse(ev.data); }
    catch { console.warn('bad event', ev.data); return; }
    handleServerEvent(env);
  };
}

function handleServerEvent(env) {
  switch (env.type) {
    case 'incoming_session':
      handleIncomingSession(env.peer, env.session_id);
      break;
    case 'signal_recv':
      handleSignalRecv(env.peer, env.session_id, env.kind, env.payload);
      break;
    case 'session_closed':
      onSessionClosed(env.peer, env.session_id);
      break;
    case 'peer_online':
      // No-op for UI; server may use this to drain outbox.
      break;
    case 'error':
      toast('server error: ' + env.error, 'error');
      break;
    default:
      console.log('event', env);
  }
}

// ──────────────────────────────────────────────────────────────────
// Per-peer connection wrapper
// ──────────────────────────────────────────────────────────────────
const ICE_SERVERS_FALLBACK = [{ urls: 'stun:stun.l.google.com:19302' }];

async function ensurePeer(hash) {
  let p = STATE.peers.get(hash);
  if (p && (p.state === 'open' || p.state === 'connecting')) return p;
  const resp = await api('/api/session/open', { method: 'POST', body: { peer: hash } });
  return setupPeer(hash, resp.session_id, 'initiator');
}

function setupPeer(hash, sessionId, role) {
  const existing = STATE.peers.get(hash);
  if (existing) { try { existing.pc.close(); } catch {} }
  const pc = new RTCPeerConnection({ iceServers: STATE.iceServers || ICE_SERVERS_FALLBACK });
  const peer = {
    hash, sessionId, pc, role,
    state: 'connecting',
    dc: null,
    incomingFiles: new Map(),
    pendingCandidates: [],
    haveRemoteDesc: false,
    isMakingOffer: false,
  };
  STATE.peers.set(hash, peer);
  renderContacts();
  if (hash === STATE.selectedHash) updatePeerStatus(hash);

  pc.onicecandidate = ev => {
    if (!ev.candidate) return;
    sendSignal(peer, 'ice', JSON.stringify(ev.candidate.toJSON()));
  };
  pc.onconnectionstatechange = () => {
    if (pc.connectionState === 'connected') peer.state = 'open';
    else if (pc.connectionState === 'failed' || pc.connectionState === 'closed') peer.state = 'failed';
    renderContacts();
    if (hash === STATE.selectedHash) updatePeerStatus(hash);
    if (STATE.call && STATE.call.peer === hash) syncCallStatusFromPC(pc.connectionState);
  };
  pc.ontrack = ev => onRemoteTrack(peer, ev);
  pc.ondatachannel = ev => attachDataChannel(peer, ev.channel);

  if (role === 'initiator') {
    const dc = pc.createDataChannel('udisend', { ordered: true });
    attachDataChannel(peer, dc);
    pc.createOffer()
      .then(offer => pc.setLocalDescription(offer))
      .then(() => sendSignal(peer, 'offer', pc.localDescription.sdp))
      .catch(err => console.error('offer', err));
  }
  return peer;
}

function attachDataChannel(peer, dc) {
  peer.dc = dc;
  dc.binaryType = 'arraybuffer';
  dc.onopen = () => {
    peer.state = 'open';
    renderContacts();
    if (peer.hash === STATE.selectedHash) updatePeerStatus(peer.hash);
  };
  dc.onclose = () => {
    peer.state = 'closed';
    renderContacts();
    if (peer.hash === STATE.selectedHash) updatePeerStatus(peer.hash);
  };
  dc.onmessage = ev => onDataChannelMessage(peer, ev.data);
}

async function handleIncomingSession(peerHash, sessionId) {
  setupPeer(peerHash, sessionId, 'responder');
  // Backend has just upserted a placeholder contact for this peer
  // (EnsureContact in onIncomingSession). Re-pull the snapshot so the
  // sidebar reflects the now-persistent row instead of the stale
  // in-memory push that disappeared on reload.
  if (!STATE.contacts.find(c => c.hash === peerHash)) {
    try { await loadSnapshot(); }
    catch (e) { console.warn('snapshot after incoming session', e); }
  }
}

async function handleSignalRecv(peerHash, sessionId, kind, payload) {
  const peer = STATE.peers.get(peerHash);
  if (!peer) { console.warn('signal for unknown peer', peerHash); return; }
  try {
    if (kind === 'offer') {
      await peer.pc.setRemoteDescription({ type: 'offer', sdp: payload });
      peer.haveRemoteDesc = true;
      const ans = await peer.pc.createAnswer();
      await peer.pc.setLocalDescription(ans);
      sendSignal(peer, 'answer', peer.pc.localDescription.sdp);
      flushPending(peer);
    } else if (kind === 'answer') {
      await peer.pc.setRemoteDescription({ type: 'answer', sdp: payload });
      peer.haveRemoteDesc = true;
      flushPending(peer);
    } else if (kind === 'ice') {
      const cand = JSON.parse(payload);
      if (peer.haveRemoteDesc) await peer.pc.addIceCandidate(cand);
      else peer.pendingCandidates.push(cand);
    } else if (kind === 'bye') {
      onSessionClosed(peerHash, sessionId);
    }
  } catch (err) {
    console.error('handle signal', kind, err);
    systemMsg(`signal error (${kind}): ${err.message}`);
  }
}

async function flushPending(peer) {
  for (const c of peer.pendingCandidates) {
    try { await peer.pc.addIceCandidate(c); } catch (e) { console.warn('flush candidate', e); }
  }
  peer.pendingCandidates = [];
}

function sendSignal(peer, kind, payload) {
  return api('/api/signal/send', {
    method: 'POST',
    body: { session_id: peer.sessionId, kind, payload },
  }).catch(err => console.error('send signal', kind, err));
}

function onSessionClosed(peerHash, sessionId) {
  const peer = STATE.peers.get(peerHash);
  if (!peer || peer.sessionId !== sessionId) return;
  if (STATE.call && STATE.call.peer === peerHash) endCallLocal('peer disconnected');
  try { peer.pc.close(); } catch {}
  STATE.peers.delete(peerHash);
  renderContacts();
  if (peerHash === STATE.selectedHash) updatePeerStatus(peerHash);
}

// ──────────────────────────────────────────────────────────────────
// DataChannel application protocol
// ──────────────────────────────────────────────────────────────────
function onDataChannelMessage(peer, data) {
  if (typeof data === 'string') {
    let msg;
    try { msg = JSON.parse(data); } catch { return; }
    switch (msg.kind) {
      case 'text': {
        const when = new Date(msg.ts ? msg.ts / 1e6 : Date.now());
        if (peer.hash === STATE.selectedHash) {
          appendIncomingMessage(msg.body, when, 1);
        }
        const incrementUnread = peer.hash !== STATE.selectedHash;
        updatePreview(peer.hash, msg.body, 'in', when.getTime(), incrementUnread);
        persistHistory(peer.hash, 'in', 1, msg.body, msg.ts);
        if (incrementUnread) {
          toast(`${aliasOf(peer.hash)}: ${truncate(msg.body, 70)}`);
          maybeNotify(`${aliasOf(peer.hash)}`, truncate(msg.body, 200), peer.hash);
        }
        break;
      }
      case 'file_offer': {
        peer.incomingFiles.set(msg.id, { name: msg.name, size: msg.size, mime: msg.mime, parts: [] });
        if (peer.hash === STATE.selectedHash) {
          appendIncomingMessage(`incoming file: ${msg.name} (${humanSize(msg.size)})`, new Date(), 100);
        }
        updatePreview(peer.hash, '📎 ' + msg.name, 'in', Date.now(), peer.hash !== STATE.selectedHash);
        if (peer.hash !== STATE.selectedHash) {
          maybeNotify(`${aliasOf(peer.hash)} sent a file`, `${msg.name} · ${humanSize(msg.size)}`, peer.hash);
        }
        break;
      }
      case 'file_end': {
        const f = peer.incomingFiles.get(msg.id);
        if (!f) return;
        const blob = new Blob(f.parts, { type: f.mime || 'application/octet-stream' });
        const url = URL.createObjectURL(blob);
        if (peer.hash === STATE.selectedHash) {
          const inner = ensureHistoryInner();
          const div = document.createElement('div');
          div.className = 'msg file in group-only';
          const row = document.createElement('div');
          row.className = 'file-row';
          const icon = document.createElement('div');
          icon.className = 'file-icon';
          icon.textContent = '⬇';
          const meta = document.createElement('div');
          const a = document.createElement('a');
          a.href = url; a.download = f.name; a.textContent = f.name;
          const sub = document.createElement('div');
          sub.className = 'file-meta';
          sub.textContent = humanSize(blob.size) + ' · ' + formatTime(new Date());
          meta.append(a, sub);
          row.append(icon, meta);
          div.append(row);
          inner.appendChild(div);
          scrollHistory();
        }
        peer.incomingFiles.delete(msg.id);
        persistHistory(peer.hash, 'in', 100, `received file ${f.name}`, Date.now() * 1e6);
        break;
      }
      case 'call_invite':  onCallInvite(peer);  break;
      case 'call_accept':  onCallAccept(peer);  break;
      case 'call_reject':  onCallReject(peer);  break;
      case 'call_end':     onCallEnd(peer);     break;
    }
    return;
  }
  // Binary frame: first 16 bytes = id, rest = chunk.
  const view = new Uint8Array(data);
  const id = new TextDecoder().decode(view.slice(0, 16));
  const f = peer.incomingFiles.get(id);
  if (!f) return;
  f.parts.push(view.slice(16));
}

function dcSend(peer, obj) {
  if (!peer.dc || peer.dc.readyState !== 'open') return false;
  peer.dc.send(JSON.stringify(obj));
  return true;
}

async function sendText(peer, text) {
  if (!peer.dc || peer.dc.readyState !== 'open') {
    systemMsg('Not connected yet — message not sent.');

    return;
  }
  const id = crypto.randomUUID();
  const ts = Date.now() * 1e6;
  peer.dc.send(JSON.stringify({ kind: 'text', body: text, ts, id }));
  appendOutgoingMessage(text, new Date(ts / 1e6), 1);
  updatePreview(peer.hash, text, 'out', ts / 1e6, false);
  await persistHistory(peer.hash, 'out', 1, text, ts);
}

async function sendFile(peer, file) {
  if (!peer.dc || peer.dc.readyState !== 'open') {
    systemMsg('Not connected — file not sent.');

    return;
  }
  const id = crypto.randomUUID().replaceAll('-', '').slice(0, 16);
  peer.dc.send(JSON.stringify({ kind: 'file_offer', id, name: file.name, size: file.size, mime: file.type }));
  appendOutgoingMessage(`${file.name} (${humanSize(file.size)})`, new Date(), 100);
  updatePreview(peer.hash, '📎 ' + file.name, 'out', Date.now(), false);
  const CHUNK = 16 * 1024;
  let offset = 0;
  const idBytes = new TextEncoder().encode(id);
  while (offset < file.size) {
    const slice = await file.slice(offset, offset + CHUNK).arrayBuffer();
    const buf = new Uint8Array(idBytes.length + slice.byteLength);
    buf.set(idBytes, 0);
    buf.set(new Uint8Array(slice), idBytes.length);
    while (peer.dc.bufferedAmount > 4 * 1024 * 1024) {
      await new Promise(r => setTimeout(r, 50));
    }
    peer.dc.send(buf);
    offset += slice.byteLength;
  }
  peer.dc.send(JSON.stringify({ kind: 'file_end', id }));
  await persistHistory(peer.hash, 'out', 100, `sent file ${file.name}`, Date.now() * 1e6);
}

async function persistHistory(peerHash, direction, kind, body, ts) {
  try {
    await api('/api/append-history', {
      method: 'POST',
      body: { peer: peerHash, direction, kind, body, timestamp: ts },
    });
  } catch (e) { console.warn('persist', e); }
}

function onRemoteTrack(peer, ev) {
  if (!STATE.call || STATE.call.peer !== peer.hash) return;
  const stream = ev.streams && ev.streams[0];
  if (!stream) return;
  els.callRemote.srcObject = stream;
  els.callPipVideo.srcObject = stream;
}

// ──────────────────────────────────────────────────────────────────
// Calls
// ──────────────────────────────────────────────────────────────────
function setCallState(state, mode) {
  els.callRoot.dataset.state = state;
  if (mode) els.callRoot.dataset.mode = mode;
  if (state === 'idle') els.callRoot.dataset.mode = 'floating';
}

async function placeCall() {
  if (STATE.call) { systemMsg('Already in a call.'); return; }
  if (!STATE.selectedHash) return;
  let peer;
  try { peer = await ensurePeer(STATE.selectedHash); }
  catch (e) { systemMsg('cannot reach peer: ' + e.message); return; }
  if (!peer.dc || peer.dc.readyState !== 'open') {
    const ok = await waitFor(() => peer.dc && peer.dc.readyState === 'open', 8000);
    if (!ok) { systemMsg('peer not connected'); return; }
  }
  STATE.call = { peer: peer.hash, role: 'caller', state: 'outgoing', startedAt: Date.now(), localStream: null };
  showCallModal('outgoing', aliasOf(peer.hash), 'Calling…');
  dcSend(peer, { kind: 'call_invite' });
  STATE.call.ringTimeout = setTimeout(() => {
    if (!STATE.call || STATE.call.state !== 'outgoing') return;
    const p = STATE.peers.get(STATE.call.peer);
    if (p) dcSend(p, { kind: 'call_end' });
    endCallLocal('no answer');
  }, 45_000);
}

function onCallInvite(peer) {
  if (STATE.call) {
    dcSend(peer, { kind: 'call_reject' });

    return;
  }
  STATE.call = { peer: peer.hash, role: 'callee', state: 'incoming', startedAt: Date.now(), localStream: null };
  showCallModal('incoming', aliasOf(peer.hash), 'Incoming call');
  startRingtone();
  maybeNotify('Incoming call', `from ${aliasOf(peer.hash)}`, peer.hash, { requireInteraction: true });
}

async function acceptIncomingCall() {
  if (!STATE.call || STATE.call.role !== 'callee' || STATE.call.state !== 'incoming') return;
  stopRingtone();
  const peer = STATE.peers.get(STATE.call.peer);
  if (!peer) { endCallLocal('peer gone'); return; }
  let stream;
  try { stream = await navigator.mediaDevices.getUserMedia({ audio: true, video: true }); }
  catch (e) {
    dcSend(peer, { kind: 'call_reject' });
    endCallLocal('media access denied: ' + e.message);

    return;
  }
  STATE.call.localStream = stream;
  els.callLocal.srcObject = stream;
  for (const t of stream.getTracks()) peer.pc.addTrack(t, stream);
  dcSend(peer, { kind: 'call_accept' });
  setCallState('active', 'floating');
  STATE.call.state = 'active';
  setCallStatus('Connecting…');
  syncCallStatusFromPC(peer.pc.connectionState);
}

function declineIncomingCall() {
  if (!STATE.call || STATE.call.role !== 'callee' || STATE.call.state !== 'incoming') return;
  stopRingtone();
  const peer = STATE.peers.get(STATE.call.peer);
  if (peer) dcSend(peer, { kind: 'call_reject' });
  endCallLocal('declined');
}

async function onCallAccept(peer) {
  if (!STATE.call || STATE.call.role !== 'caller' || STATE.call.state !== 'outgoing' || STATE.call.peer !== peer.hash) return;
  if (STATE.call.ringTimeout) { clearTimeout(STATE.call.ringTimeout); STATE.call.ringTimeout = null; }
  let stream;
  try { stream = await navigator.mediaDevices.getUserMedia({ audio: true, video: true }); }
  catch (e) {
    dcSend(peer, { kind: 'call_end' });
    endCallLocal('media access denied: ' + e.message);

    return;
  }
  STATE.call.localStream = stream;
  els.callLocal.srcObject = stream;
  for (const t of stream.getTracks()) peer.pc.addTrack(t, stream);
  STATE.call.state = 'active';
  setCallState('active', 'floating');
  setCallStatus('Connecting…');
  syncCallStatusFromPC(peer.pc.connectionState);
  try {
    const offer = await peer.pc.createOffer();
    await peer.pc.setLocalDescription(offer);
    sendSignal(peer, 'offer', peer.pc.localDescription.sdp);
  } catch (e) {
    console.error('renegotiate', e);
    endCallLocal('renegotiate failed: ' + e.message);
  }
}

function onCallReject(peer) {
  if (!STATE.call || STATE.call.peer !== peer.hash) return;
  endCallLocal('declined by peer');
}

function onCallEnd(peer) {
  if (!STATE.call || STATE.call.peer !== peer.hash) return;
  endCallLocal('peer hung up');
}

function hangupActive() {
  if (!STATE.call) return;
  const peer = STATE.peers.get(STATE.call.peer);
  if (peer) dcSend(peer, { kind: 'call_end' });
  endCallLocal('ended');
}

function endCallLocal(reason) {
  stopRingtone();
  stopCallDurationTimer();
  if (STATE.call) {
    if (STATE.call.localStream) {
      for (const t of STATE.call.localStream.getTracks()) t.stop();
    }
    if (STATE.call.ringTimeout) clearTimeout(STATE.call.ringTimeout);
    const peer = STATE.peers.get(STATE.call.peer);
    if (peer) {
      for (const sender of peer.pc.getSenders()) {
        if (sender.track) {
          try { sender.track.stop(); } catch {}
          try { peer.pc.removeTrack(sender); } catch {}
        }
      }
    }
  }
  STATE.call = null;
  els.callRemote.srcObject = null;
  els.callLocal.srcObject = null;
  els.callPipVideo.srcObject = null;
  setCallState('idle');
  if (reason) systemMsg(`call: ${reason}`);
}

function showCallModal(state, peerName, statusText) {
  els.callPeerName.textContent = peerName;
  setCallStatus(statusText);
  setCallState(state, 'floating');
}

function setCallStatus(text) {
  els.callPeerStatus.textContent = text;
  els.callPreLabel.textContent = text;
}

// While the call is 'active', map RTCPeerConnection state to user-visible
// text. Once we hit 'connected' for the first time we hand off to a
// running mm:ss duration timer; transient drops show "Reconnecting…".
function syncCallStatusFromPC(connState) {
  if (!STATE.call || STATE.call.state !== 'active') return;
  switch (connState) {
    case 'connected':
      startCallDurationTimer();
      break;
    case 'disconnected':
      stopCallDurationTimer();
      setCallStatus('Reconnecting…');
      break;
    case 'failed':
    case 'closed':
      stopCallDurationTimer();
      setCallStatus('Disconnected');
      break;
    case 'connecting':
    case 'new':
      if (!STATE.call.connectedAt) setCallStatus('Connecting…');
      break;
  }
}

function startCallDurationTimer() {
  if (!STATE.call) return;
  if (!STATE.call.connectedAt) STATE.call.connectedAt = Date.now();
  if (STATE.call.durationTimer) return;
  const tick = () => {
    if (!STATE.call || !STATE.call.connectedAt) return;
    const secs = Math.floor((Date.now() - STATE.call.connectedAt) / 1000);
    const mm = String(Math.floor(secs / 60)).padStart(2, '0');
    const ss = String(secs % 60).padStart(2, '0');
    setCallStatus(`${mm}:${ss}`);
  };
  tick();
  STATE.call.durationTimer = setInterval(tick, 1000);
}

function stopCallDurationTimer() {
  if (STATE.call && STATE.call.durationTimer) {
    clearInterval(STATE.call.durationTimer);
    STATE.call.durationTimer = null;
  }
}

function toggleFullscreen() {
  if (!STATE.call || STATE.call.state !== 'active') return;
  if (els.callRoot.dataset.mode === 'fullscreen') els.callRoot.dataset.mode = 'floating';
  else els.callRoot.dataset.mode = 'fullscreen';
}

function toggleMinimize() {
  if (!STATE.call || STATE.call.state !== 'active') return;
  if (els.callRoot.dataset.mode === 'minimized') els.callRoot.dataset.mode = 'floating';
  else els.callRoot.dataset.mode = 'minimized';
}

function toggleMute() {
  if (!STATE.call || !STATE.call.localStream) return;
  const tracks = STATE.call.localStream.getAudioTracks();
  if (!tracks.length) return;
  const enabled = !tracks[0].enabled;
  for (const t of tracks) t.enabled = enabled;
  els.callMute.classList.toggle('toggled', !enabled);
}

function toggleCam() {
  if (!STATE.call || !STATE.call.localStream) return;
  const tracks = STATE.call.localStream.getVideoTracks();
  if (!tracks.length) return;
  const enabled = !tracks[0].enabled;
  for (const t of tracks) t.enabled = enabled;
  els.callCam.classList.toggle('toggled', !enabled);
}

// ──────────────────────────────────────────────────────────────────
// Ringtone
// ──────────────────────────────────────────────────────────────────
let ringAudio = null;
let ringInterval = null;

function startRingtone() {
  stopRingtone();
  try {
    const ctx = new (window.AudioContext || window.webkitAudioContext)();
    ringAudio = ctx;
    const beep = () => {
      const osc = ctx.createOscillator();
      const gain = ctx.createGain();
      osc.frequency.value = 480;
      gain.gain.setValueAtTime(0, ctx.currentTime);
      gain.gain.linearRampToValueAtTime(0.15, ctx.currentTime + 0.05);
      gain.gain.linearRampToValueAtTime(0, ctx.currentTime + 0.6);
      osc.connect(gain).connect(ctx.destination);
      osc.start();
      osc.stop(ctx.currentTime + 0.7);
    };
    beep();
    ringInterval = setInterval(beep, 1500);
  } catch (e) { /* autoplay policy may suppress */ }
}

function stopRingtone() {
  if (ringInterval) { clearInterval(ringInterval); ringInterval = null; }
  if (ringAudio) { try { ringAudio.close(); } catch {} ringAudio = null; }
}

function waitFor(predicate, timeoutMs) {
  return new Promise(resolve => {
    const start = Date.now();
    const tick = () => {
      if (predicate()) return resolve(true);
      if (Date.now() - start > timeoutMs) return resolve(false);
      setTimeout(tick, 100);
    };
    tick();
  });
}

// ──────────────────────────────────────────────────────────────────
// UI handlers
// ──────────────────────────────────────────────────────────────────
function attachUIHandlers() {
  els.addContact.addEventListener('click', showAddContactModal);
  els.themeToggle.addEventListener('click', toggleTheme);
  if (els.logoutBtn) {
    els.logoutBtn.addEventListener('click', signOut);
  }
  if (els.menuBtn) {
    els.menuBtn.addEventListener('click', showSettingsModal);
  }

  els.searchInput.addEventListener('input', () => {
    STATE.searchQuery = els.searchInput.value;
    els.searchClear.hidden = !els.searchInput.value;
    renderContacts();
  });
  els.searchClear.addEventListener('click', () => {
    els.searchInput.value = '';
    STATE.searchQuery = '';
    els.searchClear.hidden = true;
    renderContacts();
    els.searchInput.focus();
  });

  els.chatBack.addEventListener('click', () => {
    els.app.dataset.view = 'list';
  });

  els.sendBtn.addEventListener('click', () => sendCurrent());
  els.msgInput.addEventListener('keydown', e => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      sendCurrent();
    }
  });
  els.msgInput.addEventListener('input', () => {
    autosizeTextarea(els.msgInput);
    els.sendBtn.disabled = !els.msgInput.value.trim();
  });
  els.sendBtn.disabled = true;

  els.fileInput.addEventListener('change', () => {
    const file = els.fileInput.files?.[0];
    if (!file || !STATE.selectedHash) return;
    ensurePeer(STATE.selectedHash).then(p => sendFile(p, file)).catch(e => systemMsg('file: ' + e.message));
    els.fileInput.value = '';
  });

  els.callBtn.addEventListener('click', placeCall);
  els.emojiBtn.addEventListener('click', () => {
    // Lightweight emoji insertion — spawns a small popover with a few emojis.
    showEmojiPicker();
  });

  els.callAccept.addEventListener('click', acceptIncomingCall);
  els.callDecline.addEventListener('click', declineIncomingCall);
  els.callCancel.addEventListener('click', () => {
    const peer = STATE.peers.get(STATE.call?.peer);
    if (peer) dcSend(peer, { kind: 'call_end' });
    endCallLocal('cancelled');
  });
  els.callHangup.addEventListener('click', hangupActive);
  els.callMinimize.addEventListener('click', toggleMinimize);
  els.callFullscreen.addEventListener('click', toggleFullscreen);
  els.callPip.addEventListener('click', toggleMinimize);
  els.callMute.addEventListener('click', toggleMute);
  els.callCam.addEventListener('click', toggleCam);

  document.addEventListener('keydown', e => {
    if (e.key === 'Escape' && STATE.call && STATE.call.state === 'active') {
      if (els.callRoot.dataset.mode === 'fullscreen') {
        e.preventDefault();
        els.callRoot.dataset.mode = 'floating';
      }
    }
  });

  els.verifyBtn.addEventListener('click', () => {
    if (!STATE.selectedHash) return;
    const c = STATE.contacts.find(x => x.hash === STATE.selectedHash);
    if (!c) return;
    showVerifyModal(c);
  });

  els.deleteBtn.addEventListener('click', async () => {
    if (!STATE.selectedHash) return;
    const c = STATE.contacts.find(x => x.hash === STATE.selectedHash);
    if (!c) return;
    let messageCount = 0;
    try {
      const items = await api(`/api/history?peer=${encodeURIComponent(c.hash)}`);
      messageCount = Array.isArray(items) ? items.length : 0;
    } catch {}
    showDeleteContactModal(c, messageCount);
  });

  // Click peer header → toggle info panel (hash + safety + rename).
  els.chatPeerBtn.addEventListener('click', () => {
    const c = STATE.contacts.find(x => x.hash === STATE.selectedHash);
    if (!c) return;
    if (els.chatPeerHash.hidden) {
      const aliasLabel = c.alias ? escapeHTML(c.alias) : `<em class="muted-inline">no alias yet — only you decide what to call this peer</em>`;
      els.chatPeerHash.innerHTML = `
        <div class="peer-info-row"><span class="peer-info-label">Alias</span><span>${aliasLabel}</span><button class="peer-info-action" id="peer-rename">${c.alias ? 'Rename' : 'Set name'}</button></div>
        <div class="peer-info-row"><span class="peer-info-label">Hash</span><code>${escapeHTML(c.hash)}</code></div>
        <div class="peer-info-row"><span class="peer-info-label">Safety</span><code>${escapeHTML(formatFingerprint(c.fingerprint || ''))}</code></div>
      `;
      els.chatPeerHash.hidden = false;
      document.getElementById('peer-rename').addEventListener('click', e => {
        e.stopPropagation();
        showRenameModal(c);
      });
    } else {
      els.chatPeerHash.hidden = true;
    }
  });
}

function showRenameModal(contact) {
  const root = els.modalRoot;
  const isRename = !!contact.alias;
  root.innerHTML = `
    <div class="modal-backdrop">
      <div class="modal">
        <h3>${isRename ? 'Rename contact' : 'Set local name'}</h3>
        <p class="muted">This name is stored only on your device. The peer will never see it.</p>
        <label>Alias</label>
        <input id="modal-rename-alias" placeholder="${escapeHTML(abbrev(contact.hash))}" value="${escapeHTML(contact.alias || '')}" autocomplete="off" />
        <div class="modal-actions">
          <button id="modal-cancel">Cancel</button>
          <button id="modal-save" class="primary">Save</button>
        </div>
      </div>
    </div>
  `;
  document.getElementById('modal-cancel').onclick = () => (root.innerHTML = '');
  const save = async () => {
    const alias = document.getElementById('modal-rename-alias').value.trim();
    try {
      await api('/api/contacts/rename', { method: 'POST', body: { hash: contact.hash, alias } });
      root.innerHTML = '';
      await loadSnapshot();
      // Re-render the chat header with the updated alias.
      if (STATE.selectedHash === contact.hash) {
        els.chatPeerHash.hidden = true;
        selectContact(contact.hash);
      }
      toast(alias ? `Renamed to ${alias}` : 'Alias cleared');
    } catch (e) { toast(e.message, 'error'); }
  };
  document.getElementById('modal-save').onclick = save;
  const input = document.getElementById('modal-rename-alias');
  input.addEventListener('keydown', e => { if (e.key === 'Enter') save(); });
  setTimeout(() => { input.focus(); input.select(); }, 50);
}

function sendCurrent() {
  const text = els.msgInput.value.trim();
  if (!text || !STATE.selectedHash) return;
  els.msgInput.value = '';
  autosizeTextarea(els.msgInput);
  els.sendBtn.disabled = true;
  ensurePeer(STATE.selectedHash)
    .then(p => sendText(p, text))
    .catch(e => systemMsg('send: ' + e.message));
}

function autosizeTextarea(ta) {
  ta.style.height = 'auto';
  ta.style.height = Math.min(ta.scrollHeight, 180) + 'px';
}

function showEmojiPicker() {
  const picker = document.createElement('div');
  picker.style.cssText = `
    position: absolute; bottom: 64px; right: 70px; z-index: 50;
    background: var(--bg-page); border-radius: 12px;
    padding: 8px; display: grid; grid-template-columns: repeat(8, 1fr);
    gap: 4px; box-shadow: var(--shadow-elev); max-width: 280px;
  `;
  const emojis = ['😀','😁','😂','🤣','😅','😊','🙂','😉','😍','🥰','😘','😎','🤔','🙃','😐','😴',
                  '👍','👎','👏','🙏','💪','🤝','❤️','🔥','✨','🎉','💯','✅','❌','⭐','💡','📎'];
  for (const e of emojis) {
    const b = document.createElement('button');
    b.textContent = e;
    b.style.cssText = 'font-size: 22px; padding: 4px; border-radius: 6px;';
    b.onmouseenter = () => b.style.background = 'var(--bg-hover)';
    b.onmouseleave = () => b.style.background = 'transparent';
    b.onclick = () => {
      els.msgInput.value += e;
      els.msgInput.focus();
      els.sendBtn.disabled = !els.msgInput.value.trim();
      document.body.removeChild(picker);
      document.removeEventListener('click', dismiss, true);
    };
    picker.appendChild(b);
  }
  document.body.appendChild(picker);
  const dismiss = (ev) => {
    if (!picker.contains(ev.target) && ev.target !== els.emojiBtn) {
      try { document.body.removeChild(picker); } catch {}
      document.removeEventListener('click', dismiss, true);
    }
  };
  setTimeout(() => document.addEventListener('click', dismiss, true), 0);
}

// ──────────────────────────────────────────────────────────────────
// Modals
// ──────────────────────────────────────────────────────────────────
function showDeleteContactModal(contact, messageCount) {
  const root = els.modalRoot;
  const label = contact.alias || abbrev(contact.hash);
  const historyLine = messageCount > 0
    ? `<label class="check-row"><input type="checkbox" id="modal-wipe-history" checked /> Also delete ${messageCount} message${messageCount === 1 ? '' : 's'} from this chat</label>`
    : '<p class="muted">No saved messages with this contact.</p>';

  root.innerHTML = `
    <div class="modal-backdrop">
      <div class="modal">
        <h3>Remove ${escapeHTML(label)}?</h3>
        <p class="muted">The contact and any pending outbox items will be removed from this device. This does not notify the peer.</p>
        ${historyLine}
        <div class="modal-actions">
          <button id="modal-cancel">Cancel</button>
          <button id="modal-confirm" class="primary danger">Remove</button>
        </div>
      </div>
    </div>
  `;
  document.getElementById('modal-cancel').onclick = () => (root.innerHTML = '');
  document.getElementById('modal-confirm').onclick = async () => {
    const wipeBox = document.getElementById('modal-wipe-history');
    const wipeHistory = wipeBox ? wipeBox.checked : true;
    try {
      await api('/api/contacts/delete', {
        method: 'POST',
        body: { hash: contact.hash, wipe_history: wipeHistory },
      });
      root.innerHTML = '';
      const peer = STATE.peers.get(contact.hash);
      if (peer) {
        try { peer.pc.close(); } catch {}
        STATE.peers.delete(contact.hash);
      }
      STATE.preview.delete(contact.hash);
      if (STATE.selectedHash === contact.hash) {
        STATE.selectedHash = null;
        els.chatPane.hidden = true;
        els.emptyState.hidden = false;
        els.chatPeerAlias.textContent = 'peer';
        els.chatPeerStatus.textContent = '';
        els.history.innerHTML = '';
        els.app.dataset.view = 'list';
      }
      await loadSnapshot();
    } catch (e) { toast(e.message, 'error'); }
  };
}

function showVerifyModal(contact) {
  const root = els.modalRoot;
  const myFp = formatFingerprint(STATE.identity?.fingerprint || '');
  const action = contact.verified ? 'Unverify' : 'Mark verified';
  root.innerHTML = `
    <div class="modal-backdrop">
      <div class="modal">
        <h3>Verify ${escapeHTML(contact.alias || abbrev(contact.hash))}</h3>
        <p class="muted">Compare these safety numbers out-of-band (voice/video call, in person). They must match exactly on both sides before you trust this contact.</p>
        <label>You</label>
        <pre class="fp-block" id="modal-fp-self">${escapeHTML(myFp)}</pre>
        <label>${escapeHTML(contact.alias || 'Peer')}</label>
        <pre class="fp-block" id="modal-fp-peer">${escapeHTML(formatFingerprint(contact.fingerprint || ''))}</pre>
        <div class="modal-actions">
          <button id="modal-cancel">Cancel</button>
          <button id="modal-confirm" class="primary">${action}</button>
        </div>
      </div>
    </div>
  `;
  document.getElementById('modal-cancel').onclick = () => (root.innerHTML = '');
  document.getElementById('modal-confirm').onclick = async () => {
    try {
      await api('/api/contacts/verify', {
        method: 'POST',
        body: { hash: contact.hash, verified: !contact.verified },
      });
      root.innerHTML = '';
      await loadSnapshot();
      selectContact(contact.hash);
    } catch (e) { toast(e.message, 'error'); }
  };
}

function showAddContactModal() {
  const root = els.modalRoot;
  root.innerHTML = `
    <div class="modal-backdrop">
      <div class="modal">
        <h3>Add contact</h3>
        <p class="muted">Paste your peer's destination hash. Set an alias to display in the chat list.</p>
        <label>Destination hash</label>
        <input id="modal-hash" placeholder="e.g. 89f3a7b…" autocomplete="off" />
        <label>Alias</label>
        <input id="modal-alias" placeholder="e.g. Alice" autocomplete="off" />
        <div class="modal-actions">
          <button id="modal-cancel">Cancel</button>
          <button id="modal-add" class="primary">Add</button>
        </div>
      </div>
    </div>
  `;
  const cancel = () => (root.innerHTML = '');
  document.getElementById('modal-cancel').onclick = cancel;
  document.getElementById('modal-add').onclick = async () => {
    const hash = document.getElementById('modal-hash').value.trim();
    const alias = document.getElementById('modal-alias').value.trim();
    try {
      await api('/api/contacts/add', { method: 'POST', body: { hash, alias } });
      root.innerHTML = '';
      await loadSnapshot();
      toast(alias ? `Added ${alias}` : 'Contact added');
    } catch (e) { toast(e.message, 'error'); }
  };
  setTimeout(() => document.getElementById('modal-hash').focus(), 50);
}

// ──────────────────────────────────────────────────────────────────
// Settings modal — tabbed shell
// ──────────────────────────────────────────────────────────────────
const SETTINGS_TABS = [
  { id: 'security',      label: 'Security',      init: initSecurityTab      },
  { id: 'network',       label: 'Network',       init: initNetworkTab       },
  { id: 'privacy',       label: 'Privacy',       init: initPrivacyTab       },
  { id: 'notifications', label: 'Notifications', init: initNotificationsTab },
];

function showSettingsModal() {
  const root = els.modalRoot;
  const tabsHTML = SETTINGS_TABS.map(t =>
    `<button class="settings-tab" data-tab="${t.id}" role="tab">${escapeHTML(t.label)}</button>`
  ).join('');
  root.innerHTML = `
    <div class="modal-backdrop">
      <div class="modal modal-wide">
        <h3>Settings</h3>
        <nav class="settings-tabs" role="tablist">${tabsHTML}</nav>
        <div class="modal-body" id="settings-panel" role="tabpanel" aria-busy="true">
          <p class="section-help">Loading…</p>
        </div>
        <div class="modal-actions">
          <button id="modal-close" class="primary">Close</button>
        </div>
      </div>
    </div>
  `;
  const close = () => (root.innerHTML = '');
  document.getElementById('modal-close').onclick = close;
  root.querySelector('.modal-backdrop').addEventListener('click', e => {
    if (e.target === e.currentTarget) close();
  });

  const lastTab = sessionStorage.getItem('udisend_settings_tab');
  const initialId = SETTINGS_TABS.some(t => t.id === lastTab) ? lastTab : SETTINGS_TABS[0].id;

  for (const btn of root.querySelectorAll('.settings-tab')) {
    btn.addEventListener('click', () => activateSettingsTab(btn.dataset.tab));
  }
  activateSettingsTab(initialId);
}

function activateSettingsTab(id) {
  const tab = SETTINGS_TABS.find(t => t.id === id);
  if (!tab) return;
  sessionStorage.setItem('udisend_settings_tab', id);
  for (const btn of document.querySelectorAll('.settings-tab')) {
    btn.classList.toggle('active', btn.dataset.tab === id);
    btn.setAttribute('aria-selected', btn.dataset.tab === id ? 'true' : 'false');
  }
  const panel = document.getElementById('settings-panel');
  panel.setAttribute('aria-busy', 'true');
  panel.innerHTML = `<p class="section-help">Loading…</p>`;
  Promise.resolve(tab.init(panel)).catch(err => {
    panel.innerHTML = `<p class="section-help">Failed to load: ${escapeHTML(err.message)}</p>`;
  }).finally(() => panel.removeAttribute('aria-busy'));
}

// ──────────────────────────────────────────────────────────────────
// Network tab — bootstrap nodes (other network sections appended below)
// ──────────────────────────────────────────────────────────────────
async function initNetworkTab(panel) {
  panel.innerHTML = `
    <section class="settings-section">
      <h4>Bootstrap nodes</h4>
      <p class="section-help">
        Peers tried first to seed the DHT on startup and on Reconnect.
        Manual entries take priority over auto-cached and built-in defaults.
      </p>
      <ul class="bootstrap-list" id="bootstrap-list" aria-busy="true">
        <li class="bootstrap-empty">Loading…</li>
      </ul>
      <div class="bootstrap-add-row">
        <input id="bootstrap-add-addr" placeholder="host:port — e.g. relay.example.com:9000" autocomplete="off" />
        <input id="bootstrap-add-note" placeholder="Note (optional)" autocomplete="off" maxlength="80" />
        <button class="add-btn" id="bootstrap-add-btn">Add</button>
      </div>
      <div class="section-actions">
        <button id="bootstrap-reconnect">Reconnect</button>
      </div>
    </section>
    <section class="settings-section" id="ice-section">
      <h4>STUN / TURN servers</h4>
      <p class="section-help">
        Relay servers used by your browser's WebRTC stack to traverse NAT.
        Custom entries are merged with peers discovered through the network.
      </p>
      <ul class="bootstrap-list" id="ice-list" aria-busy="true">
        <li class="bootstrap-empty">Loading…</li>
      </ul>
      <div class="ice-add-grid">
        <input id="ice-add-url"  placeholder="stun:host:port  or  turn:host:port?transport=udp" autocomplete="off" />
        <input id="ice-add-user" placeholder="Username (TURN)" autocomplete="off" />
        <input id="ice-add-cred" placeholder="Credential (TURN)" autocomplete="off" type="password" />
        <button class="add-btn" id="ice-add-btn">Add</button>
      </div>
      <label class="check-row">
        <input type="checkbox" id="ice-disable-fallback" />
        <span>Don't fall back to the public Google STUN server</span>
      </label>
    </section>
    <section class="settings-section" id="netstatus-section">
      <h4>Network status</h4>
      <p class="section-help">Read-only diagnostics about the local DHT node.</p>
      <ul class="netstatus-list" id="netstatus-list" aria-busy="true">
        <li>Loading…</li>
      </ul>
    </section>
  `;
  await initBootstrapPanel(panel);
  await initICEPanel(panel);
  await initNetworkStatusPanel(panel);
}

async function initBootstrapPanel(panel) {
  const addBtn = panel.querySelector('#bootstrap-add-btn');
  const addAddr = panel.querySelector('#bootstrap-add-addr');
  const addNote = panel.querySelector('#bootstrap-add-note');
  const reconnectBtn = panel.querySelector('#bootstrap-reconnect');

  async function refresh(initial) {
    try {
      const data = await api('/api/bootstrap');
      renderBootstrapList(data.entries || []);
    } catch (e) {
      if (initial) {
        panel.querySelector('#bootstrap-list').innerHTML =
          `<li class="bootstrap-empty">Failed to load: ${escapeHTML(e.message)}</li>`;
      } else {
        toast(e.message, 'error');
      }
    }
  }

  addBtn.onclick = async () => {
    const address = addAddr.value.trim();
    if (!address) { addAddr.focus(); return; }
    const note = addNote.value.trim();
    addBtn.disabled = true;
    try {
      const data = await api('/api/bootstrap/add', { method: 'POST', body: { address, note } });
      addAddr.value = '';
      addNote.value = '';
      renderBootstrapList(data.entries || []);
      if (data.dial_error) {
        toast(`Added but unreachable: ${data.dial_error}`, 'error');
      } else {
        toast('Bootstrap added — dialed ok');
      }
    } catch (e) {
      toast(e.message, 'error');
    } finally {
      addBtn.disabled = false;
      addAddr.focus();
    }
  };
  addAddr.addEventListener('keydown', e => {
    if (e.key === 'Enter') { e.preventDefault(); addBtn.click(); }
  });
  addNote.addEventListener('keydown', e => {
    if (e.key === 'Enter') { e.preventDefault(); addBtn.click(); }
  });

  reconnectBtn.onclick = async () => {
    reconnectBtn.disabled = true;
    const original = reconnectBtn.textContent;
    reconnectBtn.textContent = 'Reconnecting…';
    try {
      const data = await api('/api/bootstrap/reconnect', { method: 'POST', body: {} });
      renderBootstrapList(data.entries || []);
      const ok = (data.results || []).filter(r => r.status === 'ok').length;
      const total = (data.results || []).length;
      if (total === 0) {
        toast('No enabled bootstrap entries to dial');
      } else {
        toast(`Reconnect: ${ok}/${total} succeeded`, ok === total ? null : 'error');
      }
    } catch (e) {
      toast(e.message, 'error');
    } finally {
      reconnectBtn.textContent = original;
      reconnectBtn.disabled = false;
    }
  };

  await refresh(true);
}

function renderBootstrapList(entries) {
  const list = document.getElementById('bootstrap-list');
  if (!list) return;
  list.removeAttribute('aria-busy');
  if (!entries.length) {
    list.innerHTML = `<li class="bootstrap-empty">No bootstrap entries yet. Add one below to get started.</li>`;
    return;
  }
  list.innerHTML = entries.map(renderBootstrapRow).join('');
  for (const e of entries) {
    wireBootstrapRow(e);
  }
}

function renderBootstrapRow(e) {
  const id = bootstrapRowId(e);
  const sourceLabel = e.source === 'manual' ? 'Manual' : (e.source === 'cache' ? 'Cached' : 'Default');
  const statusAttr = e.last_status || '';
  const statusTitle = bootstrapStatusTitle(e);
  const subParts = [`<span class="source-tag ${e.source}">${sourceLabel}</span>`];
  if (e.note) subParts.push(`<span class="note" title="${escapeHTML(e.note)}">${escapeHTML(e.note)}</span>`);
  if (e.last_status_at) {
    subParts.push(`<span title="${escapeHTML(new Date(e.last_status_at * 1000).toLocaleString())}">${bootstrapRelativeTime(e.last_status_at)}</span>`);
  }
  const toggleHTML = e.source === 'manual'
    ? `<button class="toggle-switch" data-action="toggle" data-addr="${escapeHTML(e.address)}" role="switch" aria-checked="${e.enabled ? 'true' : 'false'}" title="${e.enabled ? 'Disable' : 'Enable'}"></button>`
    : '';
  const removable = e.source !== 'default';
  const removeHTML = removable
    ? `<button class="icon-btn ghost" data-action="remove" data-addr="${escapeHTML(e.address)}" data-source="${e.source}" title="Remove" aria-label="Remove">
         <svg viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
           <path d="M5 7h14M9 7V5a2 2 0 0 1 2-2h2a2 2 0 0 1 2 2v2M7 7l1 12a2 2 0 0 0 2 2h4a2 2 0 0 0 2-2l1-12" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
         </svg>
       </button>`
    : '';
  return `
    <li class="bootstrap-row ${e.enabled ? '' : 'disabled'}" id="${id}">
      <span class="bootstrap-status" data-status="${escapeHTML(statusAttr)}" title="${escapeHTML(statusTitle)}"></span>
      <div class="bootstrap-meta">
        <div class="bootstrap-addr" title="${escapeHTML(e.address)}">${escapeHTML(e.address)}</div>
        <div class="bootstrap-sub">${subParts.join('')}</div>
      </div>
      <div class="bootstrap-actions">${toggleHTML}${removeHTML}</div>
    </li>`;
}

function wireBootstrapRow(e) {
  const row = document.getElementById(bootstrapRowId(e));
  if (!row) return;
  const toggle = row.querySelector('[data-action="toggle"]');
  if (toggle) {
    toggle.addEventListener('click', async () => {
      const next = toggle.getAttribute('aria-checked') !== 'true';
      try {
        const data = await api('/api/bootstrap/toggle', {
          method: 'POST',
          body: { address: e.address, enabled: next },
        });
        renderBootstrapList(data.entries || []);
      } catch (err) { toast(err.message, 'error'); }
    });
  }
  const remove = row.querySelector('[data-action="remove"]');
  if (remove) {
    remove.addEventListener('click', async () => {
      try {
        const data = await api('/api/bootstrap/remove', {
          method: 'POST',
          body: { address: e.address, source: e.source },
        });
        renderBootstrapList(data.entries || []);
        toast('Removed');
      } catch (err) { toast(err.message, 'error'); }
    });
  }
}

function bootstrapRowId(e) {
  return 'bs-' + (e.address || '').replace(/[^a-z0-9]+/gi, '-').replace(/^-|-$/g, '');
}

function bootstrapStatusTitle(e) {
  if (!e.last_status) return 'Not yet attempted';
  const when = e.last_status_at ? ' at ' + new Date(e.last_status_at * 1000).toLocaleString() : '';
  return (e.last_status === 'ok' ? 'Last bootstrap succeeded' : 'Last bootstrap failed') + when;
}

function bootstrapRelativeTime(unixSeconds) {
  const diff = Math.floor(Date.now() / 1000 - unixSeconds);
  if (diff < 5) return 'just now';
  if (diff < 60) return diff + 's ago';
  if (diff < 3600) return Math.floor(diff / 60) + 'm ago';
  if (diff < 86400) return Math.floor(diff / 3600) + 'h ago';
  return Math.floor(diff / 86400) + 'd ago';
}

// ──────────────────────────────────────────────────────────────────
// Network tab — STUN/TURN servers + read-only DHT status
// ──────────────────────────────────────────────────────────────────
async function initICEPanel(panel) {
  const list = panel.querySelector('#ice-list');
  const url = panel.querySelector('#ice-add-url');
  const user = panel.querySelector('#ice-add-user');
  const cred = panel.querySelector('#ice-add-cred');
  const addBtn = panel.querySelector('#ice-add-btn');
  const fallbackBox = panel.querySelector('#ice-disable-fallback');

  function render(state) {
    list.removeAttribute('aria-busy');
    fallbackBox.checked = !!state.disable_default_fallback;
    const entries = state.entries || [];
    if (!entries.length) {
      list.innerHTML = `<li class="bootstrap-empty">No custom ICE servers — using whatever the network discovers.</li>`;
      return;
    }
    list.innerHTML = entries.map(e => {
      const sourceLabel = e.source === 'manual' ? 'Manual' : 'Discovered';
      const credChip = e.username ? `<span class="note">user: ${escapeHTML(e.username)}</span>` : '';
      const toggle = e.source === 'manual'
        ? `<button class="toggle-switch" data-action="toggle" data-url="${escapeHTML(e.url)}" role="switch" aria-checked="${e.enabled ? 'true' : 'false'}" title="${e.enabled ? 'Disable' : 'Enable'}"></button>`
        : '';
      const remove = e.source === 'manual'
        ? `<button class="icon-btn ghost" data-action="remove" data-url="${escapeHTML(e.url)}" title="Remove" aria-label="Remove">
             <svg viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
               <path d="M5 7h14M9 7V5a2 2 0 0 1 2-2h2a2 2 0 0 1 2 2v2M7 7l1 12a2 2 0 0 0 2 2h4a2 2 0 0 0 2-2l1-12" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
             </svg>
           </button>`
        : '';
      return `
        <li class="bootstrap-row ${e.enabled ? '' : 'disabled'}">
          <span class="bootstrap-status" data-status="" title=""></span>
          <div class="bootstrap-meta">
            <div class="bootstrap-addr" title="${escapeHTML(e.url)}">${escapeHTML(e.url)}</div>
            <div class="bootstrap-sub">
              <span class="source-tag ${e.source}">${sourceLabel}</span>
              ${credChip}
            </div>
          </div>
          <div class="bootstrap-actions">${toggle}${remove}</div>
        </li>`;
    }).join('');
    list.querySelectorAll('[data-action="toggle"]').forEach(btn => {
      btn.addEventListener('click', async () => {
        const next = btn.getAttribute('aria-checked') !== 'true';
        try {
          const data = await api('/api/ice/toggle', { method: 'POST', body: { url: btn.dataset.url, enabled: next } });
          render(data);
        } catch (err) { toast(err.message, 'error'); }
      });
    });
    list.querySelectorAll('[data-action="remove"]').forEach(btn => {
      btn.addEventListener('click', async () => {
        try {
          const data = await api('/api/ice/remove', { method: 'POST', body: { url: btn.dataset.url } });
          render(data);
          toast('Removed');
        } catch (err) { toast(err.message, 'error'); }
      });
    });
  }

  async function refresh() {
    try {
      const data = await api('/api/ice');
      render(data);
    } catch (e) {
      list.innerHTML = `<li class="bootstrap-empty">Failed: ${escapeHTML(e.message)}</li>`;
    }
  }

  addBtn.onclick = async () => {
    const u = url.value.trim();
    if (!u) { url.focus(); return; }
    addBtn.disabled = true;
    try {
      const data = await api('/api/ice/add', { method: 'POST', body: {
        url: u, username: user.value.trim(), credential: cred.value,
      }});
      url.value = ''; user.value = ''; cred.value = '';
      render(data);
      toast('ICE server added');
    } catch (e) {
      toast(e.message, 'error');
    } finally {
      addBtn.disabled = false;
      url.focus();
    }
  };
  url.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); addBtn.click(); } });
  cred.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); addBtn.click(); } });

  fallbackBox.addEventListener('change', async () => {
    try {
      const data = await api('/api/ice/fallback', { method: 'POST', body: { disabled: fallbackBox.checked } });
      render(data);
    } catch (e) {
      toast(e.message, 'error');
      fallbackBox.checked = !fallbackBox.checked;
    }
  });

  await refresh();
}

async function initNetworkStatusPanel(panel) {
  const list = panel.querySelector('#netstatus-list');
  try {
    const data = await api('/api/network/status');
    list.removeAttribute('aria-busy');
    const rows = [
      ['Local UDP', data.local_address || '—'],
      ['Identity', abbrev(data.identity_hash || '')],
      ['Routing-table size', data.routing_table_size ?? 0],
      ['Active sessions', data.active_sessions ?? 0],
      ['Cached peers', data.seen_peers ?? 0],
      ['Public mode', data.public_mode ? 'yes' : 'no'],
    ];
    list.innerHTML = rows.map(([k, v]) =>
      `<li><span class="netstatus-key">${escapeHTML(k)}</span><span class="netstatus-val">${escapeHTML(String(v))}</span></li>`
    ).join('');
  } catch (e) {
    list.innerHTML = `<li>Failed: ${escapeHTML(e.message)}</li>`;
  }
}

// ──────────────────────────────────────────────────────────────────
// Security tab — passphrase, TOTP, sessions, audit log
// ──────────────────────────────────────────────────────────────────
async function initSecurityTab(panel) {
  panel.innerHTML = `
    <section class="settings-section" id="sec-passphrase-section">
      <h4>Passphrase</h4>
      <p class="section-help">Used to log in to this messenger from a remote browser.</p>
      <div class="sec-actions">
        <button id="sec-change-pass">Change passphrase…</button>
        <span class="muted" id="sec-pass-status">—</span>
      </div>
    </section>
    <section class="settings-section" id="sec-totp-section">
      <h4>Two-factor authentication (TOTP)</h4>
      <p class="section-help">A 6-digit code from an authenticator app (Aegis, Authy, 1Password) on top of the passphrase.</p>
      <div class="sec-actions">
        <button id="sec-totp-enroll" hidden>Enable TOTP…</button>
        <button id="sec-totp-disable" class="danger" hidden>Disable TOTP…</button>
        <button id="sec-recovery-regen" hidden>Regenerate recovery codes…</button>
        <span class="muted" id="sec-totp-status">—</span>
      </div>
    </section>
    <section class="settings-section" id="sec-sessions-section">
      <h4>Active sessions</h4>
      <p class="section-help">Logged-in browsers. Revoke any you don't recognise.</p>
      <ul class="bootstrap-list" id="sec-sessions-list" aria-busy="true">
        <li class="bootstrap-empty">Loading…</li>
      </ul>
    </section>
    <section class="settings-section" id="sec-audit-section">
      <h4>Audit log</h4>
      <p class="section-help">Recent login attempts, lockouts and logouts.</p>
      <ul class="audit-list" id="sec-audit-list" aria-busy="true">
        <li>Loading…</li>
      </ul>
    </section>
  `;

  let state = await api('/api/auth/state');
  const passStatus = panel.querySelector('#sec-pass-status');
  const totpStatus = panel.querySelector('#sec-totp-status');
  const enrollBtn = panel.querySelector('#sec-totp-enroll');
  const disableBtn = panel.querySelector('#sec-totp-disable');
  const regenBtn = panel.querySelector('#sec-recovery-regen');

  function render() {
    passStatus.textContent = state.passphrase_set
      ? `Set on ${new Date(state.passphrase_updated_at * 1000).toLocaleDateString()}`
      : 'Not set — public mode requires `messenger -set-password`';
    panel.querySelector('#sec-change-pass').disabled = !state.passphrase_set;
    if (state.totp_enrolled) {
      totpStatus.textContent = `Enabled — ${state.recovery_codes_remaining} recovery code(s) left`;
      enrollBtn.hidden = true;
      disableBtn.hidden = false;
      regenBtn.hidden = false;
    } else {
      totpStatus.textContent = 'Disabled';
      enrollBtn.hidden = !state.passphrase_set;
      disableBtn.hidden = true;
      regenBtn.hidden = true;
    }
  }
  render();

  panel.querySelector('#sec-change-pass').onclick = async () => {
    await showPassphraseChangeModal(state.totp_enrolled);
    state = await api('/api/auth/state');
    render();
  };
  enrollBtn.onclick = async () => {
    const enrolled = await showTOTPEnrollModal();
    if (enrolled) {
      state = await api('/api/auth/state');
      render();
    }
  };
  disableBtn.onclick = async () => {
    const ok = await showTOTPDisableModal();
    if (ok) {
      state = await api('/api/auth/state');
      render();
    }
  };
  regenBtn.onclick = async () => {
    const refreshed = await showRecoveryRegenModal();
    if (refreshed) {
      state = await api('/api/auth/state');
      render();
    }
  };

  await loadAuthSessions(panel);
  await loadAuditLog(panel);
}

async function loadAuthSessions(panel) {
  const list = panel.querySelector('#sec-sessions-list');
  try {
    const data = await api('/api/auth/sessions');
    list.removeAttribute('aria-busy');
    const sessions = data.sessions || [];
    if (!sessions.length) {
      list.innerHTML = `<li class="bootstrap-empty">No active sessions tracked.</li>`;
      return;
    }
    list.innerHTML = sessions.map(s => {
      const isCurrent = s.is_current;
      const ua = truncate(s.user_agent || 'unknown agent', 70);
      const ip = s.remote_ip || '—';
      const last = s.last_seen ? bootstrapRelativeTime(s.last_seen) : '—';
      const removeBtn = isCurrent ? '' : `<button class="icon-btn ghost" data-revoke="${escapeHTML(s.public_id)}" title="Revoke" aria-label="Revoke">
        <svg viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
          <path d="M5 7h14M9 7V5a2 2 0 0 1 2-2h2a2 2 0 0 1 2 2v2M7 7l1 12a2 2 0 0 0 2 2h4a2 2 0 0 0 2-2l1-12" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
        </svg></button>`;
      return `
        <li class="bootstrap-row">
          <span class="bootstrap-status" data-status="${isCurrent ? 'ok' : ''}" title="${isCurrent ? 'Current session' : ''}"></span>
          <div class="bootstrap-meta">
            <div class="bootstrap-addr" title="${escapeHTML(ua)}">${escapeHTML(ua)}</div>
            <div class="bootstrap-sub">
              <span class="source-tag ${isCurrent ? 'manual' : ''}">${isCurrent ? 'This browser' : 'Other'}</span>
              <span>${escapeHTML(ip)}</span>
              <span title="${escapeHTML(new Date(s.last_seen * 1000).toLocaleString())}">last seen ${escapeHTML(last)}</span>
            </div>
          </div>
          <div class="bootstrap-actions">${removeBtn}</div>
        </li>`;
    }).join('');
    list.querySelectorAll('[data-revoke]').forEach(btn => {
      btn.addEventListener('click', async () => {
        try {
          await api('/api/auth/sessions/revoke', { method: 'POST', body: { public_id: btn.dataset.revoke } });
          await loadAuthSessions(panel);
          toast('Session revoked');
        } catch (e) { toast(e.message, 'error'); }
      });
    });
  } catch (e) {
    list.innerHTML = `<li class="bootstrap-empty">Failed: ${escapeHTML(e.message)}</li>`;
  }
}

async function loadAuditLog(panel) {
  const list = panel.querySelector('#sec-audit-list');
  try {
    const data = await api('/api/auth/log?limit=50');
    list.removeAttribute('aria-busy');
    const entries = data.entries || [];
    if (!entries.length) {
      list.innerHTML = `<li>No audit events yet.</li>`;
      return;
    }
    list.innerHTML = entries.map(e => {
      const when = new Date(e.timestamp * 1000).toLocaleString();
      const ua = truncate(e.user_agent || '—', 60);
      const note = e.note ? ` · ${escapeHTML(e.note)}` : '';
      return `<li>
        <span class="audit-event">${escapeHTML(e.event)}</span>
        <span class="audit-when" title="${escapeHTML(when)}">${escapeHTML(bootstrapRelativeTime(e.timestamp))}</span>
        <span class="audit-ip">${escapeHTML(e.remote_ip || '—')}</span>
        <span class="audit-ua" title="${escapeHTML(ua)}">${escapeHTML(ua)}${note}</span>
      </li>`;
    }).join('');
  } catch (e) {
    list.innerHTML = `<li>Failed: ${escapeHTML(e.message)}</li>`;
  }
}

function showPassphraseChangeModal(totpEnrolled) {
  return new Promise(resolve => {
    const root = els.modalRoot;
    const stepUpHTML = totpEnrolled ? `
      <label>TOTP code (or recovery code)</label>
      <input id="pp-code" inputmode="numeric" maxlength="20" autocomplete="one-time-code" />` : '';
    const stack = document.createElement('div');
    stack.className = 'modal-backdrop modal-stack';
    stack.innerHTML = `
        <div class="modal">
          <h3>Change passphrase</h3>
          <p class="muted">You'll be signed out of all other sessions.</p>
          <label>Current passphrase</label>
          <input id="pp-old" type="password" autocomplete="current-password" />
          <label>New passphrase</label>
          <input id="pp-new" type="password" autocomplete="new-password" />
          <label>Confirm new passphrase</label>
          <input id="pp-new2" type="password" autocomplete="new-password" />
          ${stepUpHTML}
          <div class="modal-actions">
            <button id="pp-cancel">Cancel</button>
            <button id="pp-save" class="primary">Save</button>
          </div>
        </div>`;
    root.appendChild(stack);
    const close = () => { stack.remove(); resolve(false); };
    stack.querySelector('#pp-cancel').onclick = close;
    stack.addEventListener('click', e => { if (e.target === stack) close(); });
    stack.querySelector('#pp-save').onclick = async () => {
      const oldP = stack.querySelector('#pp-old').value;
      const n1 = stack.querySelector('#pp-new').value;
      const n2 = stack.querySelector('#pp-new2').value;
      if (!oldP || !n1) { toast('All fields required', 'error'); return; }
      if (n1 !== n2) { toast('New passphrases do not match', 'error'); return; }
      const codeRaw = totpEnrolled ? (stack.querySelector('#pp-code').value || '').trim() : '';
      const body = { old: oldP, new: n1 };
      if (/^[0-9]{6}$/.test(codeRaw)) body.code = codeRaw;
      else if (codeRaw) body.recovery = codeRaw;
      try {
        await api('/api/auth/change-passphrase', { method: 'POST', body });
        toast('Passphrase updated');
        stack.remove();
        resolve(true);
      } catch (e) { toast(e.message, 'error'); }
    };
    setTimeout(() => stack.querySelector('#pp-old').focus(), 50);
  });
}

function showTOTPEnrollModal() {
  return new Promise(async resolve => {
    let start;
    try {
      start = await api('/api/auth/totp/start', { method: 'POST', body: {} });
    } catch (e) {
      toast(e.message, 'error');
      resolve(false);
      return;
    }
    const root = els.modalRoot;
    root.insertAdjacentHTML('beforeend', `
      <div class="modal-backdrop modal-stack">
        <div class="modal">
          <h3>Enable two-factor authentication</h3>
          <p class="muted">Scan the secret with your authenticator app, then type a code to confirm.</p>
          <pre class="fp-block" id="totp-secret">${escapeHTML(start.secret_b32)}</pre>
          <p class="muted" style="margin-top:8px;font-size:12px;">Or paste this URL into a TOTP app:</p>
          <pre class="fp-block" style="font-size:11px;">${escapeHTML(start.otpauth_url)}</pre>
          <label>6-digit code</label>
          <input id="totp-code" inputmode="numeric" pattern="[0-9]*" maxlength="6" autocomplete="one-time-code" />
          <div class="modal-actions">
            <button id="totp-cancel">Cancel</button>
            <button id="totp-confirm" class="primary">Enable</button>
          </div>
        </div>
      </div>`);
    const stack = root.querySelector('.modal-stack');
    const close = () => { stack.remove(); resolve(false); };
    stack.querySelector('#totp-cancel').onclick = close;
    stack.addEventListener('click', e => { if (e.target === stack) close(); });
    stack.querySelector('#totp-confirm').onclick = async () => {
      const code = stack.querySelector('#totp-code').value.trim();
      try {
        const data = await api('/api/auth/totp/finish', {
          method: 'POST',
          body: { enroll_id: start.enroll_id, code },
        });
        stack.remove();
        await showRecoveryCodesModal(data.recovery_codes || []);
        resolve(true);
      } catch (e) { toast(e.message, 'error'); }
    };
    setTimeout(() => stack.querySelector('#totp-code').focus(), 50);
  });
}

function showTOTPDisableModal() {
  return new Promise(resolve => {
    const root = els.modalRoot;
    const stack = document.createElement('div');
    stack.className = 'modal-backdrop modal-stack';
    stack.innerHTML = `
        <div class="modal">
          <h3>Disable two-factor authentication</h3>
          <p class="muted">Provide a current 6-digit code or a recovery code (passphrase alone will not disable 2FA).</p>
          <label>TOTP or recovery code</label>
          <input id="off-code" inputmode="text" maxlength="20" autocomplete="one-time-code" />
          <label>Passphrase</label>
          <input id="off-pass" type="password" autocomplete="current-password" />
          <div class="modal-actions">
            <button id="off-cancel">Cancel</button>
            <button id="off-confirm" class="primary danger">Disable</button>
          </div>
        </div>`;
    root.appendChild(stack);
    const close = () => { stack.remove(); resolve(false); };
    stack.querySelector('#off-cancel').onclick = close;
    stack.addEventListener('click', e => { if (e.target === stack) close(); });
    stack.querySelector('#off-confirm').onclick = async () => {
      const codeRaw = stack.querySelector('#off-code').value.trim();
      const pass = stack.querySelector('#off-pass').value;
      const body = { passphrase: pass };
      if (/^[0-9]{6}$/.test(codeRaw)) body.code = codeRaw;
      else if (codeRaw) body.recovery = codeRaw;
      try {
        await api('/api/auth/totp/disable', { method: 'POST', body });
        toast('Two-factor disabled');
        stack.remove();
        resolve(true);
      } catch (e) { toast(e.message, 'error'); }
    };
    setTimeout(() => stack.querySelector('#off-code').focus(), 50);
  });
}

function showRecoveryRegenModal() {
  return new Promise(resolve => {
    const root = els.modalRoot;
    root.insertAdjacentHTML('beforeend', `
      <div class="modal-backdrop modal-stack">
        <div class="modal">
          <h3>Regenerate recovery codes</h3>
          <p class="muted">Confirm with a current TOTP code or passphrase. Old codes will stop working.</p>
          <label>TOTP code</label>
          <input id="rg-code" inputmode="numeric" maxlength="6" autocomplete="one-time-code" />
          <label>Or passphrase</label>
          <input id="rg-pass" type="password" autocomplete="current-password" />
          <div class="modal-actions">
            <button id="rg-cancel">Cancel</button>
            <button id="rg-confirm" class="primary">Regenerate</button>
          </div>
        </div>
      </div>`);
    const stack = root.querySelector('.modal-stack');
    const close = () => { stack.remove(); resolve(false); };
    stack.querySelector('#rg-cancel').onclick = close;
    stack.addEventListener('click', e => { if (e.target === stack) close(); });
    stack.querySelector('#rg-confirm').onclick = async () => {
      const codeRaw = stack.querySelector('#rg-code').value.trim();
      const pass = stack.querySelector('#rg-pass').value;
      const body = { passphrase: pass };
      if (/^[0-9]{6}$/.test(codeRaw)) body.code = codeRaw;
      else if (codeRaw) body.recovery = codeRaw;
      try {
        const data = await api('/api/auth/recovery/regenerate', { method: 'POST', body });
        stack.remove();
        await showRecoveryCodesModal(data.recovery_codes || []);
        resolve(true);
      } catch (e) { toast(e.message, 'error'); }
    };
    setTimeout(() => stack.querySelector('#rg-code').focus(), 50);
  });
}

function showRecoveryCodesModal(codes) {
  return new Promise(resolve => {
    const root = els.modalRoot;
    root.insertAdjacentHTML('beforeend', `
      <div class="modal-backdrop modal-stack">
        <div class="modal">
          <h3>Save these recovery codes</h3>
          <p class="muted">Store them somewhere safe. Each code lets you sign in once if you lose your authenticator.</p>
          <pre class="fp-block">${codes.map(escapeHTML).join('\n')}</pre>
          <div class="modal-actions">
            <button id="rc-copy">Copy</button>
            <button id="rc-done" class="primary">Done</button>
          </div>
        </div>
      </div>`);
    const stack = root.querySelector('.modal-stack');
    stack.querySelector('#rc-copy').onclick = () => {
      navigator.clipboard.writeText(codes.join('\n')).then(() => toast('Copied'));
    };
    stack.querySelector('#rc-done').onclick = () => { stack.remove(); resolve(); };
  });
}

// ──────────────────────────────────────────────────────────────────
// Privacy tab — history retention, storage usage/vacuum, identity backup
// ──────────────────────────────────────────────────────────────────
async function initPrivacyTab(panel) {
  panel.innerHTML = `
    <section class="settings-section">
      <h4>History retention</h4>
      <p class="section-help">Automatically delete chat messages older than the chosen age. Set to 0 to disable.</p>
      <div class="settings-row">
        <input id="retain-days" type="number" min="0" max="3650" step="1" value="0" />
        <span class="muted">days (0 = keep forever)</span>
        <button id="retain-save">Save</button>
      </div>
    </section>
    <section class="settings-section">
      <h4>Storage</h4>
      <p class="section-help">Local SQLite database — contacts, messages, outbox, sessions.</p>
      <ul class="netstatus-list" id="storage-list" aria-busy="true">
        <li>Loading…</li>
      </ul>
      <div class="section-actions">
        <button id="storage-vacuum">Vacuum database</button>
      </div>
    </section>
    <section class="settings-section">
      <h4>Identity backup</h4>
      <p class="section-help">Download an encrypted copy of your identity seed. Anyone with this file and your passphrase can impersonate you — store it as carefully as the passphrase.</p>
      <div class="section-actions">
        <button id="identity-export">Export identity…</button>
      </div>
    </section>
  `;

  const retainInput = panel.querySelector('#retain-days');
  const retainSave = panel.querySelector('#retain-save');
  try {
    const cur = await api('/api/settings/history-retention');
    retainInput.value = String(cur.days || 0);
  } catch (e) { /* leave default */ }
  retainSave.onclick = async () => {
    const v = Math.max(0, Math.min(3650, parseInt(retainInput.value, 10) || 0));
    retainInput.value = String(v);
    try {
      await api('/api/settings/history-retention', { method: 'POST', body: { days: v } });
      toast('Saved');
    } catch (e) { toast(e.message, 'error'); }
  };

  const storageList = panel.querySelector('#storage-list');
  async function loadUsage() {
    try {
      const u = await api('/api/storage/usage');
      storageList.removeAttribute('aria-busy');
      const rows = [
        ['Database file', humanSize(u.db_bytes || 0)],
        ['Messages', String(u.messages || 0)],
        ['Contacts', String(u.contacts || 0)],
        ['Outbox queued', String(u.outbox || 0)],
      ];
      storageList.innerHTML = rows.map(([k, v]) =>
        `<li><span class="netstatus-key">${escapeHTML(k)}</span><span class="netstatus-val">${escapeHTML(v)}</span></li>`
      ).join('');
    } catch (e) { storageList.innerHTML = `<li>Failed: ${escapeHTML(e.message)}</li>`; }
  }
  await loadUsage();
  panel.querySelector('#storage-vacuum').onclick = async () => {
    try {
      const r = await api('/api/storage/vacuum', { method: 'POST', body: {} });
      toast(`Reclaimed ${humanSize(r.reclaimed_bytes || 0)}`);
      await loadUsage();
    } catch (e) { toast(e.message, 'error'); }
  };

  panel.querySelector('#identity-export').onclick = () => showIdentityExportModal();
}

async function showIdentityExportModal() {
  let totpEnrolled = false;
  try {
    const st = await api('/api/auth/state');
    totpEnrolled = !!st.totp_enrolled;
  } catch (e) { /* fall back to passphrase-only modal */ }
  return new Promise(resolve => {
    const root = els.modalRoot;
    const stepUpHTML = totpEnrolled ? `
      <label>TOTP code (or recovery code)</label>
      <input id="ix-code" inputmode="text" maxlength="20" autocomplete="one-time-code" />` : '';
    const stack = document.createElement('div');
    stack.className = 'modal-backdrop modal-stack';
    stack.innerHTML = `
        <div class="modal">
          <h3>Export identity</h3>
          <p class="muted">Confirm your current passphrase. The exported file will be encrypted with the same passphrase.</p>
          <label>Passphrase</label>
          <input id="ix-pass" type="password" autocomplete="current-password" />
          ${stepUpHTML}
          <div class="modal-actions">
            <button id="ix-cancel">Cancel</button>
            <button id="ix-go" class="primary">Download</button>
          </div>
        </div>`;
    root.appendChild(stack);
    const close = () => { stack.remove(); resolve(); };
    stack.querySelector('#ix-cancel').onclick = close;
    stack.querySelector('#ix-go').onclick = async () => {
      const pass = stack.querySelector('#ix-pass').value;
      if (!pass) { toast('Passphrase required', 'error'); return; }
      const body = { passphrase: pass };
      if (totpEnrolled) {
        const codeRaw = (stack.querySelector('#ix-code').value || '').trim();
        if (/^[0-9]{6}$/.test(codeRaw)) body.code = codeRaw;
        else if (codeRaw) body.recovery = codeRaw;
      }
      try {
        const r = await fetch('/api/identity/export', {
          method: 'POST',
          credentials: 'same-origin',
          headers: {
            'X-Requested-With': 'udisend',
            'Content-Type': 'application/json',
            ...(TOKEN ? { 'Authorization': 'Bearer ' + TOKEN } : {}),
          },
          body: JSON.stringify(body),
        });
        if (!r.ok) throw new Error(await r.text());
        const blob = await r.blob();
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url; a.download = 'udisend-identity.bin';
        document.body.appendChild(a); a.click(); a.remove();
        setTimeout(() => URL.revokeObjectURL(url), 1000);
        toast('Identity exported');
        close();
      } catch (e) { toast(e.message, 'error'); }
    };
    setTimeout(() => stack.querySelector('#ix-pass').focus(), 50);
  });
}

// ──────────────────────────────────────────────────────────────────
// Notifications tab — browser notifications + verbose-logs toggle
// ──────────────────────────────────────────────────────────────────
async function initNotificationsTab(panel) {
  const supported = 'Notification' in window;
  const permission = supported ? Notification.permission : 'unsupported';
  const enabled = localStorage.getItem('udisend_notifications') === 'on' && permission === 'granted';
  panel.innerHTML = `
    <section class="settings-section">
      <h4>Browser notifications</h4>
      <p class="section-help">Show a desktop notification for incoming messages and calls when this tab is in the background.</p>
      <label class="check-row">
        <input type="checkbox" id="notif-enable" ${enabled ? 'checked' : ''} ${supported ? '' : 'disabled'} />
        <span>${supported ? 'Show notifications' : 'Not supported by this browser'}</span>
      </label>
      <p class="muted" id="notif-status" style="font-size:12px;">Permission: ${escapeHTML(permission)}</p>
    </section>
    <section class="settings-section" id="logs-section">
      <h4>Diagnostics</h4>
      <p class="section-help">Verbose server logs help diagnose connectivity issues, at the cost of larger log files.</p>
      <label class="check-row">
        <input type="checkbox" id="logs-verbose" />
        <span>Verbose server logs</span>
      </label>
    </section>
  `;
  const cb = panel.querySelector('#notif-enable');
  const status = panel.querySelector('#notif-status');
  cb.addEventListener('change', async () => {
    if (!cb.checked) {
      localStorage.setItem('udisend_notifications', 'off');
      return;
    }
    if (Notification.permission === 'default') {
      const got = await Notification.requestPermission();
      status.textContent = `Permission: ${got}`;
      if (got !== 'granted') { cb.checked = false; return; }
    }
    if (Notification.permission === 'granted') {
      localStorage.setItem('udisend_notifications', 'on');
    } else {
      cb.checked = false;
      localStorage.setItem('udisend_notifications', 'off');
    }
  });

  const verbose = panel.querySelector('#logs-verbose');
  try {
    const cur = await api('/api/settings/log-level');
    verbose.checked = cur.level === 'debug';
  } catch (e) { /* leave unchecked */ }
  verbose.addEventListener('change', async () => {
    try {
      await api('/api/settings/log-level', { method: 'POST', body: { level: verbose.checked ? 'debug' : 'info' } });
      toast(verbose.checked ? 'Verbose logs on' : 'Verbose logs off');
    } catch (e) {
      toast(e.message, 'error');
      verbose.checked = !verbose.checked;
    }
  });
}

// ──────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────
function aliasOf(hash) {
  const c = STATE.contacts.find(x => x.hash === hash);

  return c?.alias || abbrev(hash);
}

function abbrev(hash) {
  if (!hash) return '';

  return hash.length > 12 ? hash.slice(0, 6) + '…' + hash.slice(-4) : hash;
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, c => ({ '&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;' }[c]));
}

function humanSize(b) {
  if (b < 1024) return `${b} B`;
  if (b < 1024 * 1024) return `${(b/1024).toFixed(1)} KiB`;

  return `${(b/1024/1024).toFixed(1)} MiB`;
}

function truncate(s, n) {
  if (!s) return '';

  return s.length > n ? s.slice(0, n - 1) + '…' : s;
}

function formatFingerprint(fp) {
  if (!fp) return '';
  // Group every 4 chars for readability.
  const clean = fp.replace(/\s+/g, '');
  const groups = [];
  for (let i = 0; i < clean.length; i += 4) groups.push(clean.slice(i, i + 4));

  return groups.join(' ');
}

function formatTime(d) {
  const hh = String(d.getHours()).padStart(2, '0');
  const mm = String(d.getMinutes()).padStart(2, '0');

  return `${hh}:${mm}`;
}

function dayKey(d) {
  return d.getFullYear() + '-' + (d.getMonth() + 1) + '-' + d.getDate();
}

function formatDateSeparator(d) {
  const today = new Date();
  const ymd = (x) => `${x.getFullYear()}-${x.getMonth()}-${x.getDate()}`;
  if (ymd(d) === ymd(today)) return 'Today';
  const y = new Date(today); y.setDate(y.getDate() - 1);
  if (ymd(d) === ymd(y)) return 'Yesterday';
  if (today.getFullYear() === d.getFullYear()) {
    return d.toLocaleDateString(undefined, { day: 'numeric', month: 'long' });
  }

  return d.toLocaleDateString(undefined, { day: 'numeric', month: 'long', year: 'numeric' });
}

function formatChatListTime(d) {
  const today = new Date();
  const sameDay = today.toDateString() === d.toDateString();
  if (sameDay) return formatTime(d);
  const y = new Date(today); y.setDate(y.getDate() - 1);
  if (y.toDateString() === d.toDateString()) return 'Yesterday';
  if (today.getFullYear() === d.getFullYear()) {
    return d.toLocaleDateString(undefined, { day: 'numeric', month: 'short' });
  }

  return d.toLocaleDateString(undefined, { year: '2-digit', month: 'numeric', day: 'numeric' });
}

// Deterministic 0..6 from a hash string.
function colorIdx(hash) {
  if (!hash) return 5;
  let h = 0;
  for (let i = 0; i < hash.length; i++) h = (h * 31 + hash.charCodeAt(i)) >>> 0;

  return h % 7;
}

function paintAvatar(el, label, hash, size) {
  el.classList.add('avatar');
  if (size === 'sm') el.classList.add('size-sm');
  else if (size === 'md') el.classList.add('size-md');
  for (let i = 0; i < 7; i++) el.classList.remove('color-' + i);
  el.classList.add('color-' + colorIdx(hash || label));
  el.textContent = initials(label || hash);
}

function initials(s) {
  if (!s) return '·';
  const parts = s.trim().split(/\s+/);
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
  if (parts[0].length >= 2) return parts[0].slice(0, 2).toUpperCase();

  return parts[0][0].toUpperCase();
}

function verifiedIcon() {
  const span = document.createElement('span');
  span.className = 'verified-icon';
  span.title = 'Verified safety number';
  span.innerHTML = `<svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true">
    <path d="M12 2 4 5v6c0 5 3.5 9.5 8 11 4.5-1.5 8-6 8-11V5l-8-3Z" fill="currentColor"/>
    <path d="m9 12 2 2 4-4" stroke="white" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" fill="none"/>
  </svg>`;

  return span;
}

function checkmarkIcon() {
  const span = document.createElement('span');
  span.className = 'check';
  span.innerHTML = `<svg viewBox="0 0 18 18" width="16" height="14" aria-hidden="true">
    <path d="M2 9.5l3.5 3.5L11 6.5" stroke="currentColor" stroke-width="1.6" fill="none" stroke-linecap="round" stroke-linejoin="round"/>
    <path d="M7 9.5l3.5 3.5L16 6.5" stroke="currentColor" stroke-width="1.6" fill="none" stroke-linecap="round" stroke-linejoin="round"/>
  </svg>`;

  return span;
}

// maybeNotify shows a desktop notification iff the user opted in AND the
// document is currently hidden. Quietly noop'd in any other case so we
// never compete with the in-page UI for the user's attention. Clicking
// the notification focuses the window and selects the relevant chat.
function maybeNotify(title, body, peerHash, opts) {
  if (!('Notification' in window)) return;
  if (Notification.permission !== 'granted') return;
  if (localStorage.getItem('udisend_notifications') !== 'on') return;
  if (typeof document !== 'undefined' && !document.hidden) return;
  try {
    const n = new Notification(title, {
      body,
      tag: peerHash || title,
      silent: false,
      requireInteraction: !!(opts && opts.requireInteraction),
    });
    n.onclick = () => {
      window.focus();
      if (peerHash) selectContact(peerHash);
      n.close();
    };
  } catch (e) {
    console.warn('notification failed', e);
  }
}

function toast(msg, kind) {
  const t = document.createElement('div');
  t.className = 'toast' + (kind === 'error' ? ' error' : '');
  t.textContent = msg;
  els.toastRoot.appendChild(t);
  setTimeout(() => {
    t.style.transition = 'opacity 220ms';
    t.style.opacity = '0';
    setTimeout(() => t.remove(), 240);
  }, 3200);
}
