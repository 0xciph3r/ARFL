import {defineConfig} from 'vite'
import {svelte} from '@sveltejs/vite-plugin-svelte'

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
  plugins: [svelte()]
})
