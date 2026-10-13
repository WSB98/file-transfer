// A TCP proxy that behaves like a home uplink: browser -> drop traffic is
// capped to a shared rate, and blackhole(seconds) silently stops forwarding in
// both directions (no resets), the way a dropped Wi-Fi link looks.
import net from 'node:net'

export function startProxy(listenPort, targetPort, bytesPerSec) {
  let tokens = 0
  let holeUntil = 0
  const pumps = new Set()
  // Refill by real elapsed time: Windows timers fire every ~16-31 ms, not 20.
  let last = Date.now()
  const tick = setInterval(() => {
    const now = Date.now()
    tokens = Math.min(bytesPerSec / 5, tokens + bytesPerSec * (now - last) / 1000)
    last = now
    for (const p of pumps) p()
  }, 20)

  const server = net.createServer(client => {
    const upstream = net.connect(targetPort, '127.0.0.1')
    const queue = []
    let queued = 0
    const down = []
    const pump = () => {
      if (Date.now() < holeUntil) return
      while (down.length) client.write(down.shift())
      while (queue.length && tokens > 0) {
        const buf = queue[0]
        const n = Math.min(buf.length, Math.floor(tokens))
        if (n <= 0) break
        upstream.write(buf.subarray(0, n))
        tokens -= n
        queued -= n
        if (n === buf.length) queue.shift()
        else queue[0] = buf.subarray(n)
      }
      if (queued < 1 << 20) client.resume()
    }
    pumps.add(pump)
    client.on('data', d => {
      queue.push(d)
      queued += d.length
      if (queued >= 1 << 20) client.pause()
    })
    upstream.on('data', d => { down.push(d); pump() })
    const end = () => { pumps.delete(pump); client.destroy(); upstream.destroy() }
    client.on('close', end).on('error', end)
    upstream.on('close', end).on('error', end)
  })
  server.listen(listenPort, '127.0.0.1')
  return {
    blackhole: seconds => { holeUntil = Date.now() + seconds * 1000 },
    close: () => { clearInterval(tick); server.close() },
  }
}
