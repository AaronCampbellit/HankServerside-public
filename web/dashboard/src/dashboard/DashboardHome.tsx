import { MonitoringHealthChecklist } from "../pages/MonitoringHealthChecklist";
import { useEffect, useState } from "react";
import { bootstrapClient, type BootstrapState } from "../api/bootstrap";
import { homeClient, type AgentPayload, type Home } from "../api/home";
import { homeAssistantClient, type HomeAssistantEntity, type HomeAssistantLoadPayload } from "../api/homeAssistant";
import { peopleClient, type MembersPayload } from "../api/people";
import { quickLinksClient, type QuickLinksPayload } from "../api/quickLinks";
import { storageClient, type StorageEvent, type StorageStatus, type StorageTask } from "../api/storage";
import { syncClient, type SyncProfileStatus, type SyncStatus } from "../api/sync";
import { type AsyncState, ErrorState, LoadingState, useAsyncLoad, useToast } from "../ui/primitives";
import { entityAction, EntityCard } from "./HomeAssistantPage";

type DashboardData = {
  bootstrap: BootstrapState;
  quickLinks: QuickLinksPayload | null;
  homeAssistant: HomeAssistantLoadPayload | null;
  home: Home | null;
  agentPayload: AgentPayload;
  sync: SyncStatus | null;
  storage: StorageStatus | null;
  storageError: string;
  members: MembersPayload;
};
type DashboardState = AsyncState<DashboardData>;

type Tone = "ok" | "warn" | "bad" | "neutral";

type ServiceRow = {
  title: string;
  meta: string;
  status: string;
  tone: Tone;
  glyph: string;
};

type ActivityItem = {
  title: string;
  detail: string;
  tone: Tone;
};

function statusLabel(value: string | undefined): string {
  return value && value.trim() ? value : "unknown";
}

function quickLinkStatusTone(value: string | undefined): "up" | "down" | "unknown" {
  const status = statusLabel(value).toLowerCase();
  if (status === "up") return "up";
  if (status === "down") return "down";
  return "unknown";
}

function relativeTime(iso: string | null | undefined): string {
  if (!iso) return "never";
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "unknown";
  const secs = Math.max(0, Math.round((Date.now() - then) / 1000));
  if (secs < 60) return `${secs}s ago`;
  const mins = Math.round(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.round(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.round(hours / 24)}d ago`;
}

function absoluteTime(iso: string | null | undefined): string {
  return iso ? new Date(iso).toLocaleString() : "Never";
}

async function loadOptional<T>(loader: () => Promise<T>, fallback: T): Promise<T> {
  try {
    return await loader();
  } catch {
    return fallback;
  }
}

function greeting(): string {
  const hour = new Date().getHours();
  if (hour < 12) return "Good morning";
  if (hour < 18) return "Good afternoon";
  return "Good evening";
}

function displayName(email: string | undefined): string {
  const local = email?.split("@")[0]?.trim();
  return local || "Unknown user";
}

function initials(email: string): string {
  const local = email.split("@")[0] || email;
  const parts = local.split(/[._\-\s]+/).filter(Boolean);
  const raw = parts.length > 1 ? `${parts[0][0]}${parts[1][0]}` : local.slice(0, 2);
  return raw.toUpperCase();
}

function plural(count: number, one: string, many = `${one}s`): string {
  return `${count} ${count === 1 ? one : many}`;
}

function titleCase(value: string | undefined): string {
  return statusLabel(value)
    .replace(/[_-]+/g, " ")
    .replace(/\b\w/g, (match) => match.toUpperCase());
}

function taskIsActive(task: StorageTask): boolean {
  return ["queued", "running"].includes(String(task.status || "").toLowerCase());
}

function eventTone(event: StorageEvent): Tone {
  const severity = String(event.severity || "").toLowerCase();
  if (severity === "error" || severity === "danger") return "bad";
  if (severity === "warning" || severity === "warn") return "warn";
  return "ok";
}

function findProfile(profiles: Record<string, SyncProfileStatus>, names: string[]): SyncProfileStatus | undefined {
  const lowered = names.map((name) => name.toLowerCase());
  return Object.entries(profiles).find(([key, profile]) => {
    const serviceType = String(profile.service_type || "").toLowerCase();
    const lookup = key.toLowerCase();
    return lowered.some((name) => lookup.includes(name) || serviceType.includes(name));
  })?.[1];
}

function profileHealthy(profile: SyncProfileStatus | undefined): boolean {
  if (!profile) return false;
  if (profile.last_error) return false;
  const status = String(profile.status || "").toLowerCase();
  return !status || ["healthy", "ok", "online", "connected", "ready"].includes(status);
}

function profileStatus(profile: SyncProfileStatus | undefined): { label: string; tone: Tone; meta: string } {
  if (!profile) return { label: "Setup needed", tone: "warn", meta: "No connection profile saved yet" };
  if (profile.last_error) return { label: "Review", tone: "bad", meta: profile.last_error };
  if (profileHealthy(profile)) {
    return { label: "Connected", tone: "ok", meta: `Updated ${relativeTime(profile.updated_at || profile.last_backup_at)}` };
  }
  return { label: titleCase(profile.status), tone: "warn", meta: `Updated ${relativeTime(profile.updated_at || profile.last_backup_at)}` };
}

function buildActivityItems(storage: StorageStatus | null, sync: SyncStatus | null, members: MembersPayload): ActivityItem[] {
  const events = (storage?.events || []).slice(0, 3).map((event) => ({
    title: event.message || `${titleCase(event.operation)} event`,
    detail: `${titleCase(event.operation)} · ${relativeTime(event.time)}`,
    tone: eventTone(event),
  }));
  const items: ActivityItem[] = [...events];
  if (storage?.backup?.last_successful_at) {
    items.push({
      title: "Database backup verified",
      detail: `Last completed ${relativeTime(storage.backup.last_successful_at)}`,
      tone: "ok",
    });
  }
  if (sync?.notes?.last_successful_sync_at) {
    items.push({
      title: "Notes synced",
      detail: `Completed ${relativeTime(sync.notes.last_successful_sync_at)}`,
      tone: sync.notes.last_error ? "bad" : "ok",
    });
  }
  if (members.members.length > 0) {
    items.push({
      title: "Home members active",
      detail: plural(members.members.length, "person", "people"),
      tone: "neutral",
    });
  }
  if (items.length === 0) {
    items.push({
      title: "Dashboard ready",
      detail: "Live status will appear here as Hank records activity.",
      tone: "neutral",
    });
  }
  return items.slice(0, 4);
}

function HomeAssistantControlsPanel({ payload }: { payload: HomeAssistantLoadPayload | null }) {
  const [states, setStates] = useState(payload?.states || []);
  const [message, setMessage] = useState("");
  const entities = (payload?.dashboardEntityIDs || [])
    .map((entityID) => states.find((entity) => entity.entity_id === entityID))
    .filter((entity): entity is HomeAssistantEntity => Boolean(entity))
    .slice(0, 4);

  useEffect(() => {
    let active = true;
    const unsubscribe = homeAssistantClient.onStateChanged((entity) => {
      setStates((current) => current.some((candidate) => candidate.entity_id === entity.entity_id)
        ? current.map((candidate) => candidate.entity_id === entity.entity_id ? { ...candidate, ...entity } : candidate)
        : current);
    }, () => {
      void homeAssistantClient.fetchStates().then((states) => {
        if (active) { setStates(states); setMessage(""); }
      }).catch((error) => { if (active) setMessage(error instanceof Error ? error.message : "Home Assistant refresh failed."); });
    });
    return () => { active = false; unsubscribe(); };
  }, []);

  async function toggleEntity(entity: HomeAssistantEntity) {
    const action = entityAction(entity);
    if (!action) return;
    const previous = states;
    if (action.nextState) {
      setStates((current) => current.map((candidate) => candidate.entity_id === entity.entity_id ? { ...candidate, state: action.nextState as string } : candidate));
    }
    try {
      await homeAssistantClient.callService(entity.entity_id, action.domain, action.service);
      try {
        const current = await homeAssistantClient.fetchState(entity.entity_id);
        if (current) {
          setStates((latest) => latest.map((candidate) => candidate.entity_id === current.entity_id ? { ...candidate, ...current } : candidate));
        }
      } catch {
        // Realtime polling will still correct the tile if the immediate read misses.
      }
      setMessage("");
    } catch (error) {
      if (action.nextState) setStates(previous);
      setMessage(error instanceof Error ? error.message : "Home Assistant control failed.");
    }
  }

  return (
    <section className="home-panel home-ha-panel" aria-label="Home Assistant controls">
      <div className="home-panel-heading">
        <h2 id="home-ha-title">Home Assistant</h2>
        <a href="/dashboard/home-assistant">Open</a>
      </div>
      {message ? <p className="home-panel-message">{message}</p> : null}
      {entities.length ? (
        <div className="home-ha-grid">
          {entities.map((entity) => (
            <EntityCard
              dashboardTile
              entity={entity}
              key={entity.entity_id}
              onDashboard={() => {}}
              onToggle={(nextEntity) => void toggleEntity(nextEntity)}
              saved
            />
          ))}
        </div>
      ) : (
        <p className="empty-state">No Home Assistant entities saved.</p>
      )}
    </section>
  );
}

export function DashboardHome() {
  const toast = useToast();
  const [serviceListExpanded, setServiceListExpanded] = useState(false);
  const state: DashboardState = useAsyncLoad(async () => {
    const bootstrap = await bootstrapClient.load();
    const hasHome = Boolean(bootstrap.home);
    const [quickLinks, homeAssistant, home, agentPayload, sync, storageResult, members] = await Promise.all([
      hasHome ? loadOptional(() => quickLinksClient.list(), null) : Promise.resolve(null),
      hasHome ? loadOptional(() => homeAssistantClient.load(), null) : Promise.resolve(null),
      hasHome ? loadOptional(() => homeClient.getHome(), bootstrap.home) : Promise.resolve(null),
      hasHome ? loadOptional(() => homeClient.getAgent(), { agent: bootstrap.agent, can_restart: false }) : Promise.resolve({ agent: bootstrap.agent, can_restart: false }),
      hasHome ? loadOptional(() => syncClient.status(), null) : Promise.resolve(null),
      hasHome
        ? storageClient.status()
          .then((storage) => ({ storage, storageError: "" }))
          .catch((error) => ({
            storage: null,
            storageError: error instanceof Error ? error.message : "Storage status could not be loaded.",
          }))
        : Promise.resolve({ storage: null, storageError: "" }),
      hasHome ? loadOptional(() => peopleClient.listMembers(), { members: [] }) : Promise.resolve({ members: [] }),
    ]);
    return { bootstrap, quickLinks, homeAssistant, home, agentPayload, sync, ...storageResult, members };
  }, [], "Dashboard data could not be loaded.");

  if (state.status === "loading") {
    return (
      <section className="dashboard-page" aria-labelledby="route-title">
        <p className="eyebrow">Hank</p>
        <h1 id="route-title">Dashboard</h1>
        <LoadingState label="Loading dashboard..." />
      </section>
    );
  }

  if (state.status === "error") {
    return (
      <section className="dashboard-page" aria-labelledby="route-title">
        <p className="eyebrow">Hank</p>
        <h1 id="route-title">Dashboard</h1>
        <ErrorState message={state.message} />
      </section>
    );
  }

  const { bootstrap, quickLinks, homeAssistant, home, agentPayload, sync, storage, storageError, members } = state.data;
  const homeName = home?.name || bootstrap.home?.name || "Hank";
  const agent = agentPayload.agent || bootstrap.agent;
  const agentStatus = statusLabel(agent?.status);
  const online = agentStatus === "online";
  const userEmail = bootstrap.user?.email || "Unknown user";
  const userDisplayName = bootstrap.user?.display_name?.trim() || displayName(userEmail);
  const profiles = sync?.profiles || {};
  const homeAssistantProfile = findProfile(profiles, ["homeassistant", "home_assistant", "ha"]);
  const fileProfile = findProfile(profiles, ["smb", "file", "fileserver", "share"]);
  const homeAssistantService = profileStatus(homeAssistantProfile);
  const fileServer = profileStatus(fileProfile);
  const notesError = sync?.notes?.last_error;
  const notesHealthy = String(sync?.notes?.status || "").toLowerCase() === "healthy" && !notesError;
  const tasks = storage?.tasks || [];
  const activeTasks = tasks.filter(taskIsActive);
  const backup = storage?.backup || {};
  const restore = storage?.restore || {};
  const checksum = storage?.checksum || {};
  const failures = storage?.events?.filter((event) => String(event.severity || "").toLowerCase() === "error") || [];
  const hasStorageFailure = Boolean(storageError || checksum.corruption_detected || checksum.failure_count || failures.length);
  const people = members.members.length > 0
    ? members.members
    : bootstrap.user?.email ? [{
        user_id: bootstrap.user.id || "current",
        email: bootstrap.user.email,
        display_name: bootstrap.user.display_name || "",
        role: bootstrap.membership?.role || "admin",
        created_at: "",
        updated_at: "",
      }] : [];
  const healthLooksGood = online && !hasStorageFailure && !notesError;
  const activityItems = buildActivityItems(storage, sync, { members: people });
  const backupDetail = backup.last_successful_at
    ? restore.last_test_at ? "encrypted · verified" : "encrypted · restore test pending"
    : "No backup recorded";
  const backupDisplay = backup.last_successful_at ? relativeTime(backup.last_successful_at) : "Never";
  const runningDetail = `${plural(activeTasks.filter((task) => String(task.operation || "").toLowerCase().includes("backup")).length, "backup")} · ${plural(Object.keys(profiles).length, "sync")}`;
  const serviceRows: ServiceRow[] = [
    {
      title: "Hank server",
      meta: bootstrap.user?.email ? `Signed in as ${bootstrap.user.email}` : "Sign in required",
      status: bootstrap.user?.email ? "Online" : "Review",
      tone: bootstrap.user?.email ? "ok" : "bad",
      glyph: "C",
    },
    {
      title: "Primary Hank Agent",
      meta: agent ? `${agent.name || agent.agent_id} · seen ${relativeTime(agent.last_seen_at)}` : "Agent not registered",
      status: online ? "Online" : titleCase(agentStatus),
      tone: online ? "ok" : "bad",
      glyph: "A",
    },
    {
      title: "PostgreSQL",
      meta: storageError || (backup.last_successful_at ? `Backup ${relativeTime(backup.last_successful_at)}` : "Storage operations reachable"),
      status: hasStorageFailure ? "Review" : activeTasks.length ? "Working" : "Ready",
      tone: hasStorageFailure ? "bad" : activeTasks.length ? "warn" : "ok",
      glyph: "D",
    },
    {
      title: "Home Assistant",
      meta: homeAssistantService.meta,
      status: homeAssistantService.label,
      tone: homeAssistantService.tone,
      glyph: "H",
    },
    {
      title: "SMB shares",
      meta: fileServer.meta,
      status: fileServer.label,
      tone: fileServer.tone,
      glyph: "S",
    },
    {
      title: "Notes",
      meta: notesError || `Last sync ${relativeTime(sync?.notes?.last_successful_sync_at)}`,
      status: notesHealthy ? "Healthy" : notesError ? "Review" : "Ready",
      tone: notesError ? "bad" : notesHealthy ? "ok" : "neutral",
      glyph: "N",
    },
  ];
  const quickLinkCards = quickLinks?.links.length ? quickLinks.links.map((link) => {
    const external = /^https?:\/\//i.test(link.url);
    return {
      id: link.id,
      title: link.title,
      detail: link.description || link.url,
      href: link.url,
      external,
      status: statusLabel(link.status),
      statusTone: quickLinkStatusTone(link.status),
    };
  }) : [];

  async function restartAgent() {
    try {
      const response = await homeClient.restartAgent();
      toast.showToast(response.message || "Agent restart requested.");
    } catch (error) {
      toast.showToast(error instanceof Error ? error.message : "Agent restart failed.", "error");
    }
  }

  if (!bootstrap.home) {
    return (
      <section className="dashboard-page home-dashboard" aria-labelledby="route-title">
        <header className="home-hero">
          <div>
            <p className="eyebrow">Hank</p>
            <h1 id="route-title">Dashboard</h1>
            <p>Signed in as <span>{userEmail}</span>. Create a home to connect your first agent.</p>
          </div>
          <a className="primary-action" href="/dashboard/settings/home">Create setup file</a>
        </header>
      </section>
    );
  }

  return (
    <section className="dashboard-page home-dashboard" aria-labelledby="route-title">
      <header className="home-hero">
        <div>
          <p className="eyebrow">Hank</p>
          <h1 id="route-title">{greeting()}, {userDisplayName}</h1>
          <p>{healthLooksGood ? `The primary agent and storage checks at ${homeName} look healthy.` : `${homeName} has a few things that need attention.`}</p>
          <div className={`home-mobile-connection tone-${online ? "ok" : "bad"}`} role="status" aria-label="Home connection">
            <span className="activity-dot" aria-hidden="true" />
            <strong>{online ? "Connected" : titleCase(agentStatus)}</strong>
            <small>{agent?.name || "Primary agent not registered"} · {relativeTime(agent?.last_seen_at)}</small>
          </div>
        </div>
        <div className="home-hero-actions">
          <button
            className="secondary"
            disabled={!agentPayload.can_restart || !online}
            onClick={() => void restartAgent()}
            type="button"
          >
            Restart agent
          </button>
          <a className="primary-action" href="/dashboard/settings/home">Create setup file</a>
        </div>
      </header>

      {bootstrap.setup_status?.first_setup_visible ? (
        <p className="notice-state">
          First-time setup is still visible. Create or refresh the agent setup file when you are ready to enroll another agent.
        </p>
      ) : null}

      <div className="home-metrics" aria-label="Overview">
        <article className="home-metric">
          <span>Primary agent</span>
          <strong>{online ? "Online" : titleCase(agentStatus)}</strong>
          <small>{agent?.name || "Not registered"} · {relativeTime(agent?.last_seen_at)}</small>
        </article>
        {bootstrap.membership?.role === "admin" ? <MonitoringHealthChecklist compact /> : (
          <article className="home-metric">
            <span>Monitoring</span>
            <strong>Admin access</strong>
            <small>Managed by your Home administrator</small>
          </article>
        )}
        <article className="home-metric">
          <span>Running jobs</span>
          <strong>{plural(activeTasks.length, "active", "active")}</strong>
          <small>{runningDetail}</small>
        </article>
        <article className="home-metric">
          <span>Last backup</span>
          <strong>{backupDisplay}</strong>
          <small>{backupDetail}</small>
        </article>
      </div>

      <div className="home-board">
        <div className="home-main-column">
          <section className="home-panel services-panel" aria-labelledby="services-title">
            <div className="home-panel-heading">
              <h2 id="services-title">Services</h2>
              <span className="home-services-heading-actions">
                <button
                  className="home-mobile-services-toggle"
                  type="button"
                  aria-expanded={serviceListExpanded}
                  aria-controls="home-service-list"
                  aria-label={`${serviceListExpanded ? "Hide healthy services" : "Show all services"}`}
                  onClick={() => setServiceListExpanded((expanded) => !expanded)}
                >
                  {serviceListExpanded ? "Attention only" : "All services"}
                </button>
                <a href="/dashboard/settings/connections">Connections</a>
              </span>
            </div>
            <div id="home-service-list" className={`service-list${serviceListExpanded ? " is-expanded" : ""}`}>
              {serviceRows.map((row) => (
                <article className={`service-row${row.tone === "ok" || row.tone === "neutral" ? " is-healthy" : " needs-attention"}`} key={row.title}>
                  <span className={`service-glyph tone-${row.tone}`} aria-hidden="true">{row.glyph}</span>
                  <span className="service-copy">
                    <strong>{row.title}</strong>
                    <small>{row.meta}</small>
                  </span>
                  <span className={`service-chip tone-${row.tone}`}>{row.status}</span>
                </article>
              ))}
            </div>
          </section>

          <section className="home-panel activity-panel" aria-labelledby="activity-title">
            <div className="home-panel-heading">
              <h2 id="activity-title">Recent activity</h2>
              <a href="/dashboard/settings/logs">View logs</a>
            </div>
            <div className="activity-list">
              {activityItems.map((item, index) => (
                <article className="activity-row" key={`${item.title}-${index}`}>
                  <span className={`activity-dot tone-${item.tone}`} aria-hidden="true" />
                  <span>
                    <strong>{item.title}</strong>
                    <small>{item.detail}</small>
                  </span>
                </article>
              ))}
            </div>
          </section>
        </div>

        <aside className="home-side-column">
          <section className="home-panel" aria-labelledby="quick-links-title">
            <div className="home-panel-heading">
              <h2 id="quick-links-title">Quick links</h2>
              {quickLinks?.can_edit ? <a href="/dashboard/settings/quick-links">Manage</a> : null}
            </div>
            {quickLinkCards.length ? (
              <div className="home-quick-grid">
                {quickLinkCards.slice(0, 4).map((link) => (
                  <a
                    aria-label={link.title}
                    className="home-quick-card"
                    href={link.href}
                    key={link.id}
                    rel={link.external ? "noreferrer" : undefined}
                    target={link.external ? "_blank" : undefined}
                  >
                    <strong>{link.title}</strong>
                    <small>{link.detail}</small>
                    {link.status ? <span className={`quick-status tone-${link.statusTone}`}>{link.status}</span> : null}
                  </a>
                ))}
              </div>
            ) : <p className="empty-state">No quick links saved.</p>}
          </section>

          <HomeAssistantControlsPanel payload={homeAssistant} />

          <section className="home-panel" aria-labelledby="people-title">
            <div className="home-panel-heading">
              <h2 id="people-title">People</h2>
              {bootstrap.permissions?.can_manage_people || bootstrap.permissions?.is_admin ? <a href="/dashboard/settings/people">Invite</a> : null}
            </div>
            <div className="people-list">
              {people.slice(0, 4).map((person) => {
                const label = person.display_name?.trim() || person.email;
                return (
                  <article className="person-row" key={person.user_id || person.email}>
                    <span className="person-avatar" aria-hidden="true">{initials(label)}</span>
                    <span>
                      <strong>{label}</strong>
                      <small>{person.display_name ? `${person.email} · ` : ""}{titleCase(person.role)}</small>
                    </span>
                  </article>
                );
              })}
            </div>
          </section>
        </aside>
      </div>
    </section>
  );
}
