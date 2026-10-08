import { Fragment, lazy, Suspense, useEffect, useMemo, useState, type ReactNode } from "react";
import { Shell } from "./ui/Shell";
import { primaryNav } from "./ui/navConfig";
import { SettingsLayout, settingsLandingPath } from "./settings/SettingsLayout";
import { authClient } from "./api/auth";
import { ConfirmDialogProvider, ToastProvider } from "./ui/primitives";
import { bootstrapClient, type BootstrapState } from "./api/bootstrap";
import { ApiError } from "./api/client";
import { JoinPage } from "./auth/JoinPage";
import { LoginPage } from "./auth/LoginPage";
import { PasswordChangePage } from "./auth/PasswordChangePage";
import { redirectTo } from "./browser/navigation";
import { ensureBrowserDesktopIdentity } from "./desktop/autoEnrollment";
import { agentWorkspaceForPath, routeCachePath, routeForPath, type RouteDefinition } from "./router";
import { OfflineFeaturePage } from "./offlineNotes/OfflineFeaturePage";
import { useOfflineNotes } from "./offlineNotes/OfflineNotesProvider";
import type { OfflineIdentity } from "./offlineNotes/types";
import { NotificationProvider } from "./notifications/NotificationProvider";
import { unsubscribeFromWebPush } from "./pwa/push";

const AgentsPage = lazy(() => import("./dashboard/AgentsPage").then((module) => ({ default: module.AgentsPage })));
const DashboardHome = lazy(() => import("./dashboard/DashboardHome").then((module) => ({ default: module.DashboardHome })));
const DeploymentGuide = lazy(() => import("./dashboard/DeploymentGuide").then((module) => ({ default: module.DeploymentGuide })));
const FileServerPage = lazy(() => import("./dashboard/FileServerPage").then((module) => ({ default: module.FileServerPage })));
const HankAIPage = lazy(() => import("./dashboard/HankAIPage").then((module) => ({ default: module.HankAIPage })));
const HomeAssistantPage = lazy(() => import("./dashboard/HomeAssistantPage").then((module) => ({ default: module.HomeAssistantPage })));
const ProfileNotesPage = lazy(() => import("./dashboard/ProfileNotesPage").then((module) => ({ default: module.ProfileNotesPage })));
const AssistantSettings = lazy(() => import("./settings/AssistantSettings").then((module) => ({ default: module.AssistantSettings })));
const AppsSettings = lazy(() => import("./settings/AppsSettings").then((module) => ({ default: module.AppsSettings })));
const AttachmentsSettings = lazy(() => import("./settings/AttachmentsSettings").then((module) => ({ default: module.AttachmentsSettings })));
const BackupsSettings = lazy(() => import("./settings/BackupsSettings").then((module) => ({ default: module.BackupsSettings })));
const ConnectionsSettings = lazy(() => import("./settings/ConnectionsSettings").then((module) => ({ default: module.ConnectionsSettings })));
const HomeSettings = lazy(() => import("./settings/HomeSettings").then((module) => ({ default: module.HomeSettings })));
const JoinHomeSettings = lazy(() => import("./settings/JoinHomeSettings").then((module) => ({ default: module.JoinHomeSettings })));
const LogsSettings = lazy(() => import("./settings/LogsSettings").then((module) => ({ default: module.LogsSettings })));
const PeopleSettings = lazy(() => import("./settings/PeopleSettings").then((module) => ({ default: module.PeopleSettings })));
const QuickLinksSettings = lazy(() => import("./settings/QuickLinksSettings").then((module) => ({ default: module.QuickLinksSettings })));
const RecoverySettings = lazy(() => import("./settings/RecoverySettings").then((module) => ({ default: module.RecoverySettings })));
const NotificationSettingsPage = lazy(() => import("./pages/NotificationSettings").then((module) => ({ default: module.NotificationSettingsPage })));

function RouteStub({ route }: { route: RouteDefinition }) {
  return (
    <section className="route-panel" aria-labelledby="route-title">
      <div>
        <p className="eyebrow">Hank</p>
        <h1 id="route-title">{route.heading}</h1>
      </div>
    </section>
  );
}

function RouteLoadingPage({ route }: { route: RouteDefinition }) {
  const heading = route.path === "/dashboard/profile-notes"
    ? "Loading notes"
    : route.path === "/dashboard/file-server"
      ? "Loading files"
      : route.path === "/docs/deployment"
        ? "Setup Guide"
        : route.heading;
  return (
    <section className={route.path.startsWith("/dashboard/settings") ? "settings-page" : "dashboard-page"} aria-labelledby="route-title">
      <p className="eyebrow">Hank</p>
      <h1 id="route-title">{heading}</h1>
      <p className="loading-state"><span className="spinner" aria-hidden="true" />Loading…</p>
    </section>
  );
}

function SettingsLoadingPage() {
  return (
    <section className="settings-page" aria-labelledby="route-title">
      <p className="eyebrow">Hank</p>
      <h1 id="route-title">Settings</h1>
      <p className="loading-state"><span className="spinner" aria-hidden="true" />Loading settings...</p>
    </section>
  );
}

function PublicRoute({ route }: { route: RouteDefinition }) {
  if (route.path === "/") return <LoginPage />;
  if (route.path === "/join") return <JoinPage />;
  if (route.path === "/password-change") return <PasswordChangePage />;
  return <RouteStub route={route} />;
}

function currentPathname(): string {
  return window.location.pathname;
}

function currentLocationTarget(): string {
  return `${window.location.pathname}${window.location.search}${window.location.hash}`;
}

function initialMountedRoutePaths(): string[] {
  const route = routeForPath(currentPathname());
  return route.publicRoute ? [] : [routeCachePath(route)];
}

function initialMountedRouteTargets(): Record<string, string> {
  const route = routeForPath(currentPathname());
  return route.publicRoute ? {} : { [routeCachePath(route)]: route.path };
}

function initialMountedRouteLocations(): Record<string, string> {
  const route = routeForPath(currentPathname());
  return route.publicRoute ? {} : { [routeCachePath(route)]: currentLocationTarget() };
}

function appendMountedRoutePath(paths: string[], path: string): string[] {
  return paths.includes(path) ? paths : [...paths, path];
}

// Resolve the page element for a route path (settings pages included).
function pageForRoute(route: RouteDefinition, bootstrap?: BootstrapState | null): ReactNode {
  const workspace = agentWorkspaceForPath(route.path);
  if (workspace) return <AgentsPage initialAgentID={workspace.agentID} initialTab={workspace.tab} />;
  switch (route.path) {
    case "/dashboard": return <DashboardHome />;
    case "/dashboard/hank": return <HankAIPage />;
    case "/docs/deployment": return <DeploymentGuide />;
    case "/dashboard/home-assistant": return <HomeAssistantPage />;
    case "/dashboard/file-server": return <FileServerPage isAdmin={bootstrap?.permissions.is_admin ?? false} />;
    case "/dashboard/agents": return <AgentsPage />;
    case "/dashboard/profile-notes": return <ProfileNotesPage />;
    case "/dashboard/notifications/event": return <LogsSettings />;
    case "/dashboard/settings": return <HomeSettings />;
    case "/dashboard/settings/home": return <HomeSettings />;
    case "/dashboard/settings/people": return <PeopleSettings />;
    case "/dashboard/settings/ai": return <AssistantSettings />;
    case "/dashboard/settings/apps": return <AppsSettings />;
    case "/dashboard/settings/attachments": return <AttachmentsSettings />;
    case "/dashboard/settings/backups": return <BackupsSettings />;
    case "/dashboard/settings/connections": return <ConnectionsSettings />;
    case "/dashboard/settings/join-home": return <JoinHomeSettings />;
    case "/dashboard/settings/logs": return <LogsSettings />;
    case "/dashboard/settings/quick-links": return <QuickLinksSettings />;
    case "/dashboard/settings/recovery": return <RecoverySettings />;
    case "/dashboard/settings/notifications": return <NotificationSettingsPage isAdmin={Boolean(bootstrap?.permissions.is_admin)} />;
    default: return <RouteStub route={route} />;
  }
}

export function App() {
  const [pathname, setPathname] = useState(currentPathname);
  const [bootstrap, setBootstrap] = useState<BootstrapState | null>(null);
  const [offlineIdentity, setOfflineIdentity] = useState<OfflineIdentity | null>(null);
  const offlineNotes = useOfflineNotes();
  const [mountedRoutePaths, setMountedRoutePaths] = useState<string[]>(initialMountedRoutePaths);
  const [mountedRouteTargets, setMountedRouteTargets] = useState<Record<string, string>>(initialMountedRouteTargets);
  const [mountedRouteLocations, setMountedRouteLocations] = useState<Record<string, string>>(initialMountedRouteLocations);
  const route = routeForPath(pathname);

  useEffect(() => {
    function handlePopState() {
      const nextPath = currentPathname();
      const nextRoute = routeForPath(nextPath);
      setPathname(nextPath);
      if (!nextRoute.publicRoute) {
        setMountedRouteLocations((locations) => ({
          ...locations,
          [routeCachePath(nextRoute)]: currentLocationTarget(),
        }));
      }
    }
    window.addEventListener("popstate", handlePopState);
    return () => window.removeEventListener("popstate", handlePopState);
  }, []);

  useEffect(() => {
    if (route.publicRoute) {
      setBootstrap(null);
      setOfflineIdentity(null);
      return;
    }
    if (bootstrap || offlineIdentity) return;
    let alive = true;
    bootstrapClient.load()
      .then(async (payload) => {
        if (payload.user.password_change_required && currentPathname() !== "/password-change") {
          redirectTo("/password-change");
          return;
        }
        try {
          await offlineNotes.activateAuthenticated(payload);
        } catch {
          // IndexedDB availability must not block the live dashboard.
        }
        if (alive) {
          setOfflineIdentity(null);
          setBootstrap(payload);
        }
      })
      .catch(async (error) => {
        if (!alive) return;
        if (error instanceof ApiError && error.status === 401) {
          redirectTo("/?expired=1");
          return;
        }
        if (error instanceof TypeError) {
          try {
            const identity = await offlineNotes.activateOffline();
            if (alive && identity) setOfflineIdentity(identity);
          } catch {
            // The protected shell remains locked when local storage cannot open.
          }
        }
        if (alive) setBootstrap(null);
      });
    return () => {
      alive = false;
    };
  }, [bootstrap, offlineIdentity, offlineNotes.activateAuthenticated, offlineNotes.activateOffline, route.publicRoute]);

  useEffect(() => {
    if (!bootstrap?.permissions.is_admin || !bootstrap.home?.id || !bootstrap.user.id || !bootstrap.session.id) return;
    void ensureBrowserDesktopIdentity(bootstrap.home.id, bootstrap.user.id, bootstrap.session.id).catch((error: unknown) => {
      const detail = error instanceof Error ? error.message : "desktop_browser_enrollment_failed";
      window.dispatchEvent(new CustomEvent("hank-desktop-auto-enrollment-failed", { detail }));
    });
  }, [bootstrap?.home?.id, bootstrap?.permissions.is_admin, bootstrap?.session.id, bootstrap?.user.id]);

  useEffect(() => {
    if (route.publicRoute) {
      setMountedRoutePaths([]);
      setMountedRouteTargets({});
      setMountedRouteLocations({});
      return;
    }
    const cachePath = routeCachePath(route);
    setMountedRoutePaths((paths) => appendMountedRoutePath(paths, cachePath));
    setMountedRouteTargets((targets) => targets[cachePath] === route.path ? targets : { ...targets, [cachePath]: route.path });
    setMountedRouteLocations((locations) => {
      const target = currentLocationTarget();
      return locations[cachePath] === target ? locations : { ...locations, [cachePath]: target };
    });
  }, [route.path, route.publicRoute]);

  function navigateTo(href: string) {
    const url = new URL(href, window.location.origin);
    const nextPath = url.pathname;
    if (nextPath === pathname && url.search === window.location.search && url.hash === window.location.hash) {
      return;
    }
    window.history.pushState({}, "", `${url.pathname}${url.search}${url.hash}`);
    const nextRoute = routeForPath(nextPath);
    if (!nextRoute.publicRoute) {
      setMountedRouteLocations((locations) => ({
        ...locations,
        [routeCachePath(nextRoute)]: `${url.pathname}${url.search}${url.hash}`,
      }));
    }
    setPathname(nextPath);
  }

  function prefetchRoute(href: string) {
    let url: URL;
    try {
      url = new URL(href, window.location.origin);
    } catch {
      return;
    }
    const targetRoute = routeForPath(url.pathname);
    if (targetRoute.publicRoute) return;
    setMountedRoutePaths((paths) => appendMountedRoutePath(paths, routeCachePath(targetRoute)));
  }

  const isAdmin = Boolean(bootstrap?.permissions?.is_admin ?? offlineIdentity?.permissions.is_admin);
  const visibleNavItems = useMemo(() => primaryNav, []);

  async function logout() {
    try {
      try {
        await offlineNotes.purgeActive();
      } catch {
        // A local storage failure must not leave the server session active.
      }
      try {
        const registration = await navigator.serviceWorker?.getRegistration();
        if (registration) await unsubscribeFromWebPush(registration);
      } catch {
        // Server logout still revokes the session-owned subscription.
      }
      await authClient.logout();
    } finally {
      redirectTo("/");
    }
  }

  function renderCachedRoute(path: string) {
    const activeCachePath = routeCachePath(route);
    const cachedRoute = path === activeCachePath ? route : routeForPath(mountedRouteTargets[path] ?? path);
    const isSettingsRoot = cachedRoute.path === "/dashboard/settings";
    const resolvedRoute = isSettingsRoot && bootstrap ? routeForPath(settingsLandingPath(isAdmin)) : cachedRoute;
    const page = offlineNotes.mode === "locked" && resolvedRoute.path === "/dashboard/profile-notes"
      ? <OfflineFeaturePage notesLocked />
      : offlineIdentity && resolvedRoute.path !== "/dashboard/profile-notes"
      ? <OfflineFeaturePage />
      : isSettingsRoot && !bootstrap
        ? <SettingsLoadingPage />
        : pageForRoute(resolvedRoute, bootstrap);
    const content = cachedRoute.path.startsWith("/dashboard/settings") ? (
      <SettingsLayout currentPath={cachedRoute.path} isAdmin={isAdmin} onPrefetch={prefetchRoute}>
        {page}
      </SettingsLayout>
    ) : page;
    return (
      <Fragment key={mountedRouteLocations[path] || cachedRoute.path}>
        <Suspense fallback={<RouteLoadingPage route={resolvedRoute} />}>
          {content}
        </Suspense>
      </Fragment>
    );
  }

  const activeCachePath = routeCachePath(route);
  const visibleRoutePaths = route.publicRoute ? [] : appendMountedRoutePath(mountedRoutePaths, activeCachePath);
  const cachedRouteContent = (
    <>
      {visibleRoutePaths.map((path) => {
        const page = renderCachedRoute(path);
        const hidden = path !== activeCachePath;
        return (
          <div aria-hidden={hidden ? "true" : undefined} className="route-cache-panel" hidden={hidden} key={path}>
            {page}
          </div>
        );
      })}
    </>
  );

  const content = route.publicRoute ? (
    <main className="auth-surface">
      <PublicRoute route={route} />
    </main>
  ) : (
    <Shell
      navItems={visibleNavItems}
      currentPath={route.path}
      onNavigate={navigateTo}
      onPrefetch={prefetchRoute}
      onLogout={() => void logout()}
      userDisplayName={bootstrap?.user.display_name ?? offlineIdentity?.displayName}
      userEmail={bootstrap?.user.email ?? offlineIdentity?.email}
      userRole={bootstrap?.membership?.role ?? offlineIdentity?.role}
      primaryAgentOnline={bootstrap?.agent?.status?.toLowerCase() === "online"}
    >
      {cachedRouteContent}
    </Shell>
  );

  return (
    <NotificationProvider userID={bootstrap?.user.id}>
      <ToastProvider>
        <ConfirmDialogProvider>
          {content}
        </ConfirmDialogProvider>
      </ToastProvider>
    </NotificationProvider>
  );
}
