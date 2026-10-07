import './style.css'
import { mount } from 'svelte'
import App from './App.svelte'
import { applyPrefs } from './lib/prefs.svelte'

applyPrefs()

export default mount(App, {
  target: document.getElementById('app')!,
})
