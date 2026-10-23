// Local-only account catalog fixture: every read is from a test-owned JSON
// file, with no package installation, CLI identity, vendor or network access.
import fs from "node:fs"

export const FreeFirstFixture = async (_input, options = {}) => ({
  config: async (cfg) => {
    cfg.provider ??= {}
    cfg.provider.freefixture = {
      name: "Free account fixture",
      npm: "@ai-sdk/openai-compatible",
      api: "https://fixture.invalid/v1",
      models: {
        shared: { name: "Shared", limit: { context: 1000, output: 100 } },
        retired: { name: "Retired", status: "deprecated", free: true },
      },
    }
  },
  auth: {
    provider: "freefixture",
    methods: [{ type: "api", label: "Fixture account" }],
    loader: async (getAuth) => {
      const auth = await getAuth()
      return {
        baseURL: options.base ?? "https://fixture.invalid/v1",
        fetch: async (input, init = {}) => {
          const target = new URL(typeof input === "string" ? input : input.url)
          const base = new URL(options.base ?? "https://fixture.invalid")
          target.protocol = base.protocol
          target.host = base.host
          const headers = new Headers(init.headers)
          headers.set("authorization", "Bearer " + auth.key)
          return fetch(target, { ...init, headers })
        },
      }
    },
  },
  provider: {
    id: "freefixture",
    models: async (provider, { auth }) => {
      const state = JSON.parse(fs.readFileSync(options.statePath, "utf8"))[auth?.key] ?? {}
      if (state.mode === "throw") throw new Error("fixture account catalog unavailable")
      if (state.mode === "fallback") return { ...provider.models, [Symbol.for("magpie.fellBack")]: true }
      const free = state.free ?? []
      return Object.fromEntries(Object.entries(provider.models).map(([id, model]) => [id, {
        ...model,
        // Deprecated models are deliberately marked free, too: neither
        // the served ids nor the free table may include them.
        status: id === "retired" ? "deprecated" : "active",
        free: id === "retired" || free.includes(id),
      }]))
    },
  },
})
