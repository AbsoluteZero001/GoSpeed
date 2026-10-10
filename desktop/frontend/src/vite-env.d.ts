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

declare module '*.vue' {
    import type {DefineComponent} from 'vue'
    const component: DefineComponent<{}, {}, any>
    export default component
}
