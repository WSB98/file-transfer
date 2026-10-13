// End-to-end checks of drop in real browsers, on this machine only.
//
//   go build -o drop.exe ./cmd/drop      (or: -o drop on macOS/Linux)
//   node dev/e2e/run.mjs [--drop ./drop.exe] [--only lobby,resume,restart,outage,firefox] [--shots DIR]
//
// Needs Node 22+ and at least one of Chrome, Edge, or Firefox. Everything binds
// to 127.0.0.1, and every file it makes lives in a temp folder it removes.
import { createHash, randomBytes } from 'node:crypto'
import { createReadStream, existsSync, mkdtempSync, openSync, rmSync, writeSync, closeSync, readdirSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { parseArgs } from 'node:util'
import { startDrop, tempInbox, sleep } from './lib/drop.mjs'
import { launchChromium } from './lib/chromium.mjs'
import { launchFirefox } from './lib/firefox.mjs'
import { startProxy } from './lib/proxy.mjs'

const repo = resolve(import.meta.dirname, '..', '..')
const { values: opts } = parseArgs({ options: {
  drop: { type: 'string', default: join(repo, process.platform === 'win32' ? 'drop.exe' : 'drop') },
  only: { type: 'string', default: 'lobby,resume,restart,outage,firefox' },
  shots: { type: 'string' },
} })

const browsers = {
  chromium: [
    'C:/Program Files/Google/Chrome/Application/chrome.exe',
    'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe',
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    'google-chrome', 'chromium', 'chromium-browser', 'microsoft-edge',
  ],
  firefox: ['C:/Program Files/Mozilla Firefox/firefox.exe', '/Applications/Firefox.app/Contents/MacOS/firefox', 'firefox'],
}

function find(kind) {
  for (const c of browsers[kind]) {
    if (c.includes('/') ? existsSync(c) : spawnSync(process.platform === 'win32' ? 'where' : 'which', [c]).status === 0) return c
  }
  return null
}

const MiB = 1024 * 1024
const work = mkdtempSync(join(tmpdir(), 'drop-e2e-'))

function makeFile(name, size) {
  const path = join(work, name)
  const fd = openSync(path, 'w')
  for (let left = size; left > 0; left -= 16 * MiB) writeSync(fd, randomBytes(Math.min(left, 16 * MiB)))
  closeSync(fd)
  return path
}

const sha256 = path => new Promise(r => {
  const h = createHash('sha256')
  createReadStream(path).on('data', d => h.update(d)).on('end', () => r(h.digest('hex')))
})

// --- The page, as a person sees it -------------------------------------------

// Inside the lobby, the "Send a file" panel's state stands in for the step:
// picking, sending, done, or a problem with the file.
const sendSteps = { 'send-pick': 'step-file', 'send-progress': 'step-upload', 'send-done': 'step-done', 'send-error': 'step-error' }

const page = {
  step: async b => {
    const id = await b.evaluate(`(function () {
      var visible = function (sel) { return Array.from(document.querySelectorAll(sel)).filter(function (x) { return !x.hidden })[0] }
      var top = visible('main > section, main > div')
      if (!top || top.id !== 'step-lobby') return top ? top.id : ''
      var send = visible('#send-pick, #send-progress, #send-done, #send-error')
      return send ? send.id : ''
    })()`)
    return sendSteps[id] || id
  },
  text: (b, id) => b.evaluate(`document.getElementById(${JSON.stringify(id)}).textContent`),
  errorText: async b => (await page.text(b, 'send-error-text')) || (await page.text(b, 'error-text')),
  async waitFor(b, step, ms = 30000) {
    for (const end = Date.now() + ms; Date.now() < end; await sleep(200)) if (await page.step(b) === step) return
    throw new Error(`never reached ${step}; on ${await page.step(b)}`)
  },
  async typeKey(b, key, name = 'Tester') {
    await b.evaluate(`document.getElementById('name').value = ${JSON.stringify(name)}; document.getElementById('key').value = ${JSON.stringify(key)}; document.getElementById('key-form').requestSubmit(); true`)
  },
  // The lobby as this person sees it: names (with badges) and transfers.
  lobby: b => b.evaluate(`({
    people: Array.from(document.querySelectorAll('#people .person')).map(function (li) { return { who: li.querySelector('.who').textContent, latency: li.querySelector('.latency').textContent, html: li.innerHTML } }),
    transfers: Array.from(document.querySelectorAll('#transfers .transfer')).map(function (li) { return { name: li.querySelector('.file-name').textContent, from: li.querySelector('.transfer-head .muted').textContent, state: li.className, value: li.querySelector('progress').value, hash: (li.querySelector('code') || {}).textContent || '' } })
  })`),
  async percent(b) {
    return parseFloat(await page.text(b, 'up-pct')) || 0
  },
  // Waits for done or stopped, calling onTick about twice a second.
  async finish(b, onTick = async () => {}, ms = 10 * 60000) {
    for (const end = Date.now() + ms; Date.now() < end; await sleep(500)) {
      const step = await page.step(b)
      if (step === 'step-done' || step === 'step-error') return step
      await onTick()
    }
    const showing = await b.evaluate(`document.getElementById('up-pct').textContent + ' ' + document.getElementById('up-state').textContent`).catch(e => e.message)
    throw new Error('upload did not finish in time; the page shows: ' + showing)
  },
}

// --- Scenarios ---------------------------------------------------------------

async function resume(check) {
  const exe = find('chromium')
  if (!exe) return check.skip('no Chrome or Edge found')
  const file = makeFile('holiday photos.zip', 1024 * MiB + 12345) // odd tail: a short last chunk
  const want = await sha256(file)
  const drop = await startDrop(opts.drop, 18443)
  // 400 Mbps, so the reload lands mid-upload rather than after it.
  const proxy = startProxy(18444, 18443, 50e6)
  const b = await launchChromium(exe, 9333)
  try {
    check.that('the certificate warning appears and can be clicked through', await b.open(drop.url.replace(':18443', ':18444')))
    check.that('the page is a secure context with crypto.subtle', await b.evaluate(`isSecureContext && !!crypto.subtle`))
    await page.waitFor(b, 'step-key')
    if (opts.shots) await b.screenshot(join(opts.shots, 'key.png'))
    await page.typeKey(b, 'AAAA-BBBB-CCCC')
    await sleep(800)
    check.that('a wrong key is refused with a clear message', /doesn't match/.test(await page.text(b, 'key-error')))
    await page.typeKey(b, drop.key.toLowerCase().replace(/-/g, ' '))
    await page.waitFor(b, 'step-file')
    check.pass('a loosely typed key (lowercase, spaces) is accepted')

    await b.chooseFile(file)
    await page.waitFor(b, 'step-upload')
    while (await page.percent(b) < 35) await sleep(100)
    if (opts.shots) await b.screenshot(join(opts.shots, 'uploading.png'))
    await b.reload()
    await sleep(1500)
    await page.waitFor(b, 'step-file')
    check.pass('after a reload the key is remembered')
    await b.chooseFile(file)
    await page.waitFor(b, 'step-upload')
    await sleep(1500)
    const resumedAt = await page.percent(b)
    check.that(`re-picking the file resumes partway instead of restarting (at ${resumedAt}%)`, resumedAt >= 25 && resumedAt < 90)

    const step = await page.finish(b)
    if (opts.shots) await b.screenshot(join(opts.shots, 'done.png'))
    check.that('the upload finishes', step === 'step-done', await page.errorText(b))
    check.that("the page's SHA-256 matches the original", await page.text(b, 'done-hash') === want)
    const saved = join(drop.inbox, await page.text(b, 'done-name'))
    check.that('the saved file matches the original byte for byte', existsSync(saved) && await sha256(saved) === want)
    check.that('the inbox holds only the file and the working folder', readdirSync(drop.inbox).sort().join() === ['.incoming', 'holiday photos.zip'].join())
    const log = drop.log()
    check.that('the server log has no rejected requests or internal errors', !log.some(l => /rejected|internal error/.test(l)), log.join('\n'))
  } finally {
    await b.close()
    proxy.close()
    await drop.stop()
  }
}

async function outage(check) {
  const exe = find('chromium')
  if (!exe) return check.skip('no Chrome or Edge found')
  const file = makeFile('outage.zip', 300 * MiB + 777)
  const want = await sha256(file)
  const drop = await startDrop(opts.drop, 18445)
  const capacity = 12.5e6 // 100 Mbps, a typical home upload
  const proxy = startProxy(18446, 18445, capacity)
  const b = await launchChromium(exe, 9335)
  try {
    await b.open(drop.url.replace(':18445', ':18446'))
    await page.waitFor(b, 'step-key')
    await page.typeKey(b, drop.key)
    await page.waitFor(b, 'step-file')
    await b.chooseFile(file)
    await page.waitFor(b, 'step-upload')

    const t0 = Date.now()
    while (await page.percent(b) < 5) await sleep(200)
    const p0 = await page.percent(b), s0 = Date.now()
    await sleep(8000)
    const rate = ((await page.percent(b)) - p0) / 100 * (300 * MiB) / ((Date.now() - s0) / 1000)
    check.that(`the upload fills the link (${(rate / 1e6).toFixed(1)} of 12.5 MB/s)`, rate > 0.85 * capacity)

    const seen = new Set()
    proxy.blackhole(20)
    for (let i = 0; i < 40; i++) {
      await sleep(500)
      seen.add(await page.text(b, 'up-state'))
    }
    check.that('during a silent 20 s outage the page says so', [...seen].some(s => /Nothing is getting through|Connection lost/.test(s)), [...seen].join(' | '))

    let peak = 0
    const step = await page.finish(b, async () => { peak = Math.max(peak, await b.memory()) })
    check.that('the upload recovers and finishes', step === 'step-done', await page.errorText(b))
    check.that("the page's SHA-256 matches the original", await page.text(b, 'done-hash') === want)
    check.that(`browser memory stays modest (${(peak / 1e6).toFixed(0)} MB)`, peak < 512e6)
    check.that('the server log has no internal errors', !drop.log().some(l => /internal error/.test(l)), drop.log().join('\n'))
    check.note(`took ${((Date.now() - t0) / 1000).toFixed(0)} s including the outage`)
  } finally {
    await b.close()
    proxy.close()
    await drop.stop()
  }
}

async function restart(check) {
  const exe = find('chromium')
  if (!exe) return check.skip('no Chrome or Edge found')
  const file = makeFile('restart.zip', 512 * MiB + 4321)
  const want = await sha256(file)
  const inbox = tempInbox()
  // 200 Mbps, so drop is killed mid-upload rather than after it.
  const proxy = startProxy(18449, 18448, 25e6)
  let drop = await startDrop(opts.drop, 18448, inbox)
  const b = await launchChromium(exe, 9337)
  const viaProxy = url => url.replace(':18448', ':18449')
  try {
    await b.open(viaProxy(drop.url))
    await page.waitFor(b, 'step-key')
    await page.typeKey(b, drop.key)
    await page.waitFor(b, 'step-file')
    await b.chooseFile(file)
    await page.waitFor(b, 'step-upload')
    while (await page.percent(b) < 40) await sleep(200)

    await drop.kill()
    check.that('killing drop mid-upload leaves its data and progress in .incoming',
      readdirSync(join(inbox, '.incoming')).filter(n => /\.(part|state\.json)$/.test(n)).length === 2)
    await sleep(3000)
    check.that('while drop is down, the page says the connection is lost', /Connection lost|Nothing is getting through/.test(await page.text(b, 'up-state')))

    drop = await startDrop(opts.drop, 18448, inbox)
    check.that('the restarted banner lists the unfinished upload', /Unfinished:\s+restart\.zip/.test(drop.banner), drop.banner)

    // The sender opens the new link and types the new key.
    await b.open(viaProxy(drop.url))
    await sleep(1000)
    check.that('a new link asks for its key again', await page.step(b) === 'step-key')
    await page.typeKey(b, drop.key)
    await page.waitFor(b, 'step-file')
    await b.chooseFile(file)
    await page.waitFor(b, 'step-upload')
    await sleep(800)
    const resumedAt = await page.percent(b)
    check.that(`re-picking the file resumes partway (at ${resumedAt}%)`, resumedAt >= 20 && resumedAt < 90)

    const step = await page.finish(b)
    check.that('the upload finishes', step === 'step-done', await page.errorText(b))
    check.that("the page's SHA-256 matches the original", await page.text(b, 'done-hash') === want)
    check.that('the working folder is empty afterwards', readdirSync(join(inbox, '.incoming')).length === 0)
    const log = drop.log()
    check.that('no warnings or internal errors after the restart', !log.some(l => /WARNING|internal error/.test(l)), log.join('\n'))
  } finally {
    await b.close()
    proxy.close()
    await drop.stop()
    rmSync(inbox, { recursive: true, force: true })
  }
}

async function lobby(check) {
  const exe = find('chromium')
  if (!exe) return check.skip('no Chrome or Edge found')
  const file = makeFile('shared.zip', 256 * MiB + 77)
  const want = await sha256(file)
  const drop = await startDrop(opts.drop, 18451)
  // 200 Mbps, so the second person can watch the upload partway through.
  const proxy = startProxy(18452, 18451, 25e6)
  const url = drop.url.replace(':18451', ':18452')
  const alex = await launchChromium(exe, 9338)
  const sam = await launchChromium(exe, 9339)
  const hostile = '<img src=x onerror=window.pwned=1>'
  try {
    await alex.open(url)
    await page.waitFor(alex, 'step-key')
    await page.typeKey(alex, drop.key, 'Alex')
    await page.waitFor(alex, 'step-file')
    await sam.open(url)
    await page.waitFor(sam, 'step-key')
    await page.typeKey(sam, drop.key, hostile)
    await page.waitFor(sam, 'step-file')
    await sleep(5000) // a few polls, so latencies have been measured and shared

    const a = await page.lobby(alex)
    const s = await page.lobby(sam)
    const names = view => view.people.map(p => p.who)
    check.that('each person sees both people', [a, s].every(v => v.people.length === 2 && names(v).some(n => n.startsWith('Alex')) && names(v).some(n => n.startsWith('<img'))), JSON.stringify(names(a)))
    check.that('each sees "you" beside their own name only', a.people.filter(p => p.who.endsWith('you')).map(p => p.who).join() === 'Alexyou' && s.people.filter(p => p.who.endsWith('you')).every(p => p.who.startsWith('<img')))
    check.that('everyone\'s latency is measured and shared', [a, s].every(v => v.people.every(p => /^\d+ ms$/.test(p.latency))), JSON.stringify(a.people.map(p => p.latency)))
    check.that('a name made of markup stays plain text', a.people.every(p => !/<img/i.test(p.html)) && await alex.evaluate(`window.pwned === undefined && !document.querySelector('#people img')`))

    await alex.chooseFile(file)
    let watched = null
    for (let i = 0; i < 120 && !watched; i++, await sleep(250)) {
      const t = (await page.lobby(sam)).transfers[0]
      if (t && t.value > 0.05 && t.value < 0.95) watched = t
    }
    check.that("the other person watches Alex's upload partway, attributed to Alex", watched && watched.name === 'shared.zip' && watched.from === 'from Alex', JSON.stringify(watched))

    const step = await page.finish(alex)
    check.that('the upload finishes', step === 'step-done', await page.errorText(alex))
    await sleep(3000)
    const done = (await page.lobby(sam)).transfers[0]
    check.that('the other person sees it done, with the same SHA-256', done && /\bdone\b/.test(done.state) && done.hash.endsWith(want), JSON.stringify(done))
    check.that('the server log names who joined', drop.log().some(l => /Alex \(127\.0\.0\.1\) joined the lobby/.test(l)), drop.log().join('\n'))
  } finally {
    await alex.close()
    await sam.close()
    proxy.close()
    await drop.stop()
  }
}

async function firefox(check) {
  const exe = find('firefox')
  if (!exe) return check.skip('no Firefox found')
  const file = makeFile('firefox.zip', 300 * MiB + 99)
  const want = await sha256(file)
  const drop = await startDrop(opts.drop, 18447)
  const b = await launchFirefox(exe, 9336)
  try {
    await b.open(drop.url)
    check.that('the page is a secure context with crypto.subtle', await b.evaluate(`isSecureContext && !!crypto.subtle`))
    await page.waitFor(b, 'step-key')
    await page.typeKey(b, drop.key)
    await page.waitFor(b, 'step-file')
    await b.chooseFile(file)
    const step = await page.finish(b, undefined, 5 * 60000)
    check.that('the upload finishes', step === 'step-done', await page.errorText(b))
    check.that("the page's SHA-256 matches the original", await page.text(b, 'done-hash') === want)
  } finally {
    await b.close()
    await drop.stop()
  }
}

// --- Runner ------------------------------------------------------------------

const scenarios = { lobby, resume, restart, outage, firefox }
let failed = 0

// Each result prints as it happens, so a scenario that stalls shows where.
function report(r) {
  console.log(`  ${r.note ? '    ' : r.ok ? 'ok  ' : 'FAIL'}  ${r.what}`)
  if (!r.ok && r.detail) console.log('        ' + String(r.detail).split('\n').join('\n        '))
  if (!r.ok) failed++
}

for (const name of opts.only.split(',')) {
  const check = {
    that(what, ok, detail = '') { report({ what, ok: !!ok, detail }) },
    pass(what) { report({ what, ok: true }) },
    skip(why) { report({ what: 'skipped: ' + why, ok: true }) },
    note(what) { report({ what, ok: true, note: true }) },
  }
  console.log(`\n${name}`)
  try {
    await scenarios[name](check)
  } catch (err) {
    report({ what: 'scenario crashed: ' + err.message, ok: false })
  }
}

rmSync(work, { recursive: true, force: true })
console.log(failed ? `\n${failed} check(s) failed` : '\nall checks passed')
process.exit(failed ? 1 : 0)
