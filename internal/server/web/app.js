'use strict'

// The link is /t/{token}/. The token authorizes every API call; the key the
// friend types is the second factor.
const token = location.pathname.split('/')[2] || ''
let key = ''
let session = '' // this tab's place in the lobby
let myName = ''
let busy = false

const STALL_MS = 60 * 1000
const CHUNK_TIMEOUT_MS = 12 * 60 * 1000
const POLL_MS = 2000

const $ = id => document.getElementById(id)

function show(step) {
  for (const s of document.querySelectorAll('main > section, main > div')) s.hidden = s.id !== step
}

// showSend switches the "Send a file" panel between picking, progress, done,
// and a problem with this one file.
function showSend(part) {
  for (const id of ['send-pick', 'send-progress', 'send-done', 'send-error']) $(id).hidden = id !== part
}

// --- Talking to the server ---------------------------------------------------

// Stop means retrying cannot help: the session ended, or something needs the
// people involved to sort it out. Its message is shown as is.
class Stop extends Error {}

// ApiError is a clear answer from the server refusing a request.
class ApiError extends Error {
  constructor(status, code) {
    super(code || 'HTTP ' + status)
    this.status = status
    this.code = code
  }
}

const API_TIMEOUT_MS = 30 * 1000
const MAX_SERVER_ERRORS = 8 // in a row, before giving up with a message
const MAX_ROUNDS = 3 // of sending and verifying, before suspecting the disk

const sessionGone = 'This link has stopped working. drop was probably restarted on your friend\'s computer. Ask them for the new link and key, open it, and choose the same file: it continues where it left off.'
const serverTrouble = 'drop on your friend\'s computer keeps reporting a problem. Ask them to look at its window, then open the link again and choose the same file. Nothing already sent is lost.'

const messages = {
  too_large: 'This file is bigger than your friend allowed for this session.',
  cannot_reserve: 'Your friend\'s computer doesn\'t have enough free space for this file.',
  bad_request: 'This file can\'t be sent. Is it empty?',
  too_many: 'Your friend\'s computer has taken as many files as it will this session. Ask them to restart drop.',
  cannot_resume: 'Your friend\'s computer can\'t continue this file where it left off. Ask them to look at the message in drop\'s window.',
}

// failure turns anything caught into a sentence for the page.
function failure(err) {
  if (err instanceof Stop) return err.message
  if (err instanceof ApiError && messages[err.code]) return messages[err.code]
  return 'Something went wrong (' + err.message + '). Reload the page and choose the file again.'
}

// A connection that dies silently would otherwise keep a request waiting for
// as long as the operating system allows.
function timeout() {
  return typeof AbortSignal.timeout === 'function' ? AbortSignal.timeout(API_TIMEOUT_MS) : undefined
}

function headers(extra) {
  return { Authorization: 'Bearer ' + token, 'X-Drop-Key': key, 'X-Drop-Session': session, ...extra }
}

// An empty 404 means the token no longer opens anything; a 401 means the key
// changed. Both mean drop restarted, which only a new link fixes.
function sessionError(status, body) {
  return (status === 404 && body === '') || status === 401
}

async function api(method, path, body, extra = {}) {
  const res = await fetch('/api' + path, {
    method,
    cache: 'no-store',
    headers: headers(body ? { 'Content-Type': 'application/json', ...extra } : extra),
    body: body ? JSON.stringify(body) : undefined,
    signal: timeout(),
  })
  const text = await res.text()
  if (sessionError(res.status, text)) throw new Stop(sessionGone)
  let data = null
  try { data = text ? JSON.parse(text) : null } catch {}
  if (!res.ok) throw new ApiError(res.status, data && data.error)
  return data
}

// retry runs fn until it succeeds. Network trouble is retried for as long as
// it lasts. Server errors are retried for a while and then reported, so a
// lasting problem shows a message instead of spinning. Refusals end at once.
async function retry(fn) {
  for (let attempt = 0, serverErrors = 0; ; attempt++) {
    try {
      return await fn()
    } catch (err) {
      if (err instanceof Stop) throw err
      if (err instanceof ApiError && err.status < 500) throw err
      if (err instanceof ApiError && ++serverErrors >= MAX_SERVER_ERRORS) throw new Stop(serverTrouble)
      await sleep(backoff(attempt))
    }
  }
}

// Retries are cheap, so the wait stays short: when the link comes back, every
// worker should be sending again within seconds.
function backoff(attempt) {
  const base = Math.min(10000, 1000 * 2 ** attempt)
  return base / 2 + Math.random() * base / 2
}

const sleep = ms => new Promise(r => setTimeout(r, ms))

async function sha256Hex(buf) {
  const sum = new Uint8Array(await crypto.subtle.digest('SHA-256', buf))
  return Array.from(sum, b => b.toString(16).padStart(2, '0')).join('')
}

// putChunk sends one chunk with XMLHttpRequest, which (unlike fetch) reports
// upload progress. It never throws: it resolves with what happened. A send
// that makes no progress for a minute is abandoned so it can be retried.
function putChunk(id, index, buf, hash, onProgress) {
  return new Promise(resolve => {
    const x = new XMLHttpRequest()
    let last = Date.now()
    const watchdog = setInterval(() => {
      if (Date.now() - last > STALL_MS) x.abort()
    }, 5000)
    const finish = result => { clearInterval(watchdog); resolve(result) }

    x.open('PUT', '/api/uploads/' + id + '/chunks/' + index)
    for (const [k, v] of Object.entries(headers({ 'X-Chunk-SHA256': hash }))) x.setRequestHeader(k, v)
    x.timeout = CHUNK_TIMEOUT_MS
    x.upload.onprogress = e => { last = Date.now(); onProgress(e.loaded) }
    x.onload = () => {
      let code = ''
      try { code = JSON.parse(x.responseText).error } catch {}
      finish({
        status: x.status,
        code,
        empty: x.responseText === '',
        retryAfter: Number(x.getResponseHeader('Retry-After')) || 1,
      })
    }
    x.onerror = x.ontimeout = x.onabort = () => finish({ status: 0 })
    x.send(buf)
  })
}

// --- The upload --------------------------------------------------------------

class Upload {
  constructor(file, info) {
    this.file = file
    this.id = info.id
    this.chunkSize = info.chunkSize
    this.chunks = info.chunks
    this.parallel = info.parallel
    this.setDone(info.done)
    this.sending = new Map() // chunk index -> bytes sent so far
    this.waiting = 0 // workers backing off after a network failure
  }

  chunkLen(i) {
    return Math.min(this.chunkSize, this.file.size - i * this.chunkSize)
  }

  setDone(indices) {
    this.done = new Set(indices)
    this.doneBytes = 0
    for (const i of this.done) this.doneBytes += this.chunkLen(i)
  }

  sentBytes() {
    let n = this.doneBytes
    for (const v of this.sending.values()) n += v
    return n
  }

  // run sends every missing chunk, asks the server to verify, and repeats if
  // verification reopened any chunks. It resolves with the final status.
  async run() {
    for (let round = 1; ; round++) {
      await this.sendMissing()
      ui.phase('finishing')
      try {
        await retry(() => api('POST', '/uploads/' + this.id + '/finish'))
      } catch (err) {
        // A finish whose answer was lost can be retried after verification
        // already reopened chunks; finding them missing means resend, not fail.
        if (!(err instanceof ApiError && err.code === 'missing_chunks')) throw err
      }
      const st = await waitForResult(this.id)
      if (st.state !== 'receiving') return st
      if (round >= MAX_ROUNDS) throw new Stop('Your friend\'s computer keeps finding damaged pieces after saving them, which points to a problem with its disk. Ask them to check the drive drop saves to.')
      this.setDone(st.done)
      this.resent = true
    }
  }

  async sendMissing() {
    ui.track(this)
    ui.phase(this.resent ? 'resending' : 'sending')
    const queue = []
    for (let i = 0; i < this.chunks; i++) if (!this.done.has(i)) queue.push(i)
    const worker = async () => {
      for (let i = queue.shift(); i !== undefined; i = queue.shift()) {
        const requeue = await this.send(i)
        if (requeue) queue.push(i)
      }
    }
    await Promise.all(Array.from({ length: Math.min(this.parallel, queue.length) }, worker))
  }

  // send uploads one chunk, retrying until it lands. It returns true when the
  // server is still busy with an earlier attempt at this chunk, so the worker
  // can move on and come back to it.
  async send(i) {
    let buf = null
    let hash = ''
    for (let attempt = 0, mismatches = 0, serverErrors = 0; ; attempt++) {
      if (!buf) {
        try {
          buf = await this.file.slice(i * this.chunkSize, i * this.chunkSize + this.chunkLen(i)).arrayBuffer()
        } catch {
          throw new Stop('The file can\'t be read anymore. Was it moved, renamed, or changed? Choose it again to continue.')
        }
        hash = await sha256Hex(buf)
      }

      this.sending.set(i, 0)
      const r = await putChunk(this.id, i, buf, hash, n => this.sending.set(i, n))
      this.sending.delete(i)

      if (r.status === 204) {
        this.done.add(i)
        this.doneBytes += this.chunkLen(i)
        return false
      }
      if (sessionError(r.status, r.empty ? '' : 'x')) throw new Stop(sessionGone)
      if (r.status === 409 && r.code === 'chunk_in_flight') {
        await sleep(5000)
        return true
      }
      if (r.status === 503) {
        await sleep(r.retryAfter * 1000)
        continue
      }
      if (r.status === 422) {
        // The bytes arrived but didn't match. Read them from the file again;
        // if that keeps failing, the file is changing under us.
        if (++mismatches >= 3) throw new Stop('The file seems to be changing while it is sent. Close any program using it, then choose it again.')
        buf = null
        continue
      }
      // 0 is a network failure or our own watchdog; 408 is the server cutting
      // off a chunk that stalled. Both are the network, so try again.
      if (r.status === 0 || r.status === 408 || r.status >= 500) {
        if (r.status >= 500 && ++serverErrors >= MAX_SERVER_ERRORS) throw new Stop(serverTrouble)
        const wait = backoff(attempt)
        this.waiting++
        ui.retryingIn(wait)
        await sleep(wait)
        this.waiting--
        continue
      }
      throw new Stop('Something went wrong (' + (r.code || 'HTTP ' + r.status) + '). Reload the page and choose the file again.')
    }
  }
}

// waitForResult polls while the server reads the file back from its disk.
async function waitForResult(id) {
  for (;;) {
    const st = await retry(() => api('GET', '/uploads/' + id))
    if (st.state === 'verifying') {
      ui.verifying(st)
      await sleep(POLL_MS)
      continue
    }
    if (st.state === 'failed') throw new Stop('Your friend\'s computer could not save the file. Ask them to restart drop, then open the new link and choose the same file. Nothing already sent is lost.')
    return st
  }
}

async function start(file) {
  busy = true
  stayAwake()
  ui.begin(file)
  try {
    const info = await retry(() => api('POST', '/uploads', { name: file.name, size: file.size, modified: file.lastModified }))
    let st
    if (info.state === 'receiving') {
      st = await new Upload(file, info).run()
    } else {
      st = await waitForResult(info.id)
    }
    ui.done(file, st)
  } catch (err) {
    // A link that stopped working ends the whole page; anything else is a
    // problem with this one file, and the lobby stays.
    if (err instanceof Stop && err.message === sessionGone) fatal(sessionGone)
    else ui.error(failure(err))
  } finally {
    busy = false
    ui.stop()
    releaseAwake()
  }
}

function fatal(text) {
  lobby.stop()
  $('error-text').textContent = text
  show('step-error')
}

// --- Screen ------------------------------------------------------------------

const ui = {
  timer: 0,
  samples: [],
  phaseName: '',
  retryUntil: 0,

  begin(file) {
    $('up-name').textContent = file.name
    $('up-size').textContent = formatBytes(file.size)
    $('up-bar').value = 0
    for (const id of ['up-pct', 'up-bytes', 'up-rate', 'up-eta']) $(id).textContent = ''
    $('up-state').textContent = 'Getting ready...'
    showSend('send-progress')
  },

  track(up) {
    const size = up.file.size
    this.samples = []
    clearInterval(this.timer)
    // Show where a resumed upload stands right away, not a second later.
    this.progress(up.sentBytes(), size, 0)
    this.timer = setInterval(() => {
      const sent = up.sentBytes()
      this.samples.push(sent)
      if (this.samples.length > 11) this.samples.shift()
      const n = this.samples.length
      const rate = n > 1 ? (this.samples[n - 1] - this.samples[0]) / (n - 1) : 0
      this.progress(sent, size, rate)
      if (this.phaseName === 'sending' || this.phaseName === 'resending') {
        const wait = this.retryUntil - Date.now()
        // A dropped link often fails silently: nothing errors, bytes just stop.
        const stalled = n >= 6 && this.samples[n - 1] === this.samples[n - 6]
        $('up-state').textContent = up.waiting > 0 && wait > 0
          ? 'Connection lost. Trying again in ' + Math.ceil(wait / 1000) + ' s...'
          : stalled ? 'Nothing is getting through right now. Waiting for the connection to come back...'
          : this.phaseName === 'resending' ? 'Step 1 of 2: re-sending a few pieces that didn\'t check out...' : 'Step 1 of 2: sending...'
      }
    }, 1000)
  },

  progress(done, size, rate, verb = 'sent') {
    $('up-bar').value = size ? done / size : 0
    $('up-pct').textContent = (size ? (100 * done / size).toFixed(1) : '0.0') + '%'
    $('up-bytes').textContent = formatBytes(done) + ' of ' + formatBytes(size) + ' ' + verb
    $('up-rate').textContent = rate > 0 ? formatBytes(rate) + '/s' : ''
    $('up-eta').textContent = rate > 0 ? formatDuration((size - done) / rate) + ' left' : ''
  },

  phase(name) {
    this.phaseName = name
    if (name === 'finishing') $('up-state').textContent = 'All sent. Asking your friend\'s computer to check it...'
  },

  retryingIn(ms) {
    this.retryUntil = Math.max(this.retryUntil, Date.now() + ms)
  },

  verifying(st) {
    clearInterval(this.timer)
    this.phaseName = 'verifying'
    const prev = this.lastVerify
    this.lastVerify = { at: Date.now(), bytes: st.verified }
    const rate = prev && st.verified > prev.bytes ? (st.verified - prev.bytes) / ((Date.now() - prev.at) / 1000) : 0
    this.progress(st.verified, st.size, rate, 'checked')
    $('up-state').textContent = 'Step 2 of 2: everything arrived. Your friend\'s computer is reading it back to make sure every byte is right. Large files take a while; keep this tab open.'
  },

  stop() {
    clearInterval(this.timer)
    this.lastVerify = null
  },

  done(file, st) {
    $('done-name').textContent = st.name
    $('done-hash').textContent = st.sha256
    const isMac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
    $('done-command').textContent = isMac
      ? 'shasum -a 256 "' + file.name + '"'
      : 'certutil -hashfile "' + file.name + '" SHA256'
    showSend('send-done')
  },

  error(text) {
    $('send-error-text').textContent = text
    showSend('send-error')
  },
}

// --- The lobby ---------------------------------------------------------------

// The lobby polls every couple of seconds. Each poll is also this tab's
// heartbeat, and its round trip, reported on the next poll, is the latency
// everyone sees next to this person's name.
const lobby = {
  running: false,
  rtt: 0,

  start() {
    if (this.running) return
    this.running = true
    this.loop()
  },

  stop() {
    this.running = false
  },

  async loop() {
    while (this.running) {
      await this.poll()
      await sleep(POLL_MS)
    }
  },

  async poll() {
    const started = performance.now()
    try {
      const view = await api('GET', '/lobby', null, { 'X-Drop-RTT': String(this.rtt) })
      this.rtt = Math.round(performance.now() - started)
      renderPeople(view.people)
      renderTransfers(view.transfers)
    } catch (err) {
      if (err instanceof Stop) return fatal(err.message)
      // Away too long and forgotten: join again under the same name.
      if (err instanceof ApiError && err.code === 'unknown_session') await join(myName, true)
      // Network trouble: the next poll tries again.
    }
  },
}

// el builds an element whose text is set as text, never parsed as markup:
// names and file names come from other people.
function el(tag, className, text) {
  const e = document.createElement(tag)
  if (className) e.className = className
  if (text !== undefined) e.textContent = text
  return e
}

function renderPeople(people) {
  const list = $('people')
  list.replaceChildren(...people.map(p => {
    const li = el('li', p.idle ? 'person idle' : 'person')
    const who = el('span', 'who', p.name)
    if (p.you) who.append(el('span', 'badge', 'you'))
    if (p.idle) who.append(el('span', 'badge', 'away'))
    li.append(who, el('span', 'muted', p.ip), el('span', 'latency', p.rtt > 0 ? p.rtt + ' ms' : '...'))
    return li
  }))
}

const stateLabels = {
  receiving: 'sending',
  verifying: 'checking on disk',
  done: 'done',
  failed: 'needs attention',
}

function renderTransfers(transfers) {
  $('no-transfers').hidden = transfers.length > 0
  $('transfers').replaceChildren(...transfers.slice().reverse().map(t => {
    const li = el('li', 'transfer ' + t.state)
    const head = el('div', 'transfer-head')
    head.append(el('span', 'file-name', t.name), el('span', 'muted', t.sender ? 'from ' + t.sender : ''))
    li.append(head)

    const checking = t.state === 'verifying'
    const done = checking ? t.verified : t.received
    const bar = el('progress')
    bar.max = 1
    bar.value = t.state === 'done' ? 1 : t.size ? done / t.size : 0
    li.append(bar)

    const stats = el('div', 'stats')
    stats.append(el('span', 'state-label', stateLabels[t.state] || t.state))
    if (t.state === 'done') {
      stats.append(el('span', '', formatBytes(t.size)))
    } else {
      stats.append(el('span', '', formatBytes(done) + ' of ' + formatBytes(t.size) + (checking ? ' checked' : '')))
      if (t.rate > 0) stats.append(el('span', '', formatBytes(t.rate) + '/s'), el('span', '', formatDuration((t.size - done) / t.rate) + ' left'))
    }
    li.append(stats)
    if (t.sha256) li.append(el('code', 'hash small', 'SHA-256 ' + t.sha256))
    return li
  }))
}

function formatBytes(n) {
  if (n >= 1e12) return (n / 1e12).toFixed(2) + ' TB'
  if (n >= 1e9) return (n / 1e9).toFixed(1) + ' GB'
  if (n >= 1e6) return (n / 1e6).toFixed(1) + ' MB'
  if (n >= 1e3) return (n / 1e3).toFixed(0) + ' KB'
  return Math.round(n) + ' B'
}

function formatDuration(seconds) {
  const s = Math.max(0, Math.round(seconds))
  const h = Math.floor(s / 3600)
  const m = Math.floor(s / 60) % 60
  if (h > 0) return h + 'h ' + String(m).padStart(2, '0') + 'm'
  if (m > 0) return m + 'm ' + String(s % 60).padStart(2, '0') + 's'
  return s + 's'
}

// --- Keeping the computer awake ----------------------------------------------

let wakeLock = null

async function stayAwake() {
  try {
    if (navigator.wakeLock && document.visibilityState === 'visible') wakeLock = await navigator.wakeLock.request('screen')
  } catch {}
}

function releaseAwake() {
  if (wakeLock) wakeLock.release().catch(() => {})
  wakeLock = null
}

// The browser drops the wake lock whenever the tab is hidden; take it back.
document.addEventListener('visibilitychange', () => {
  if (busy && document.visibilityState === 'visible') stayAwake()
})

window.addEventListener('beforeunload', e => {
  if (busy) {
    e.preventDefault()
    e.returnValue = ''
  }
})

// --- Key and file pickers ----------------------------------------------------

// What this tab remembers, per link: a restarted drop makes a new link and key,
// and trying an old key against it would set off the wrong-key warning.
function remembered() {
  try { return JSON.parse(sessionStorage.getItem('drop.' + token)) || {} } catch { return {} }
}

function remember() {
  try { sessionStorage.setItem('drop.' + token, JSON.stringify({ key, name: myName, session })) } catch {}
}

const keyErrors = {
  401: 'That key doesn\'t match. Check it with your friend; a new key is made each time drop starts.',
  404: 'This link has stopped working. drop was probably restarted; ask your friend for the new link and key.',
  429: 'The lobby is full right now. Try again in a few minutes.',
}

// join enters the lobby under a name. It returns whether that worked; unless
// quiet, a failure is explained on the key form.
async function join(name, quiet) {
  $('key-button').disabled = true
  $('key-error').textContent = ''
  let res = null
  try {
    res = await fetch('/api/join', {
      method: 'POST',
      cache: 'no-store',
      headers: headers({ 'Content-Type': 'application/json' }),
      body: JSON.stringify({ name }),
      signal: timeout(),
    })
  } catch {}
  $('key-button').disabled = false
  if (res && res.ok) {
    const joined = await res.json()
    session = joined.session
    myName = joined.name
    remember()
    return true
  }
  if (!quiet) $('key-error').textContent = keyErrors[res && res.status] || 'Can\'t reach your friend\'s computer right now. Check your connection and try again.'
  return false
}

function enterLobby() {
  show('step-lobby')
  if (!busy) showSend('send-pick')
  lobby.start()
}

$('key-form').addEventListener('submit', async e => {
  e.preventDefault()
  key = $('key').value.trim()
  if (await join($('name').value, false)) enterLobby()
})

function pick(file) {
  $('file-error').textContent = ''
  if (!file) return
  if (file.size === 0) {
    $('file-error').textContent = 'That file is empty, or it is a folder. To send a folder, zip it first.'
    return
  }
  start(file)
}

$('file').addEventListener('change', () => pick($('file').files[0]))

const zone = $('dropzone')
zone.addEventListener('dragover', e => {
  e.preventDefault()
  zone.classList.add('over')
})
zone.addEventListener('dragleave', () => zone.classList.remove('over'))
zone.addEventListener('drop', e => {
  e.preventDefault()
  zone.classList.remove('over')
  const items = e.dataTransfer.items
  const entry = items && items[0] && items[0].webkitGetAsEntry && items[0].webkitGetAsEntry()
  if (entry && entry.isDirectory) {
    $('file-error').textContent = 'Folders can\'t be sent directly. Zip the folder first, then drop the zip here.'
    return
  }
  if (e.dataTransfer.files.length > 1) {
    $('file-error').textContent = 'One file at a time, please. Drop just one.'
    return
  }
  pick(e.dataTransfer.files[0])
})

// A file dropped anywhere else would make the browser open it and leave the page.
for (const type of ['dragover', 'drop']) window.addEventListener(type, e => e.preventDefault())

for (const id of ['another', 'try-again']) {
  $(id).addEventListener('click', () => {
    $('file').value = ''
    $('file-error').textContent = ''
    showSend('send-pick')
  })
}

// --- Start -------------------------------------------------------------------

if (!window.isSecureContext || !window.crypto || !crypto.subtle || !token) {
  show('step-unsupported')
} else {
  // After a reload, rejoin quietly with what this tab remembers; the first
  // lobby poll rejoins again if the old session was forgotten.
  const saved = remembered()
  if (saved.key && saved.session) {
    key = saved.key
    myName = saved.name || ''
    session = saved.session
    $('key').value = key
    $('name').value = myName
    enterLobby()
  } else {
    show('step-key')
  }
}
