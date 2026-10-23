// Self-authored local account fixture. No package installs, vendor calls or CLI reads.
export const UIAudit = async (_input, options = {}) => {
  const base = new URL(options.base)
  if (base.hostname !== "127.0.0.1" || base.protocol !== "http:") throw new Error("UI audit needs its loopback vendor")
  const models = Object.fromEntries(Array.from({ length: 30 }, (_, i) => {
    const id = `audit-subscription-model-${String(i + 1).padStart(2, "0")}-extended-reasoning-long-context`
    return [id, { name: `本地订阅模型 ${i + 1} · Long context reasoning`, limit: { context: 131072, output: 8192 }, capabilities: { reasoning: true }, free: i % 3 === 0 }]
  }))
  return {
    config: async cfg => {
      cfg.provider ??= {}
      cfg.provider.uiaudit = { name: "本地订阅账号长名称 · Offline audit subscription", npm: "@ai-sdk/openai-compatible", api: base.href, models }
    },
    auth: {
      provider: "uiaudit",
      methods: [
        { type: "api", label: "本地审查账号 · Offline fixture" },
        { type: "oauth", label: "本地多步授权 · Offline multi-step sign in", prompts: [
          { type: "select", key: "scope", message: "选择测试工作区 · Long workspace prompt for checking the real selector layout", options: [{ label: "本机审查团队 · Offline team with a deliberately long display name", value: "offline" }] },
          { type: "text", key: "team", message: "填写测试团队 · Long text prompt used by the real local sign-in state machine", placeholder: "offline-team", validate: value => value ? undefined : "请填写本地测试团队" },
        ], authorize: async inputs => ({
          url: new URL("/offline-authorize?team=" + encodeURIComponent(inputs.team), base).href,
          instructions: "Confirm code: AUDIT-2026. " + "本地授权布局审查：不打开外部页面，请粘贴本地测试验证码。 ".repeat(12),
          method: "code", callback: async code => code === "offline-good" ? { type: "success", key: "offline-authorized-account-00000000", accountId: "offline-authorized-long-team-member@fixture.invalid" } : { type: "failed", error: "本地验证码不匹配 · Offline fixture rejected the code" },
        }) },
      ],
      loader: async getAuth => {
        const auth = await getAuth()
        return { baseURL: base.href, fetch: async (input, init = {}) => {
          const target = new URL(typeof input === "string" ? input : input.url)
          if (target.hostname !== "127.0.0.1" || target.port !== base.port || target.protocol !== base.protocol) throw new Error("UI audit denied non-local request")
          const headers = new Headers(init.headers)
          headers.set("authorization", "Bearer " + auth.key)
          return fetch(target, { ...init, headers })
        } }
      },
      usage: async () => ({ plan: "Offline review team plan with a deliberately long label", windows: [{ name: "Weekly", used: 37, limit: 100, reset: Date.now() + 86400000 }] }),
    },
    provider: { id: "uiaudit", models: async p => p.models },
  }
}
