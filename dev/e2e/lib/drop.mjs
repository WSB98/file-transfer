// Starts a drop binary on loopback and reads the link's token and the key from
// its banner. Without an inbox it makes a throwaway one and removes it on stop.
import { spawn } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

export const sleep = ms => new Promise(r => setTimeout(r, ms))

export function tempInbox() {
  return mkdtempSync(join(tmpdir(), 'drop-e2e-inbox-'))
}

export async function startDrop(binary, port, inbox) {
  const own = !inbox
  if (own) inbox = tempInbox()
  // Run from the inbox, so a drop.json in the repo can't change the test.
  const proc = spawn(resolve(binary), ['-addr', `127.0.0.1:${port}`, '-inbox', inbox], { cwd: inbox })
  let out = ''
  proc.stdout.on('data', d => { out += d })
  proc.stderr.on('data', d => { out += d })
  for (let i = 0; i < 100 && !/Limits:[^\n]*\n(.*\n)*?\n/.test(out); i++) await sleep(50)
  const token = /127\.0\.0\.1:\d+\/t\/([^/]+)\//.exec(out)?.[1]
  const key = /Key:\s+(\S+)/.exec(out)?.[1]
  if (!token || !key) {
    proc.kill()
    throw new Error('drop did not start:\n' + out)
  }
  return {
    url: `https://127.0.0.1:${port}/t/${token}/`,
    token,
    key,
    inbox,
    banner: out.split('\n\n').slice(0, 2).join('\n\n'),
    // The log lines drop printed, without the progress line's redraws.
    log: () => out.split(/\r|\n/).map(l => l.trim()).filter(l => /^\d\d:\d\d:\d\d/.test(l)),
    // Kills drop at once, the way a crash or power cut would.
    kill: async () => {
      proc.kill()
      await sleep(500)
    },
    stop: async () => {
      proc.kill()
      await sleep(500)
      if (own) rmSync(inbox, { recursive: true, force: true })
    },
  }
}
