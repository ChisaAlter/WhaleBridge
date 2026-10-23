// Local regression fixture. All traffic goes to the test's loopback server.
const ID = "forwarding-fixture"
export const TierFixture = async () => ({
  config: async (cfg) => {
    cfg.provider ??= {}
    cfg.provider[ID] = {
      name: "Forwarding fixture",
      npm: "@ai-sdk/openai-compatible",
      api: process.env.WHALEBRIDGE_TIER_FIXTURE_BASE,
      models: {
        chat: { name: "Chat", serviceTiers: ["priority", "ultrafast"], limit: { context: 1000000, output: 128000 } },
        responses: { name: "Responses", provider: { npm: "@ai-sdk/openai" }, serviceTiers: ["priority", "ultrafast"], limit: { context: 1000000, output: 128000 } },
        ordinary: { name: "Ordinary", limit: { context: 1000000, output: 128000 } },
        messages: { name: "Messages", provider: { npm: "@ai-sdk/anthropic" }, serviceTiers: ["priority"], limit: { context: 1000000, output: 128000 } }
      }
    }
  },
  auth: {
    provider: ID,
    methods: [{ type: "api", label: "Fixture key" }],
    loader: async (getAuth) => ({
      apiKey: "fixture",
      baseURL: process.env.WHALEBRIDGE_TIER_FIXTURE_BASE,
      fetch: async (url, init) => {
        const auth = await getAuth()
        const headers = new Headers(init.headers)
        headers.set("x-fixture-key", auth.key)
        return fetch(url, { ...init, headers })
      }
    })
  }
})
