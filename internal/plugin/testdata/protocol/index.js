// A local-only fixture for the host wire protocol. Its identities and model
// catalog are JSON files in the test's temporary home; it contacts no vendor.
import fs from "node:fs"

export const ProtocolFixture = async (_input, options = {}) => ({
  config: async (cfg) => {
    cfg.provider ??= {}
    cfg.provider.fixture = {
      name: "Protocol fixture",
      npm: "@ai-sdk/openai-compatible",
      api: "https://fixture.invalid/v1",
      models: {
        "fixture-default": {
          name: "Default",
          limit: { context: 1000, output: 100 },
          serviceTiers: [" priority ", { id: " ultrafast " }, "", null, 7, {}, { id: 9 }, "priority"],
        },
        "fixture-plain": { name: "Plain", serviceTiers: "priority" },
      },
    }
  },
  auth: {
    provider: "fixture",
    single: options.single === true,
    methods: [
      {
        type: "api",
        label: "Local identity",
        keyless: true,
        authorize: async (inputs) => {
          if (inputs.fail) return { type: "failed", error: "local identity unavailable" }
          return { type: "success", ...JSON.parse(fs.readFileSync(options.identity, "utf8")) }
        },
      },
      { type: "api", label: "API key" },
      { type: "api", label: "Missing authorize", keyless: true },
      {
        type: "oauth",
        label: "Code",
        keyless: true,
        authorize: async () => ({
          method: "code",
          url: "https://fixture.invalid/authorize",
          callback: async (code) => code === "good"
            ? { type: "success", refresh: "fixture-refresh", access: "fixture-access", expires: 0 }
            : { type: "failed" },
        }),
      },
    ],
  },
  provider: {
    id: "fixture",
    models: async (provider, { auth }) => {
      if (!auth || !options.catalog) return provider.models
      const state = JSON.parse(fs.readFileSync(options.catalog, "utf8"))[auth.key ?? auth.accountId]
      if (state?.mode === "throw") throw new Error("fixture catalog unavailable")
      if (state?.mode === "fallback") return { ...provider.models, [Symbol.for("magpie.fellBack")]: true }
      if (!state) return provider.models
      return Object.fromEntries(state.models.map((id) => [id, {
        ...provider.models["fixture-default"],
        name: state.names?.[id] ?? id,
        serviceTiers: state.tiers ?? ["priority", { id: "ultrafast" }],
      }]))
    },
  },
})
