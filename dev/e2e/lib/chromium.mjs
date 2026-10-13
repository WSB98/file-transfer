// Drives Chrome or Edge over the DevTools protocol, with nothing but Node's
// built-in WebSocket.
import { spawn } from 'node:child_process'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { sleep } from './drop.mjs'

export async function launchChromium(executable, port) {
  const profile = mkdtempSync(join(tmpdir(), 'drop-e2e-profile-'))
  const proc = spawn(executable, [`--remote-debugging-port=${port}`, `--user-data-dir=${profile}`,
    '--headless=new', '--no-first-run', '--no-default-browser-check', '--window-size=1100,800', 'about:blank'])

  let page
  for (let i = 0; i < 100 && !page; i++) {
    await sleep(100)
    try {
      const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json()
      page = targets.find(t => t.type === 'page')
    } catch {}
  }
  if (!page) throw new Error('browser did not start: ' + executable)

  const ws = new WebSocket(page.webSocketDebuggerUrl)
  await new Promise((ok, bad) => { ws.onopen = ok; ws.onerror = bad })
  let id = 0
  const pending = new Map()
  const listeners = []
  ws.onmessage = ev => {
    let msg
    try { msg = JSON.parse(ev.data) } catch { return }
    if (msg.id && pending.has(msg.id)) {
      pending.get(msg.id)(msg)
      pending.delete(msg.id)
    } else listeners.forEach(f => f(msg))
  }
  // Every command gets an answer or an error within 15 s, so a frozen or
  // crashed tab fails a check instead of hanging the whole run.
  const send = (method, params = {}) => new Promise((ok, bad) => {
    const n = ++id
    const timer = setTimeout(() => {
      pending.delete(n)
      bad(new Error(method + ': no answer in 15 s (the tab may be frozen or crashed)'))
    }, 15000)
    pending.set(n, m => {
      clearTimeout(timer)
      m.error ? bad(new Error(method + ': ' + m.error.message)) : ok(m.result)
    })
    ws.send(JSON.stringify({ id: n, method, params }))
  })
  const evaluate = async expr => {
    const r = await send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true })
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text)
    return r.result.value
  }

  // browserSend talks to the browser itself rather than this tab, for commands
  // like Browser.close that only the browser answers.
  const browserSend = async method => {
    const { webSocketDebuggerUrl } = await (await fetch(`http://127.0.0.1:${port}/json/version`)).json()
    const bws = new WebSocket(webSocketDebuggerUrl)
    await new Promise((ok, bad) => { bws.onopen = ok; bws.onerror = bad })
    bws.send(JSON.stringify({ id: 1, method }))
    await new Promise(r => { bws.onmessage = r; bws.onclose = r; setTimeout(r, 3000) })
    try { bws.close() } catch {}
  }

  // The DOM domain stays off: once enabled, Chrome streams an event for every
  // change to the page, and the lobby re-renders every two seconds.
  await send('Page.enable')
  await send('Runtime.enable')
  // Reloading mid-upload triggers the page's "leave this page?" prompt.
  listeners.push(m => { if (m.method === 'Page.javascriptDialogOpening') send('Page.handleJavaScriptDialog', { accept: true }) })
  // A crashed tab or an uncaught error in the page is always a real problem;
  // say so at once rather than leaving a check to time out.
  listeners.push(m => {
    if (m.method === 'Inspector.targetCrashed') console.log(`    browser on port ${port}: the tab crashed`)
    if (m.method === 'Runtime.exceptionThrown') console.log(`    browser on port ${port}: page error: ${m.params.exceptionDetails.exception?.description || m.params.exceptionDetails.text}`)
  })

  return {
    evaluate,
    // Opens url and, if the certificate warning appears, clicks through it
    // exactly as a person would: "Advanced", then "Proceed".
    async open(url) {
      await send('Page.navigate', { url })
      await sleep(1500)
      const warned = await evaluate(`!!document.getElementById('proceed-link')`)
      if (warned) {
        await evaluate(`document.getElementById('details-button')?.click(); document.getElementById('proceed-link').click(); true`)
        await sleep(2000)
      }
      return warned
    },
    async chooseFile(path) {
      const { result } = await send('Runtime.evaluate', { expression: `document.getElementById('file')` })
      await send('DOM.setFileInputFiles', { objectId: result.objectId, files: [path] })
      await send('Runtime.releaseObject', { objectId: result.objectId })
    },
    reload: () => send('Page.reload'),
    async memory() {
      const h = await send('Runtime.getHeapUsage')
      return h.usedSize + (h.backingStorageSize || 0)
    },
    async screenshot(path) {
      const { data } = await send('Page.captureScreenshot', { format: 'png' })
      writeFileSync(path, Buffer.from(data, 'base64'))
    },
    // close asks Chrome to shut down, which ends its helper processes too;
    // killing only the main process leaves them running, holding the profile.
    async close() {
      const exited = new Promise(r => proc.once('exit', r))
      try { await browserSend('Browser.close') } catch {}
      if (await Promise.race([exited.then(() => true), sleep(5000).then(() => false)]) === false) proc.kill()
      try { ws.close() } catch {}
      try { rmSync(profile, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 }) } catch {}
    },
  }
}
