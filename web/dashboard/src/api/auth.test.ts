import { describe, expect, it, vi } from "vitest";
import { AuthClient } from "./auth";
import type { ApiTransport } from "./client";

describe("AuthClient", () => {
  it("posts login and register requests", async () => {
    const request = vi.fn(async <T>() => ({ user: { email: "owner@example.com" } }) as T);
    const client = new AuthClient({ request: request as unknown as ApiTransport["request"] });

    await client.login("owner@example.com", "secret");
    await client.register("new@example.com", "secret");

    expect(request).toHaveBeenNthCalledWith(1, "/v1/auth/login", {
      method: "POST",
      body: { email: "owner@example.com", password: "secret" },
    });
    expect(request).toHaveBeenNthCalledWith(2, "/v1/auth/register", {
      method: "POST",
      body: { email: "new@example.com", password: "secret" },
    });
  });

  it("previews invitation signup and changes required passwords", async () => {
    const request = vi.fn(async <T>() => ({}) as T);
    const client = new AuthClient({ request: request as unknown as ApiTransport["request"] });

    await client.previewInvitation("invite-token");
    await client.signupInvitation("invite-token", "member@example.com", "secret");
    await client.changePassword("old", "new-secret");
    await client.logout();
    await client.me();

    expect(request).toHaveBeenNthCalledWith(1, "/v1/auth/invitations/preview", {
      method: "POST",
      body: { token: "invite-token" },
    });
    expect(request).toHaveBeenNthCalledWith(2, "/v1/auth/invitations/signup", {
      method: "POST",
      body: { token: "invite-token", email: "member@example.com", password: "secret" },
    });
    expect(request).toHaveBeenNthCalledWith(3, "/v1/auth/change-password", {
      method: "POST",
      body: { current_password: "old", new_password: "new-secret" },
    });
    expect(request).toHaveBeenNthCalledWith(4, "/v1/auth/logout", {
      method: "POST",
    });
    expect(request).toHaveBeenNthCalledWith(5, "/v1/me");
  });
});
