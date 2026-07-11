import { FormEvent, useEffect, useRef, useState } from "react";
import { AIAskResult, AIMessage, AIProviderVM, App, eventsOn } from "../bindings";

// UsageStats 汇总最近一次 Ask 或 Chat 完成时的用量指标，供徐章展示。
type UsageStats = {
  rounds?: number;
  toolCalls?: number;
  totalTokens?: number;
  finishReason?: string;
};

export default function AIPage() {
  const [providers, setProviders] = useState<AIProviderVM[]>([]);
  const [provider, setProvider] = useState("");
  const [model, setModel] = useState("");
  const [messages, setMessages] = useState<AIMessage[]>([]);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [tool, setTool] = useState("");
  const [usage, setUsage] = useState<UsageStats>({});
  const session = useRef("");
  const off = useRef<(() => void)[]>([]);

  useEffect(() => {
    App.AIProviders()
      .then((p) => {
        setProviders(p);
        if (p[0]) {
          setProvider(p[0].name);
          setModel(p[0].capabilities.models?.[0] ?? "");
        }
      })
      .catch((e) => setError(String(e)));
    return () => {
      off.current.forEach((f) => f());
      if (session.current) void App.AIChatClose(session.current);
    };
  }, []);

  const selected = providers.find((p) => p.name === provider);

  // send 走流式 chat_stream：适合多轮对话与实时打字机效果。
  async function send(e: FormEvent) {
    e.preventDefault();
    const text = input.trim();
    if (!text || busy) return;
    setInput("");
    setError("");
    setTool("");
    setUsage({});
    const next = [...messages, { role: "user", content: text } as AIMessage];
    setMessages([...next, { role: "assistant", content: "" }]);
    setBusy(true);
    try {
      const sid = await App.AIChatOpen({ provider, model, messages: next, timeout_seconds: 120 });
      session.current = sid;
      off.current.forEach((f) => f());
      off.current = [
        eventsOn(`ai:${sid}:delta`, (...d) => {
          const chunk = String(d[0] ?? "");
          setMessages((old) =>
            old.map((m, i) =>
              i === old.length - 1 ? { ...m, content: (m.content ?? "") + chunk } : m,
            ),
          );
        }),
        eventsOn(`ai:${sid}:tool`, (...d) => setTool(JSON.stringify(d[0] ?? {}, null, 2))),
        eventsOn(`ai:${sid}:finish`, (...d) => {
          // provider 侧的 ChatResponse：拿 finish_reason / usage 更新徐章
          const payload = d[0] as { finish_reason?: string; usage?: { total_tokens?: number } } | undefined;
          if (payload) {
            setUsage((u) => ({
              ...u,
              finishReason: payload.finish_reason,
              totalTokens: payload.usage?.total_tokens,
            }));
          }
        }),
        eventsOn(`ai:${sid}:done`, (...d) => {
          const payload = d[0] as { error?: string } | undefined;
          if (payload?.error) setError(payload.error);
          setBusy(false);
          session.current = "";
        }),
      ];
      await App.AIChatStart(sid);
    } catch (e) {
      setError(String(e));
      setBusy(false);
    }
  }

  // ask 走宿主 orchestrator（非流式）：一次性拿到 Rounds / ToolCalls / Usage，
  // 决策链路已由 SlogAuditor 落审计日志。适合明确的一次性问答，也用于 v0.4
  // 验收清单中的 usage 展示。
  async function ask() {
    const text = input.trim();
    if (!text || busy) return;
    setInput("");
    setError("");
    setTool("");
    setUsage({});
    const next = [...messages, { role: "user", content: text } as AIMessage];
    setMessages([...next, { role: "assistant", content: "" }]);
    setBusy(true);
    try {
      const res: AIAskResult = await App.AIAsk({
        provider,
        model,
        messages: next,
        timeout_seconds: 120,
      });
      const content = res.response.message.content ?? "";
      setMessages((old) =>
        old.map((m, i) => (i === old.length - 1 ? { ...m, content } : m)),
      );
      setUsage({
        rounds: res.rounds,
        toolCalls: res.tool_calls,
        totalTokens: res.response.usage?.total_tokens,
        finishReason: res.response.finish_reason,
      });
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }

  function stop() {
    if (session.current) void App.AIChatClose(session.current);
    setBusy(false);
  }

  return (
    <section className="ai-page">
      <header className="ai-toolbar">
        <h2>AI Workspace</h2>
        <select
          value={provider}
          onChange={(e) => {
            setProvider(e.target.value);
            const p = providers.find((x) => x.name === e.target.value);
            setModel(p?.capabilities.models?.[0] ?? "");
          }}
        >
          {providers.map((p) => (
            <option key={p.name}>{p.name}</option>
          ))}
        </select>
        <input value={model} onChange={(e) => setModel(e.target.value)} placeholder="Model" />
        <span>{selected?.capabilities.tool_calls ? "Tools available" : "Chat only"}</span>
        <UsageBadges usage={usage} />
      </header>
      <div className="ai-messages">
        {messages.length === 0 && (
          <div className="ai-empty">
            Ask about your infrastructure. AI remains behind the same Command Engine permissions.
          </div>
        )}
        {messages.map((m, i) => (
          <article className={`ai-msg ${m.role}`} key={i}>
            <b>{m.role}</b>
            <pre>{m.content}</pre>
          </article>
        ))}
        {tool && (
          <details open>
            <summary>Tool call</summary>
            <pre>{tool}</pre>
          </details>
        )}
        {error && <div className="ai-error">{error}</div>}
      </div>
      <form className="ai-compose" onSubmit={send}>
        <textarea
          value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder="Ask MOW…"
          disabled={busy}
        />
        {busy ? (
          <button type="button" onClick={stop}>
            Stop
          </button>
        ) : (
          <>
            <button type="button" onClick={ask} disabled={!provider} title="One-shot via orchestrator (audited)">
              Ask
            </button>
            <button type="submit" disabled={!provider}>
              Send
            </button>
          </>
        )}
      </form>
    </section>
  );
}

// UsageBadges 把最近一次调用的用量指标显示为 header 上的一排徐章。
// - Rounds / ToolCalls 仅在 Ask 路径填充；Send 路径不显示
// - Tokens / Finish 两条 chat_stream 与 ask 都可能提供
function UsageBadges({ usage }: { usage: UsageStats }) {
  const empty =
    usage.rounds === undefined &&
    usage.toolCalls === undefined &&
    usage.totalTokens === undefined &&
    !usage.finishReason;
  if (empty) return null;
  return (
    <div className="ai-usage" role="group" aria-label="usage">
      {usage.rounds !== undefined && (
        <span className="ai-badge" title="Orchestrator rounds">
          rounds: {usage.rounds}
        </span>
      )}
      {usage.toolCalls !== undefined && (
        <span className="ai-badge" title="Tool calls executed">
          tools: {usage.toolCalls}
        </span>
      )}
      {usage.totalTokens !== undefined && usage.totalTokens > 0 && (
        <span className="ai-badge" title="Total tokens (prompt + completion)">
          tokens: {usage.totalTokens}
        </span>
      )}
      {usage.finishReason && (
        <span className="ai-badge ai-badge-finish" title="Finish reason">
          {usage.finishReason}
        </span>
      )}
    </div>
  );
}
