import { describe, expect, it } from "vitest";
import { PeopleClient } from "./people";

describe("PeopleClient", () => {
  it("uses the existing home member endpoints", async () => {
    const calls: Array<{ path: string; method?: string; body?: unknown }> = [];
    const request = async <T,>(path: string, options: { method?: string; body?: unknown } = {}) => {
      calls.push({ path, method: options.method, body: options.body });
      return {} as T;
    };
    const client = new PeopleClient({ request });

    await client.listMembers();
    await client.listInvitations();
    await client.createInvitation("member@example.com");
    await client.cancelInvitation("invite_1");
    await client.removeMember("usr_member");
    await client.resetPassword("usr_member", {
      temporary_password: "temporary-password",
      password_change_required: true,
    });
    await client.updateDisplayName("usr_member", "Member Name");

    expect(calls).toEqual([
      { path: "/v1/home/members", method: undefined, body: undefined },
      { path: "/v1/home/members/invitations", method: undefined, body: undefined },
      { path: "/v1/home/members/invitations", method: "POST", body: { email: "member@example.com" } },
      { path: "/v1/home/members/invitations/invite_1", method: "DELETE", body: undefined },
      { path: "/v1/home/members/usr_member", method: "DELETE", body: undefined },
      {
        path: "/v1/home/members/usr_member/password",
        method: "PUT",
        body: { temporary_password: "temporary-password", password_change_required: true },
      },
      {
        path: "/v1/home/members/usr_member/display-name",
        method: "PUT",
        body: { display_name: "Member Name" },
      },
    ]);
  });

  it("normalizes nullable people lists", async () => {
    const request = async <T,>(path: string) => (
      path.includes("invitations") ? { invitations: null } : { members: null }
    ) as T;
    const client = new PeopleClient({ request });

    await expect(client.listMembers()).resolves.toEqual({ members: [] });
    await expect(client.listInvitations()).resolves.toEqual({ invitations: [] });
  });
});
