import { createApp } from 'vue'
import App from './App.vue'
import './style.css'

createApp(App).mount('#app')

if (import.meta.env.VITE_CLOUDFLARE_P0G_HARNESS === 'true') {
  void import('./p0gHarness').then(({ installP0GHarness }) => {
    installP0GHarness()
  })
}
