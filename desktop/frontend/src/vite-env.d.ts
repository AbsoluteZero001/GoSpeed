/// <reference types="vite/client" />

interface ImportMetaEnv {
    readonly VITE_CLOUDFLARE_SPEEDTEST_POC?: string
    readonly VITE_CLOUDFLARE_P0G_HARNESS?: string
    readonly VITE_CLOUDFLARE_P0G_AUTORUN?: string
    readonly VITE_CLOUDFLARE_MOCK_BASE_URL?: string
}

interface ImportMeta {
    readonly env: ImportMetaEnv
}

// Build-time Cloudflare experiment gates, inlined by Vite's `define`.
// They are compile-time constants so dead branches (and their chunks) are dropped.
declare const __GOSPEED_CF_POC_BUILD__: boolean
declare const __GOSPEED_CF_HARNESS_BUILD__: boolean

declare module '*.vue' {
    import type {DefineComponent} from 'vue'
    const component: DefineComponent<{}, {}, any>
    export default component
}
