import { defineConfig, type Plugin } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import { isCloudflarePocEnabled } from './src/featureFlags'

// Cloudflare speed-test experiment gates.
//
// These are resolved once, at build time, so Rollup can drop the Cloudflare
// PoC branches (and the dynamic-import chunks they pull in) from the default
// desktop build. A runtime-only flag is not enough: `defineAsyncComponent(() =>
// import('./components/CloudflarePocPanel.vue'))` would still emit a
// CloudflarePocPanel-*.js chunk containing the speed.cloudflare.com endpoint.
const cloudflarePocBuild = isCloudflarePocEnabled(process.env.VITE_CLOUDFLARE_SPEEDTEST_POC)
const cloudflareHarnessBuild = process.env.VITE_CLOUDFLARE_P0G_HARNESS === 'true'
const cloudflareIncluded = cloudflarePocBuild || cloudflareHarnessBuild

/**
 * Fails the production build when a default (non-Cloudflare) bundle still ships
 * the experiment. Scans the emitted chunks, so this verifies the actual artifact
 * rather than trusting the source-level gate.
 */
function cloudflareIsolationGuard(): Plugin {
  return {
    name: 'gospeed:cloudflare-isolation-guard',
    apply: 'build',
    generateBundle(_options, bundle) {
      if (cloudflareIncluded) return
      const offenders: string[] = []
      for (const [fileName, chunk] of Object.entries(bundle)) {
        if (fileName === 'index.html') continue
        if (/CloudflarePocPanel|p0gHarness/i.test(fileName)) {
          offenders.push(`${fileName} (experiment chunk emitted)`)
          continue
        }
        if (chunk.type === 'chunk' && chunk.code.includes('speed.cloudflare.com')) {
          offenders.push(`${fileName} -> speed.cloudflare.com`)
        }
      }
      if (offenders.length > 0) {
        this.error(
          'Cloudflare experiment leaked into the default build:\n  - ' +
            offenders.join('\n  - ') +
            '\nThe Cloudflare PoC must not ship in the default desktop artifact.',
        )
      }
    },
  }
}

export default defineConfig({
  plugins: [vue(), cloudflareIsolationGuard()],
  define: {
    __GOSPEED_CF_POC_BUILD__: JSON.stringify(cloudflarePocBuild),
    __GOSPEED_CF_HARNESS_BUILD__: JSON.stringify(cloudflareHarnessBuild),
  },
  build: {
    // ECharts dominates the bundle; the desktop shell loads it from disk, so
    // the default 500 kB chunk warning is noise here.
    chunkSizeWarningLimit: 900,
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.{spec,test}.ts'],
  },
})
