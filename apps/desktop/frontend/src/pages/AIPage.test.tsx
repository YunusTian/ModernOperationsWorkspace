import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { callbacks, api } = vi.hoisted(() => ({
  callbacks: new Map<string, (...data: unknown[]) => void>(),
  api: {
    AIProviders: vi.fn(),
    AIChatOpen: vi.fn(),
    AIChatStart: vi.fn(),
    AIChatClose: vi.fn(),
    AIAsk: vi.fn(),
  },
}));
vi.mock("../bindings", () => ({
  App: api,
  eventsOn: (name: string, cb: (...data: unknown[]) => void) => {
    callbacks.set(name, cb);
    return () => callbacks.delete(name);
  },
}));

import AIPage from "./AIPage";

describe("AIPage", () => {
  afterEach(() => cleanup());
  beforeEach(() => {
    callbacks.clear();
    vi.clearAllMocks();
    api.AIProviders.mockResolvedValue([
      {
        name: "local",
        capabilities: { chat: true, chat_stream: true, tool_calls: true, models: ["model-a"] },
      },
    ]);
    api.AIChatOpen.mockResolvedValue("s1");
    api.AIChatStart.mockResolvedValue(undefined);
    api.AIChatClose.mockResolvedValue(undefined);
    api.AIAsk.mockResolvedValue({
      response: {
        message: { role: "assistant", content: "healthy" },
        usage: { total_tokens: 42 },
        finish_reason: "stop",
      },
      rounds: 2,
      tool_calls: 1,
    });
  });

  it("loads providers and streams a response after subscriptions are ready", async () => {
    render(<AIPage />);
    expect(await screen.findByDisplayValue("model-a")).toBeInTheDocument();
    fireEvent.change(screen.getByPlaceholderText("Ask MOW…"), { target: { value: "status" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(api.AIChatStart).toHaveBeenCalledWith("s1"));
    callbacks.get("ai:s1:delta")?.("healthy");
    expect(await screen.findByText("healthy")).toBeInTheDocument();
    callbacks.get("ai:s1:finish")?.({ finish_reason: "stop", usage: { total_tokens: 33 } });
    // Send 完成后 finish 事件应刷新徐章
    expect(await screen.findByText(/tokens:\s*33/)).toBeInTheDocument();
    expect(screen.getByText("stop")).toBeInTheDocument();
    callbacks.get("ai:s1:done")?.({});
    await waitFor(() => expect(screen.getByRole("button", { name: "Send" })).toBeInTheDocument());
  });

  it("invokes AIAsk via the Ask button and renders usage badges", async () => {
    render(<AIPage />);
    await screen.findByDisplayValue("model-a");
    fireEvent.change(screen.getByPlaceholderText("Ask MOW…"), { target: { value: "check cpu" } });
    fireEvent.click(screen.getByRole("button", { name: "Ask" }));
    await waitFor(() =>
      expect(api.AIAsk).toHaveBeenCalledWith(
        expect.objectContaining({
          provider: "local",
          model: "model-a",
          messages: [{ role: "user", content: "check cpu" }],
        }),
      ),
    );
    // 助手回复被写入 UI
    expect(await screen.findByText("healthy")).toBeInTheDocument();
    // 四个徐章：rounds / tools / tokens / finish
    expect(screen.getByText(/rounds:\s*2/)).toBeInTheDocument();
    expect(screen.getByText(/tools:\s*1/)).toBeInTheDocument();
    expect(screen.getByText(/tokens:\s*42/)).toBeInTheDocument();
    expect(screen.getByText("stop")).toBeInTheDocument();
    // 忙碌复位后 Ask 按钮再次可见
    await waitFor(() => expect(screen.getByRole("button", { name: "Ask" })).toBeInTheDocument());
  });

  it("shows error when AIAsk rejects", async () => {
    api.AIAsk.mockRejectedValueOnce(new Error("AI_MAX_ROUNDS: rounds exceeded"));
    render(<AIPage />);
    await screen.findByDisplayValue("model-a");
    fireEvent.change(screen.getByPlaceholderText("Ask MOW…"), { target: { value: "loop" } });
    fireEvent.click(screen.getByRole("button", { name: "Ask" }));
    expect(await screen.findByText(/AI_MAX_ROUNDS/)).toBeInTheDocument();
  });
});
