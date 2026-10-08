import { type FormEvent, useEffect, useRef, useState } from "react";
import { bootstrapClient, type BootstrapState } from "../api/bootstrap";
import {
  peopleClient,
  type CreatedInvitation,
  type HomeInvitation,
  type HomeMember,
} from "../api/people";
import { useConfirmDialog } from "../ui/primitives";

type ResetForm = {
  member: HomeMember;
  temporaryPassword: string;
  passwordChangeRequired: boolean;
};

type NameEdit = {
  member: HomeMember;
  value: string;
};

type State =
  | { status: "loading" }
  | { status: "error"; message: string }
  | {
      status: "ready";
      bootstrap: BootstrapState;
      members: HomeMember[];
      invitations: HomeInvitation[];
      inviteEmail: string;
      createdInvitation: CreatedInvitation | null;
      resetForm: ResetForm | null;
      nameEdit: NameEdit | null;
      message: string;
    };

function errorMessage(error: unknown): string {
  return error instanceof Error && error.message ? error.message : "People settings could not be loaded.";
}

function formatDate(value: string | null | undefined): string {
  return value ? new Date(value).toLocaleString() : "Never";
}

function initials(value: string): string {
  const local = value.split("@")[0] || value;
  const parts = local.split(/[\s._-]+/).filter(Boolean);
  return (parts.length > 1 ? `${parts[0][0]}${parts[1][0]}` : local.slice(0, 2)).toUpperCase();
}

function memberLabel(member: HomeMember): string {
  return member.display_name?.trim() || member.email;
}

export function PeopleSettings() {
  const [state, setState] = useState<State>({ status: "loading" });
  const targetedMember = useRef(new URLSearchParams(window.location.search).get("member") || "");
  const targetedMemberRow = useRef<HTMLElement>(null);
  const dialog = useConfirmDialog();

  async function load(message = "", createdInvitation: CreatedInvitation | null = null, resetForm: ResetForm | null = null) {
    try {
      const bootstrap = await bootstrapClient.load();
      const membersPayload = await peopleClient.listMembers();
      const invitationsPayload = bootstrap.permissions.can_manage_people
        ? await peopleClient.listInvitations()
        : { invitations: [] };
      setState({
        status: "ready",
        bootstrap,
        members: membersPayload.members || [],
        invitations: invitationsPayload.invitations || [],
        inviteEmail: "",
        createdInvitation,
        resetForm,
        nameEdit: null,
        message,
      });
    } catch (error) {
      setState({ status: "error", message: errorMessage(error) });
    }
  }

  useEffect(() => {
    void load();
  }, []);

  useEffect(() => {
    if (state.status !== "ready" || !targetedMember.current) return;
    targetedMemberRow.current?.focus();
    targetedMemberRow.current?.scrollIntoView?.({ block: "center" });
  }, [state]);

  if (state.status === "loading") {
    return (
      <section className="settings-page" aria-labelledby="route-title">
        <p className="eyebrow">Hank</p>
        <h1 id="route-title">People</h1>
        <p className="loading-state">Loading people...</p>
      </section>
    );
  }

  if (state.status === "error") {
    return (
      <section className="settings-page" aria-labelledby="route-title">
        <p className="eyebrow">Hank</p>
        <h1 id="route-title">People</h1>
        <p className="error-state">{state.message}</p>
      </section>
    );
  }

  const readyState = state;
  const canManage = readyState.bootstrap.permissions.can_manage_people;

  function setReady(next: Partial<Extract<State, { status: "ready" }>>) {
    setState((current) => current.status === "ready" ? { ...current, ...next } : current);
  }

  async function createInvite(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      const invitation = await peopleClient.createInvitation(readyState.inviteEmail.trim());
      await load("Invite created.", invitation, null);
    } catch (error) {
      setReady({ message: errorMessage(error) });
    }
  }

  async function cancelInvite(invitation: HomeInvitation) {
    const confirmed = await dialog.confirm({
      title: "Cancel invite",
      message: `Cancel the invite for ${invitation.email}?`,
      confirmLabel: "Cancel invite",
      tone: "danger",
    });
    if (!confirmed) return;
    try {
      await peopleClient.cancelInvitation(invitation.id);
      await load("Invite cancelled.");
    } catch (error) {
      setReady({ message: errorMessage(error) });
    }
  }

  async function removeMember(member: HomeMember) {
    const confirmed = await dialog.confirm({
      title: "Remove person",
      message: `Remove ${member.email} from this home?`,
      confirmLabel: "Remove",
      tone: "danger",
    });
    if (!confirmed) return;
    try {
      await peopleClient.removeMember(member.user_id);
      await load("Person removed from home.");
    } catch (error) {
      setReady({ message: errorMessage(error) });
    }
  }

  async function resetPassword(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!readyState.resetForm) return;
    try {
      await peopleClient.resetPassword(readyState.resetForm.member.user_id, {
        temporary_password: readyState.resetForm.temporaryPassword,
        password_change_required: readyState.resetForm.passwordChangeRequired,
      });
      setReady({ message: "Password reset. Existing sessions revoked." });
    } catch (error) {
      setReady({ message: errorMessage(error) });
    }
  }

  async function saveDisplayName(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!readyState.nameEdit) return;
    try {
      const updated = await peopleClient.updateDisplayName(
        readyState.nameEdit.member.user_id,
        readyState.nameEdit.value.trim(),
      );
      setReady({
        members: readyState.members.map((member) => member.user_id === updated.user_id ? updated : member),
        nameEdit: null,
        message: "Display name updated.",
      });
    } catch (error) {
      setReady({ message: errorMessage(error) });
    }
  }

  return (
    <section className="settings-page" aria-labelledby="route-title">
      <header className="dashboard-header">
        <div>
          <p className="eyebrow">Hank</p>
          <h1 id="route-title">People</h1>
          <p className="meta-line">Who can access this home and what they can manage.</p>
        </div>
        <span className="status-pill">{readyState.bootstrap.membership?.role || "member"}</span>
      </header>

      {readyState.message ? <p className="notice-state">{readyState.message}</p> : null}

      {canManage ? (
        <section className="settings-panel" aria-label="Invite someone">
          <h2>Invite</h2>
          <form className="inline-form" onSubmit={createInvite}>
            <label>
              <span>Invite email</span>
              <input
                onChange={(event) => setReady({ inviteEmail: event.target.value })}
                required
                type="email"
                value={readyState.inviteEmail}
              />
            </label>
            <button type="submit">Create invite</button>
          </form>
          {readyState.createdInvitation ? (
            <div className="token-output">
              <strong>{readyState.createdInvitation.email}</strong>
              <code>{readyState.createdInvitation.join_url || readyState.createdInvitation.token}</code>
              <code>{readyState.createdInvitation.token}</code>
            </div>
          ) : null}
        </section>
      ) : (
        <p className="notice-state">Only admins can invite people.</p>
      )}

      <section className="settings-panel" aria-label="People with access">
        <div className="panel-heading">
          <h2>Members</h2>
          <span className="status-pill">{readyState.members.length} members</span>
        </div>
        <div aria-label="Home members" className="quick-links-list settings-list" role="list">
          {readyState.members.map((member) => {
            const isSelf = member.user_id === readyState.bootstrap.user.id;
            const label = memberLabel(member);
            const mayEditName = canManage || isSelf;
            const editingName = readyState.nameEdit?.member.user_id === member.user_id;
            const isSearchTarget = targetedMember.current === member.email || targetedMember.current === member.user_id;
            return (
              <article
                aria-current={isSearchTarget ? "true" : undefined}
                className={`quick-link-row${isSearchTarget ? " is-search-target" : ""}`}
                key={member.user_id}
                ref={isSearchTarget ? targetedMemberRow : undefined}
                role="listitem"
                tabIndex={isSearchTarget ? -1 : undefined}
              >
                <div className="person-row-main">
                  <span className="person-avatar" aria-hidden="true">{initials(label)}</span>
                  <div className="quick-link-copy">
                    <strong>{label}</strong>
                    <span>{member.display_name ? member.email : member.user_id}</span>
                    <small>{member.display_name ? `${member.user_id} · ` : ""}Joined {formatDate(member.created_at)}</small>
                  </div>
                </div>
                <span className="status-pill">{member.role}</span>
                {mayEditName || (canManage && !isSelf) ? (
                  <div className="row-actions">
                    {mayEditName ? (
                      <button
                        className="secondary"
                        onClick={() => setReady({ nameEdit: { member, value: member.display_name || "" } })}
                        type="button"
                      >
                        Edit display name for {label}
                      </button>
                    ) : null}
                    {canManage && !isSelf ? (
                      <>
                        <button
                          className="secondary"
                          onClick={() => setReady({
                            resetForm: {
                              member,
                              temporaryPassword: "",
                              passwordChangeRequired: true,
                            },
                          })}
                          type="button"
                        >
                          Reset password for {member.email}
                        </button>
                        <button className="danger-link" onClick={() => void removeMember(member)} type="button">
                          Remove {member.email}
                        </button>
                      </>
                    ) : null}
                  </div>
                ) : null}
                {editingName ? (
                  <form className="inline-form person-name-form" onSubmit={saveDisplayName}>
                    <label>
                      <span>Display name for {member.email}</span>
                      <input
                        onChange={(event) => setReady({
                          nameEdit: readyState.nameEdit ? { ...readyState.nameEdit, value: event.target.value } : null,
                        })}
                        value={readyState.nameEdit?.value || ""}
                      />
                    </label>
                    <button type="submit">Save display name</button>
                    <button className="secondary" onClick={() => setReady({ nameEdit: null })} type="button">Cancel</button>
                  </form>
                ) : null}
              </article>
            );
          })}
        </div>
      </section>

      {readyState.resetForm ? (
        <section className="settings-panel" aria-label="Reset password">
          <h2>Reset Password</h2>
          <p className="empty-state">Reset {readyState.resetForm.member.email}. Existing sessions will be revoked.</p>
          <form className="quick-link-form" onSubmit={resetPassword}>
            <label>
              <span>Temporary password</span>
              <input
                onChange={(event) => setReady({
                  resetForm: readyState.resetForm
                    ? { ...readyState.resetForm, temporaryPassword: event.target.value }
                    : null,
                })}
                required
                type="text"
                value={readyState.resetForm.temporaryPassword}
              />
            </label>
            <label className="checkbox-field">
              <input
                checked={readyState.resetForm.passwordChangeRequired}
                onChange={(event) => setReady({
                  resetForm: readyState.resetForm
                    ? { ...readyState.resetForm, passwordChangeRequired: event.target.checked }
                    : null,
                })}
                type="checkbox"
              />
              <span>Require password change on next login</span>
            </label>
            <div className="form-actions">
              <button type="submit">Save temporary password</button>
              <button className="secondary" onClick={() => setReady({ resetForm: null })} type="button">
                Cancel
              </button>
            </div>
          </form>
        </section>
      ) : null}

      <section className="settings-panel" aria-label="Pending invites">
        <div className="panel-heading">
          <h2>Pending Invites</h2>
          <span className="status-pill">{readyState.invitations.length} pending</span>
        </div>
        {canManage ? (
          <div aria-label="Pending invitations" className="quick-links-list settings-list" role="list">
            {readyState.invitations.length > 0 ? readyState.invitations.map((invitation) => (
              <article className="quick-link-row" key={invitation.id} role="listitem">
                <div className="quick-link-copy">
                  <strong>{invitation.email}</strong>
                  <span>{invitation.id}</span>
                  <small>Expires {formatDate(invitation.expires_at)}</small>
                </div>
                <span className="status-pill">{invitation.role}</span>
                <div className="row-actions">
                  <button className="danger-link" onClick={() => void cancelInvite(invitation)} type="button">
                    Cancel invite for {invitation.email}
                  </button>
                </div>
              </article>
            )) : (
              <p className="empty-state">No pending invites.</p>
            )}
          </div>
        ) : (
          <p className="empty-state">Only admins can see pending invites.</p>
        )}
      </section>
    </section>
  );
}
