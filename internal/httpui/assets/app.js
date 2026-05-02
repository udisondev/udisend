// udisend browser-side runtime.
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
  // active call info or null
  call: null, // { peer, role: 'caller'|'callee', state, localStream, ... }
};

const els = {
  myAlias:       document.getElementById('my-alias'),
  myHash:        document.getElementById('my-hash'),
  myFp:          document.getElementById('my-fingerprint'),
  myAddr:        document.getElementById('my-address'),
  connStatus:    document.getElementById('conn-status'),
  contacts:      document.getElementById('contacts'),
  addContact:    document.getElementById('add-contact-btn'),
  chatPane:      document.getElementById('chat-pane'),
  emptyState:    document.getElementById('empty-state'),
  chatPeerAlias: document.getElementById('chat-peer-alias'),
  chatPeerHash:  document.getElementById('chat-peer-hash'),
  history:       document.getElementById('chat-history'),
  msgInput:      document.getElementById('msg-input'),
  sendBtn:       document.getElementById('send-btn'),
  fileInput:     document.getElementById('file-input'),
  callBtn:       document.getElementById('call-btn'),
  verifyBtn:     document.getElementById('verify-btn'),
  modalRoot:     document.getElementById('modal-root'),
  // call modal
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
  const headers = Object.assign({}, opts.headers || {}, { 'Authorization': 'Bearer ' + TOKEN });
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
  const ct = r.headers.get('content-type') || '';
  if (ct.includes('application/json')) return r.json();
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
  STATE.iceServers = (snap.ice_servers && snap.ice_servers.length)
    ? snap.ice_servers
    : ICE_SERVERS_FALLBACK;
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
  if (kind === 100) {
    div.className = 'msg file ' + (direction === 'in' ? 'in' : 'out');
    div.textContent = body;
  } else if (kind === 0) {
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

// Fallback used only if /api/snapshot did not surface any volunteer ICE
// servers (no public-IP network nodes in this messenger's routing table).
// The real list comes from the Go side via snapshot.ice_servers.
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
    isMakingOffer: false, // perfect-negotiation flag for renegotiation
  };
  STATE.peers.set(hash, peer);
  renderContacts();

  pc.onicecandidate = ev => {
    if (!ev.candidate) return;
    sendSignal(peer, 'ice', JSON.stringify(ev.candidate.toJSON()));
  };
  pc.onconnectionstatechange = () => {
    if (pc.connectionState === 'connected') peer.state = 'open';
    else if (pc.connectionState === 'failed' || pc.connectionState === 'closed') peer.state = 'failed';
    renderContacts();
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
    systemMsg(`session with ${aliasOf(peer.hash)} is open`);
  };
  dc.onclose = () => { peer.state = 'closed'; renderContacts(); };
  dc.onmessage = ev => onDataChannelMessage(peer, ev.data);
}

async function handleIncomingSession(peerHash, sessionId) {
  setupPeer(peerHash, sessionId, 'responder');
  if (!STATE.contacts.find(c => c.hash === peerHash)) {
    STATE.contacts.push({ hash: peerHash, alias: '', fingerprint: '', verified: false });
    renderContacts();
  }
}

async function handleSignalRecv(peerHash, sessionId, kind, payload) {
  const peer = STATE.peers.get(peerHash);
  if (!peer) { console.warn('signal for unknown peer', peerHash); return; }
  try {
    if (kind === 'offer') {
      await peer.pc.setRemoteDescription({ type: 'offer', sdp: payload });
      peer.haveRemoteDesc = true;
      // If we're in an active call as the callee, our local tracks have
      // already been added before we accepted; createAnswer pulls them in.
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
}

// ──────────────────────────────────────────────────────────────────
// DataChannel application protocol
// Wire shapes:
//   text:        { kind:'text', body, ts, id }
//   file_offer:  { kind:'file_offer', id, name, size, mime }
//   file_chunk:  binary frame; first 16 bytes = id, rest = payload
//   file_end:    { kind:'file_end', id }
//   call_invite: { kind:'call_invite' }
//   call_accept: { kind:'call_accept' }
//   call_reject: { kind:'call_reject' }
//   call_end:    { kind:'call_end' }
// ──────────────────────────────────────────────────────────────────

function onDataChannelMessage(peer, data) {
  if (typeof data === 'string') {
    let msg;
    try { msg = JSON.parse(data); } catch { return; }
    switch (msg.kind) {
      case 'text': {
        addHistoryRow('in', msg.body, new Date(msg.ts || Date.now()), 1);
        persistHistory(peer.hash, 'in', 1, msg.body, msg.ts);
        break;
      }
      case 'file_offer': {
        peer.incomingFiles.set(msg.id, { name: msg.name, size: msg.size, mime: msg.mime, parts: [] });
        addHistoryRow('in', `📎 incoming file: ${msg.name} (${humanSize(msg.size)})`, new Date(), 100);
        break;
      }
      case 'file_end': {
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
  // A remote track arrives. If we're in a call with this peer, route the
  // stream to the call modal; otherwise it's likely an extra m-section
  // we don't care about yet.
  if (!STATE.call || STATE.call.peer !== peer.hash) return;
  const stream = ev.streams && ev.streams[0];
  if (!stream) return;
  els.callRemote.srcObject = stream;
  els.callPipVideo.srcObject = stream;
}

// ──────────────────────────────────────────────────────────────────
// Calls — Telegram-style state machine
// ──────────────────────────────────────────────────────────────────
//
// States in STATE.call.state:
//   'outgoing'  — caller waiting for accept
//   'incoming'  — callee shown the modal, ringtone playing
//   'active'    — accepted, media flowing
// Modes (only meaningful while 'active'):
//   'floating' (default), 'fullscreen', 'minimized'

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
    // Wait briefly for DC to open.
    const ok = await waitFor(() => peer.dc && peer.dc.readyState === 'open', 8000);
    if (!ok) { systemMsg('peer not connected'); return; }
  }
  STATE.call = { peer: peer.hash, role: 'caller', state: 'outgoing', startedAt: Date.now(), localStream: null };
  showCallModal('outgoing', aliasOf(peer.hash), 'Calling…');
  dcSend(peer, { kind: 'call_invite' });
  // Auto-cancel after 45s if peer doesn't pick up.
  STATE.call.ringTimeout = setTimeout(() => {
    if (STATE.call && STATE.call.state === 'outgoing') endCallLocal('no answer');
  }, 45_000);
}

function onCallInvite(peer) {
  if (STATE.call) {
    // Already busy — auto-decline.
    dcSend(peer, { kind: 'call_reject' });
    return;
  }
  STATE.call = { peer: peer.hash, role: 'callee', state: 'incoming', startedAt: Date.now(), localStream: null };
  showCallModal('incoming', aliasOf(peer.hash), 'Incoming call');
  startRingtone();
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
  // Add tracks NOW so when caller's renegotiation offer arrives, our
  // createAnswer pulls these into the answer SDP.
  for (const t of stream.getTracks()) peer.pc.addTrack(t, stream);
  dcSend(peer, { kind: 'call_accept' });
  setCallState('active', 'floating');
  STATE.call.state = 'active';
  els.callPeerStatus.textContent = 'Connected — waiting for offer…';
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
  els.callPeerStatus.textContent = 'Connecting…';
  // Renegotiate: we (caller) drive the offer. Both sides have tracks now;
  // the answer will flip the m-section direction to sendrecv on both ends.
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
  if (STATE.call) {
    if (STATE.call.localStream) {
      for (const t of STATE.call.localStream.getTracks()) t.stop();
    }
    if (STATE.call.ringTimeout) clearTimeout(STATE.call.ringTimeout);
    // Detach call media tracks from the PC so the next call starts clean.
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
  els.callPeerStatus.textContent = statusText;
  els.callPreLabel.textContent = statusText;
  setCallState(state, 'floating');
}

function toggleFullscreen() {
  if (!STATE.call || STATE.call.state !== 'active') return;
  if (els.callRoot.dataset.mode === 'fullscreen') {
    els.callRoot.dataset.mode = 'floating';
  } else {
    els.callRoot.dataset.mode = 'fullscreen';
  }
}

function toggleMinimize() {
  if (!STATE.call || STATE.call.state !== 'active') return;
  if (els.callRoot.dataset.mode === 'minimized') {
    els.callRoot.dataset.mode = 'floating';
  } else {
    els.callRoot.dataset.mode = 'minimized';
  }
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
// Ringtone — synthesized via WebAudio so we don't ship an asset.
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
  } catch (e) { /* autoplay policy may suppress; not critical */ }
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

  els.callBtn.addEventListener('click', placeCall);

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

  // Esc to exit fullscreen / minimize.
  document.addEventListener('keydown', e => {
    if (e.key === 'Escape' && STATE.call && STATE.call.state === 'active') {
      if (els.callRoot.dataset.mode === 'fullscreen') { e.preventDefault(); els.callRoot.dataset.mode = 'floating'; }
    }
  });

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
    } catch (e) { alert(e.message); }
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
