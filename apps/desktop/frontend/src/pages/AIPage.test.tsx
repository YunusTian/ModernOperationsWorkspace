import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { callbacks, api } = vi.hoisted(() => ({
  callbacks: new Map<string, (...data: unknown[]) => void>(),
  api: { AIProviders: vi.fn(), AIChatOpen: vi.fn(), AIChatStart: vi.fn(), AIChatClose: vi.fn() },
}));
vi.mock("../bindings", () => ({
  App: api,
  eventsOn: (name: string, cb: (...data: unknown[]) => void) => { callbacks.set(name,cb); return () => callbacks.delete(name); },
}));

import AIPage from "./AIPage";

describe("AIPage", () => {
  beforeEach(() => { callbacks.clear(); vi.clearAllMocks(); api.AIProviders.mockResolvedValue([{name:"local",capabilities:{chat:true,chat_stream:true,tool_calls:true,models:["model-a"]}}]); api.AIChatOpen.mockResolvedValue("s1"); api.AIChatStart.mockResolvedValue(undefined); api.AIChatClose.mockResolvedValue(undefined); });

  it("loads providers and streams a response after subscriptions are ready", async () => {
    render(<AIPage />);
    expect(await screen.findByDisplayValue("model-a")).toBeInTheDocument();
    fireEvent.change(screen.getByPlaceholderText("Ask MOW…"),{target:{value:"status"}});
    fireEvent.click(screen.getByRole("button",{name:"Send"}));
    await waitFor(()=>expect(api.AIChatStart).toHaveBeenCalledWith("s1"));
    callbacks.get("ai:s1:delta")?.("healthy");
    expect(await screen.findByText("healthy")).toBeInTheDocument();
    callbacks.get("ai:s1:done")?.({});
    await waitFor(()=>expect(screen.getByRole("button",{name:"Send"})).toBeInTheDocument());
  });
});
