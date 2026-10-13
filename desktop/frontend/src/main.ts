import { createApp } from 'vue'
import App from './App.vue'
import './style.css'

createApp(App).mount('#app')

if (__GOSPEED_CF_HARNESS_BUILD__) {
  void import('./p0gHarness').then(({ installP0GHarness }) => {
    installP0GHarness()
  })
}
