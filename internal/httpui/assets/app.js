// udisend browser-side runtime.
//
// Architecture:
//   - Server-Sent Events stream from /api/events delivers Go-side
//     pushes (incoming sessions, signaling traffic from peers, errors).
//   - REST endpoints under /api/* drive contact / history / session
//     management.
//   - WebRTC PeerConnection lives in this script. Each chat peer gets one
//     RTCPeerConnection with a DataChannel for text/files plus optional
//     audio/video tracks for calls.
//   - Signaling envelopes (offer/answer/ICE) ride over the Go-side
//     end-to-end-encrypted signaling pipe; the JS only sees the
//     relevant SDP / candidate strings.

const TOKEN = new URLSearchParams(location.search).get('token') || sessionStorage.getItem('udisend_token');
if (TOKEN) sessionStorage.setItem('udisend_token', TOKEN);

const STATE = {
  identity: null,
  contacts: [],
  selectedHash: null,
  // peer hash → RTCPeerConnection wrapper
  peers: new Map(),
};

const els = {
  myAlias:     document.getElementById('my-alias'),
  myHash:      document.getElementById('my-hash'),
  myFp:        document.getElementById('my-fingerprint'),
  myAddr:      document.getElementById('my-address'),
  connStatus:  document.getElementById('conn-status'),
  contacts:    document.getElementById('contacts'),
  addContact:  document.getElementById('add-contact-btn'),
  chatPane:    document.getElementById('chat-pane'),
  emptyState:  document.getElementById('empty-state'),
  chatPeerAlias: document.getElementById('chat-peer-alias'),
  chatPeerHash:  document.getElementById('chat-peer-hash'),
  history:     document.getElementById('chat-history'),
  msgInput:    document.getElementById('msg-input'),
  sendBtn:     document.getElementById('send-btn'),
  fileInput:   document.getElementById('file-input'),
  callBtn:     document.getElementById('call-btn'),
  hangupBtn:   document.getElementById('hangup-btn'),
  verifyBtn:   document.getElementById('verify-btn'),
  callPane:    document.getElementById('call-pane'),
  localVideo:  document.getElementById('local-video'),
  remoteVideo: document.getElementById('remote-video'),
  modalRoot:   document.getElementById('modal-root'),
};

// ──────────────────────────────────────────────────────────────────
// HTTP helpers
// ──────────────────────────────────────────────────────────────────
async function api(path, opts = {}) {
  const headers = Object.assign({}, opts.headers || {}, {
    'Authorization': 'Bearer ' + TOKEN,
  });
  if (opts.body && typeof opts.body === 'object' && !(opts.body instanceof FormData)) {
    headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(opts.body);
  }
  const r = await fetch(path, { ...opts, headers });
  if (!r.ok) {
    const t = await r.text();
    throw new Error(`${path}: ${r.status} ${t}`);
  }
  if (r.status === 204) return null;
  const contentType = r.headers.get('content-type') || '';
  if (contentType.includes('application/json')) return r.json();
  return r.text();
}

// ──────────────────────────────────────────────────────────────────
// Boot
// ──────────────────────────────────────────────────────────────────
async function boot() {
  if (!TOKEN) {
    document.body.innerHTML = '<p style="padding:32px;color:#ff5d6c">Missing auth token. Open the URL printed in the messenger logs (it contains <code>?token=…</code>).</p>';
    return;
  }
  await loadSnapshot();
  startEventStream();
  attachUIHandlers();
}
boot();

// ──────────────────────────────────────────────────────────────────
// Snapshot + contact list
// ──────────────────────────────────────────────────────────────────
async function loadSnapshot() {
  const snap = await api('/api/snapshot');
  STATE.identity = snap.identity;
  STATE.contacts = snap.contacts;
  els.myHash.textContent = snap.identity.hash;
  els.myFp.textContent = snap.identity.fingerprint;
  els.myAddr.textContent = snap.identity.address;
  els.myAlias.textContent = 'me';
  renderContacts();
}

function renderContacts() {
  els.contacts.innerHTML = '';
  STATE.contacts.forEach(c => {
    const li = document.createElement('li');
    li.dataset.hash = c.hash;
    if (c.hash === STATE.selectedHash) li.classList.add('active');
    const dotClass = peerDotClass(c.hash);
    li.innerHTML = `
      <span class="dot ${dotClass}"></span>
      <div style="flex:1; min-width:0">
        <div class="alias">${escapeHTML(c.alias || c.hash.slice(0, 12) + '…')} ${c.verified ? '<span class="verified" title="fingerprint verified">✓</span>' : ''}</div>
        <div class="hash">${c.hash}</div>
      </div>
    `;
    li.addEventListener('click', () => selectContact(c.hash));
    els.contacts.appendChild(li);
  });
}

function peerDotClass(hash) {
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
  els.emptyState.hidden = true;
  els.chatPane.hidden = false;
  els.chatPeerAlias.textContent = c.alias || hash.slice(0, 12) + '…';
  els.chatPeerHash.textContent = `${hash}  ·  fp: ${c.fingerprint}`;
  loadHistory(hash);
  renderContacts();
  // Auto-open session if we don't have one yet.
  ensurePeer(hash).catch(err => systemMsg('open session: ' + err.message));
}

async function loadHistory(hash) {
  els.history.innerHTML = '';
  const items = await api(`/api/history?peer=${encodeURIComponent(hash)}`);
  for (const e of items) {
    addHistoryRow(e.direction, e.body, new Date(e.timestamp / 1e6), e.kind);
  }
  scrollHistory();
}

function addHistoryRow(direction, body, when, kind) {
  const div = document.createElement('div');
  if (kind === 100 /* file marker */) {
    div.className = 'msg file ' + (direction === 'in' ? 'in' : 'out');
    div.textContent = body;
  } else if (kind === 0 /* system */) {
    div.className = 'msg system';
    div.textContent = body;
  } else {
    div.className = 'msg ' + (direction === 'in' ? 'in' : 'out');
    div.textContent = body;
  }
  const meta = document.createElement('span');
  meta.className = 'meta';
  meta.textContent = (when || new Date()).toLocaleTimeString();
  div.appendChild(meta);
  els.history.appendChild(div);
  scrollHistory();
}

function systemMsg(text) {
  const div = document.createElement('div');
  div.className = 'msg system';
  div.textContent = text;
  els.history.appendChild(div);
  scrollHistory();
}

function scrollHistory() { els.history.scrollTop = els.history.scrollHeight; }

// ──────────────────────────────────────────────────────────────────
// SSE events from Go
// ──────────────────────────────────────────────────────────────────
function startEventStream() {
  const es = new EventSource('/api/events?token=' + encodeURIComponent(TOKEN));
  es.onopen = () => {
    els.connStatus.textContent = 'connected';
    els.connStatus.className = 'status connected';
  };
  es.onerror = () => {
    els.connStatus.textContent = 'reconnecting…';
    els.connStatus.className = 'status disconnected';
  };
  es.onmessage = ev => {
    let env;
    try { env = JSON.parse(ev.data); }
    catch (e) { console.warn('bad event', ev.data); return; }
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
    case 'error':
      systemMsg('server error: ' + env.error);
      break;
    default:
      console.log('event', env);
  }
}

// ──────────────────────────────────────────────────────────────────
// Per-peer connection wrapper
// ──────────────────────────────────────────────────────────────────
//
// peer = {
//   hash, sessionId, pc (RTCPeerConnection), dc (DataChannel),
//   state ('connecting'|'open'|'failed'|'closed'),
//   incomingFiles: Map<id, {name, size, parts, received}>,
//   role ('initiator'|'responder')
// }

const ICE_SERVERS = [
  { urls: 'stun:stun.l.google.com:19302' },
];

async function ensurePeer(hash) {
  let p = STATE.peers.get(hash);
  if (p && (p.state === 'open' || p.state === 'connecting')) return p;
  // Initiator path: ask Go to open a signaling session.
  const resp = await api('/api/session/open', { method: 'POST', body: { peer: hash } });
  return setupPeer(hash, resp.session_id, 'initiator');
}

function setupPeer(hash, sessionId, role) {
  const existing = STATE.peers.get(hash);
  if (existing) {
    try { existing.pc.close(); } catch {}
  }
  const pc = new RTCPeerConnection({ iceServers: ICE_SERVERS });
  const peer = {
    hash, sessionId, pc, role,
    state: 'connecting',
    dc: null,
    incomingFiles: new Map(),
    pendingCandidates: [],
    haveRemoteDesc: false,
  };
  STATE.peers.set(hash, peer);
  renderContacts();

  pc.onicecandidate = ev => {
    if (!ev.candidate) return;
    sendSignal(peer, 'ice', JSON.stringify(ev.candidate.toJSON()));
  };
  pc.onconnectionstatechange = () => {
    if (pc.connectionState === 'connected') {
      peer.state = 'open';
    } else if (pc.connectionState === 'failed' || pc.connectionState === 'closed') {
      peer.state = 'failed';
    }
    renderContacts();
  };
  pc.ontrack = ev => {
    els.remoteVideo.srcObject = ev.streams[0];
    els.callPane.hidden = false;
    els.hangupBtn.hidden = false;
  };
  pc.ondatachannel = ev => attachDataChannel(peer, ev.channel);

  if (role === 'initiator') {
    const dc = pc.createDataChannel('udisend', { ordered: true });
    attachDataChannel(peer, dc);
    pc.createOffer().then(offer => {
      return pc.setLocalDescription(offer);
    }).then(() => sendSignal(peer, 'offer', pc.localDescription.sdp))
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
    systemMsg(`session with ${aliasOf(peer.hash)} is open`);
  };
  dc.onclose = () => {
    peer.state = 'closed';
    renderContacts();
  };
  dc.onmessage = ev => onDataChannelMessage(peer, ev.data);
}

async function handleIncomingSession(peerHash, sessionId) {
  // Server-initiated session: we are the responder.
  setupPeer(peerHash, sessionId, 'responder');
  // Make sure this contact is in our list (auto-add ghost contact if not).
  if (!STATE.contacts.find(c => c.hash === peerHash)) {
    STATE.contacts.push({ hash: peerHash, alias: '', fingerprint: '', verified: false });
    renderContacts();
  }
}

async function handleSignalRecv(peerHash, sessionId, kind, payload) {
  const peer = STATE.peers.get(peerHash);
  if (!peer) {
    console.warn('signal for unknown peer', peerHash);
    return;
  }
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
      if (peer.haveRemoteDesc) {
        await peer.pc.addIceCandidate(cand);
      } else {
        peer.pendingCandidates.push(cand);
      }
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
    try { await peer.pc.addIceCandidate(c); }
    catch (e) { console.warn('flush candidate', e); }
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
  try { peer.pc.close(); } catch {}
  STATE.peers.delete(peerHash);
  renderContacts();
}

// ──────────────────────────────────────────────────────────────────
// DataChannel — application-level chat protocol (JSON over text frames)
// ──────────────────────────────────────────────────────────────────
//
// Wire shapes (kind ∈ {'text','file_offer','file_chunk','file_end'}):
//   text:        { kind:'text', body, ts, id }
//   file_offer:  { kind:'file_offer', id, name, size, mime }
//   file_chunk:  ArrayBuffer — first 16 bytes is hex id, rest is payload
//                (we send these as binary frames for efficiency).
//   file_end:    { kind:'file_end', id }

function onDataChannelMessage(peer, data) {
  if (typeof data === 'string') {
    let msg;
    try { msg = JSON.parse(data); } catch (e) { console.warn('bad json', data); return; }
    if (msg.kind === 'text') {
      addHistoryRow('in', msg.body, new Date(msg.ts || Date.now()), 1);
      persistHistory(peer.hash, 'in', 1, msg.body, msg.ts);
    } else if (msg.kind === 'file_offer') {
      peer.incomingFiles.set(msg.id, { name: msg.name, size: msg.size, mime: msg.mime, parts: [] });
      addHistoryRow('in', `📎 incoming file: ${msg.name} (${humanSize(msg.size)})`, new Date(), 100);
    } else if (msg.kind === 'file_end') {
      const f = peer.incomingFiles.get(msg.id);
      if (!f) return;
      const blob = new Blob(f.parts, { type: f.mime || 'application/octet-stream' });
      const url = URL.createObjectURL(blob);
      const div = document.createElement('div');
      div.className = 'msg file in';
      const a = document.createElement('a');
      a.href = url; a.download = f.name; a.textContent = `↓ ${f.name} (${humanSize(blob.size)})`;
      div.appendChild(a);
      els.history.appendChild(div);
      scrollHistory();
      peer.incomingFiles.delete(msg.id);
      persistHistory(peer.hash, 'in', 100, `received file ${f.name}`, Date.now() * 1e6);
    }
    return;
  }
  // Binary frame: first 16 bytes = id (utf-8), rest = chunk.
  const view = new Uint8Array(data);
  const id = new TextDecoder().decode(view.slice(0, 16));
  const f = peer.incomingFiles.get(id);
  if (!f) return;
  f.parts.push(view.slice(16));
}

async function sendText(peer, text) {
  if (!peer.dc || peer.dc.readyState !== 'open') {
    systemMsg('Not connected yet — message not sent.');
    return;
  }
  const id = crypto.randomUUID();
  const ts = Date.now() * 1e6;
  peer.dc.send(JSON.stringify({ kind: 'text', body: text, ts, id }));
  addHistoryRow('out', text, new Date(ts / 1e6), 1);
  await persistHistory(peer.hash, 'out', 1, text, ts);
}

async function sendFile(peer, file) {
  if (!peer.dc || peer.dc.readyState !== 'open') {
    systemMsg('Not connected — file not sent.');
    return;
  }
  const id = crypto.randomUUID().replaceAll('-', '').slice(0, 16);
  peer.dc.send(JSON.stringify({ kind: 'file_offer', id, name: file.name, size: file.size, mime: file.type }));
  const div = document.createElement('div');
  div.className = 'msg file out';
  div.textContent = `↑ ${file.name} (${humanSize(file.size)})`;
  els.history.appendChild(div);
  scrollHistory();
  // Stream chunks of 16 KiB.
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
  } catch (e) {
    console.warn('persist', e);
  }
}

// ──────────────────────────────────────────────────────────────────
// Calls
// ──────────────────────────────────────────────────────────────────
let localStream = null;

async function startCall() {
  if (!STATE.selectedHash) return;
  const peer = STATE.peers.get(STATE.selectedHash);
  if (!peer || peer.state !== 'open') {
    systemMsg('Not connected — open chat first to establish a session.');
    return;
  }
  try {
    localStream = await navigator.mediaDevices.getUserMedia({ audio: true, video: true });
  } catch (e) {
    systemMsg('Camera/mic access denied: ' + e.message);
    return;
  }
  els.localVideo.srcObject = localStream;
  els.callPane.hidden = false;
  els.hangupBtn.hidden = false;
  for (const track of localStream.getTracks()) {
    peer.pc.addTrack(track, localStream);
  }
  // Renegotiate — adding tracks needs a new offer.
  const offer = await peer.pc.createOffer();
  await peer.pc.setLocalDescription(offer);
  sendSignal(peer, 'offer', peer.pc.localDescription.sdp);
  systemMsg('Calling…');
}

function hangup() {
  if (!STATE.selectedHash) return;
  const peer = STATE.peers.get(STATE.selectedHash);
  if (peer) {
    for (const sender of peer.pc.getSenders()) {
      if (sender.track) sender.track.stop();
    }
  }
  if (localStream) {
    for (const t of localStream.getTracks()) t.stop();
    localStream = null;
  }
  els.localVideo.srcObject = null;
  els.remoteVideo.srcObject = null;
  els.callPane.hidden = true;
  els.hangupBtn.hidden = true;
  systemMsg('Call ended.');
}

// ──────────────────────────────────────────────────────────────────
// UI handlers + helpers
// ──────────────────────────────────────────────────────────────────
function attachUIHandlers() {
  els.addContact.addEventListener('click', showAddContactModal);
  els.sendBtn.addEventListener('click', () => {
    const text = els.msgInput.value.trim();
    if (!text || !STATE.selectedHash) return;
    els.msgInput.value = '';
    ensurePeer(STATE.selectedHash).then(p => sendText(p, text)).catch(e => systemMsg('send: ' + e.message));
  });
  els.msgInput.addEventListener('keydown', e => {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); els.sendBtn.click(); }
  });
  els.fileInput.addEventListener('change', () => {
    const file = els.fileInput.files?.[0];
    if (!file || !STATE.selectedHash) return;
    ensurePeer(STATE.selectedHash).then(p => sendFile(p, file)).catch(e => systemMsg('file: ' + e.message));
    els.fileInput.value = '';
  });
  els.callBtn.addEventListener('click', startCall);
  els.hangupBtn.addEventListener('click', hangup);
  els.verifyBtn.addEventListener('click', async () => {
    if (!STATE.selectedHash) return;
    const c = STATE.contacts.find(x => x.hash === STATE.selectedHash);
    if (!c) return;
    await api('/api/contacts/verify', { method: 'POST', body: { hash: c.hash, verified: !c.verified } });
    await loadSnapshot();
    selectContact(c.hash);
  });
}

function showAddContactModal() {
  const root = els.modalRoot;
  root.innerHTML = `
    <div class="modal-backdrop">
      <div class="modal">
        <h3>Add contact</h3>
        <label>Destination hash (32 hex chars)</label>
        <input id="modal-hash" placeholder="89f3a7b…" />
        <label>Alias</label>
        <input id="modal-alias" placeholder="alice" />
        <div class="modal-actions">
          <button id="modal-cancel">Cancel</button>
          <button id="modal-add" class="primary">Add</button>
        </div>
      </div>
    </div>
  `;
  document.getElementById('modal-cancel').onclick = () => (root.innerHTML = '');
  document.getElementById('modal-add').onclick = async () => {
    const hash = document.getElementById('modal-hash').value.trim();
    const alias = document.getElementById('modal-alias').value.trim();
    try {
      await api('/api/contacts/add', { method: 'POST', body: { hash, alias } });
      root.innerHTML = '';
      await loadSnapshot();
    } catch (e) {
      alert(e.message);
    }
  };
}

function aliasOf(hash) {
  const c = STATE.contacts.find(x => x.hash === hash);
  return c?.alias || hash.slice(0, 12) + '…';
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, c => ({ '&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;' }[c]));
}

function humanSize(b) {
  if (b < 1024) return `${b} B`;
  if (b < 1024 * 1024) return `${(b/1024).toFixed(1)} KiB`;
  return `${(b/1024/1024).toFixed(1)} MiB`;
}
