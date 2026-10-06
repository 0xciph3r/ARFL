import { prefs } from './prefs.svelte'

// Soft sine tones from the motion and sound spec. Synthesised, so there are no
// sound files to ship. Silent unless the user turns sounds on.
const SEQUENCES: Record<string, [number, number, number][]> = {
  up: [[440, 0, 0.14], [660, 0.12, 0.22]],
  down: [[520, 0, 0.12], [330, 0.1, 0.22]],
  tick: [[220, 0, 0.07]],
  pluck: [[880, 0, 0.2]],
  mint: [[660, 0, 0.1], [880, 0.09, 0.1], [1320, 0.18, 0.24]],
  save: [[784, 0, 0.18]],
}

export type Cue = keyof typeof SEQUENCES

let ctx: AudioContext | null = null

export function cue(name: Cue, force = false) {
  if (!force && !prefs.sounds) return
  try {
    ctx ??= new AudioContext()
    if (ctx.state === 'suspended') void ctx.resume()
    const t0 = ctx.currentTime
    for (const [freq, delay, length] of SEQUENCES[name]) {
      const osc = ctx.createOscillator()
      const gain = ctx.createGain()
      osc.type = 'sine'
      osc.frequency.value = freq
      gain.gain.setValueAtTime(0.0001, t0 + delay)
      gain.gain.exponentialRampToValueAtTime(0.06, t0 + delay + 0.02)
      gain.gain.exponentialRampToValueAtTime(0.0001, t0 + delay + length)
      osc.connect(gain)
      gain.connect(ctx.destination)
      osc.start(t0 + delay)
      osc.stop(t0 + delay + length + 0.05)
    }
  } catch {
    // Audio is optional; never let it break the UI.
  }
}
