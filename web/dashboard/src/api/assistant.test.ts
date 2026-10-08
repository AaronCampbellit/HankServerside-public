import { describe, expect, it, vi } from "vitest";
import type { ApiTransport } from "./client";
import { AssistantClient } from "./assistant";

function testTransport(request: ReturnType<typeof vi.fn>): ApiTransport {
  return { request: request as ApiTransport["request"] };
}

describe("AssistantClient", () => {
  it("loads assistant settings surfaces in parallel", async () => {
    const request = vi.fn(async (path: string) => ({ path }));
    const client = new AssistantClient(testTransport(request));

    const result = await client.loadSettings();

    expect(request).toHaveBeenCalledWith("/v1/oauth/openai/status");
    expect(request).toHaveBeenCalledWith("/v1/home/assistant/status");
    expect(request).toHaveBeenCalledWith("/v1/home/assistant/settings");
    expect(request).toHaveBeenCalledWith("/v1/home/assistant/models");
    expect(request).toHaveBeenCalledWith("/v1/me/mcp");
    expect(request).toHaveBeenCalledTimes(5);
    expect(request).not.toHaveBeenCalledWith("/v1/home/mcp/settings");
    expect(result.openAI).toEqual({ path: "/v1/oauth/openai/status" });
    expect(result.assistant).toEqual({ path: "/v1/home/assistant/status" });
    expect(result.mcp).toEqual(expect.objectContaining({ connections: [], context_sources: [], scopes_supported: [] }));
  });

  it("saves HankAI settings with the existing PUT contract", async () => {
    const request = vi.fn(async () => ({ ok: true }));
    const client = new AssistantClient(testTransport(request));
    const settings = {
      profile_notes_enabled: true,
      home_notes_enabled: false,
      files_enabled: true,
      calendar_enabled: false,
      homeassistant_enabled: true,
      project_docs_enabled: true,
      conversations_enabled: false,
      ai_provider: "ollama",
      ollama_base_url: "http://localhost:11434",
      chat_model: "llama3.1",
      embedding_model: "nomic-embed-text",
      planner_enabled: true,
      planner_model: "llama3.1",
      prompt_profile: "local",
      system_prompt: "Use local context.",
    };

    await client.saveSettings(settings);

    expect(request).toHaveBeenCalledWith("/v1/home/assistant/settings", {
      method: "PUT",
      body: settings,
    });
  });

  it("starts OpenAI linking and revokes MCP connections", async () => {
    const request = vi.fn(async () => ({ ok: true }));
    const client = new AssistantClient(testTransport(request));

    await client.startOpenAILink();
    await client.revokeMCPConnection("conn_1");

    expect(request).toHaveBeenCalledWith("/v1/oauth/openai/start", { method: "POST", body: {} });
    expect(request).toHaveBeenCalledWith("/v1/me/mcp/connections/conn_1", { method: "DELETE" });
  });

  it("manages MCP context sources", async () => {
    const request = vi.fn(async () => ({ ok: true }));
    const client = new AssistantClient(testTransport(request));
    const input = { name: "MiniHank", file_source_id: "projects", root_path: "Projects/MiniHank", enabled: true };
    await client.createMCPContextSource(input);
    await client.updateMCPContextSource("ctx_1", { ...input, enabled: false });
    await client.testMCPContextSource("ctx_1");
    await client.deleteMCPContextSource("ctx_1");
    expect(request).toHaveBeenNthCalledWith(1, "/v1/me/mcp/context-sources", { method: "POST", body: input });
    expect(request).toHaveBeenNthCalledWith(2, "/v1/me/mcp/context-sources/ctx_1", { method: "PUT", body: { ...input, enabled: false } });
    expect(request).toHaveBeenNthCalledWith(3, "/v1/me/mcp/context-sources/ctx_1/test", { method: "POST", body: {} });
    expect(request).toHaveBeenNthCalledWith(4, "/v1/me/mcp/context-sources/ctx_1", { method: "DELETE" });
  });
});
