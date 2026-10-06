import {defineConfig} from 'vite'
import {svelte} from '@sveltejs/vite-plugin-svelte'
import wails from '@wailsio/runtime/plugins/vite'

// Everything in public/ is copied verbatim into dist/ on each build.
//
// That is what keeps public/.gitkeep alive. main.go embeds the built frontend
// with `//go:embed all:frontend/dist`, which fails to compile if the directory
// does not exist, so a fresh clone needs a committed file inside it. Vite
// empties dist/ before every build, which used to delete that file and break
// the Go build for anyone cloning afterwards. Sourcing it from public/ means
// each build restores it rather than removing it.

// https://vitejs.dev/config/
export default defineConfig({
  server: {
    host: '127.0.0.1',
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  plugins: [svelte(), wails('./bindings')],
})
