// Drives Firefox over WebDriver BiDi, with nothing but Node's built-in WebSocket.
import { spawn } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { sleep } from './drop.mjs'

export async function launchFirefox(executable, port) {
  const profile = mkdtempSync(join(tmpdir(), 'drop-e2e-profile-'))
  const proc = spawn(executable, ['--headless', `--remote-debugging-port=${port}`, '--no-remote', '--profile', profile])

  let ws
  for (let i = 0; i < 100 && !ws; i++) {
    await sleep(200)
    try {
      const s = new WebSocket(`ws://127.0.0.1:${port}/session`)
      await new Promise((ok, bad) => { s.onopen = ok; s.onerror = bad })
      ws = s
    } catch {}
  }
  if (!ws) throw new Error('browser did not start: ' + executable)

  let id = 0
  const pending = new Map()
  ws.onmessage = ev => {
    const m = JSON.parse(ev.data)
    if (m.id && pending.has(m.id)) {
      pending.get(m.id)(m)
      pending.delete(m.id)
    }
  }
  const send = (method, params = {}) => new Promise((ok, bad) => {
    const n = ++id
    pending.set(n, m => m.type === 'error' ? bad(new Error(method + ': ' + m.message)) : ok(m.result))
    ws.send(JSON.stringify({ id: n, method, params }))
  })

  // acceptInsecureCerts uses the same certificate-exception mechanism as
  // clicking "Accept the Risk and Continue".
  await send('session.new', { capabilities: { alwaysMatch: { acceptInsecureCerts: true } } })
  const { contexts } = await send('browsingContext.getTree', {})
  const context = contexts[0].context
  const run = async (expression, ownership = 'none') => {
    const r = await send('script.evaluate', { expression, target: { context }, awaitPromise: true, resultOwnership: ownership })
    if (r.type === 'exception') throw new Error('page threw: ' + JSON.stringify(r.exceptionDetails).slice(0, 300))
    return r.result
  }

  return {
    evaluate: async expr => (await run(expr)).value,
    open: url => send('browsingContext.navigate', { context, url, wait: 'complete' }),
    async chooseFile(path) {
      const input = await run(`document.getElementById('file')`, 'root')
      await send('input.setFiles', { context, element: { sharedId: input.sharedId }, files: [path] })
    },
    async close() {
      try { await send('session.end', {}) } catch {}
      proc.kill()
      await sleep(1500)
      try { rmSync(profile, { recursive: true, force: true }) } catch {}
    },
  }
}
