import { SidebarDividerToggle } from "./SidebarDividerToggle";
import { type MouseEvent, type ReactNode, useEffect, useRef, useState } from "react";
import { InstallHankAction } from "../pwa/InstallHankAction";
import { internalNavigationTarget } from "../router";
import type { GroupedNavItem } from "./navConfig";
import { searchClient, type SearchResult } from "../api/search";
import { NotificationInbox } from "../notifications/NotificationInbox";
import { useOptionalNotifications } from "../notifications/NotificationProvider";

/* Minimal dependency-free icon set keyed by route path. */
function NavIcon({ href }: { href: string }) {
  const p =
    href === "/dashboard" ? "M3 10.5 12 3l9 7.5V21H3z" :
    href === "/dashboard/hank" ? "M12 3l2.5 5.5L20 11l-5.5 2.5L12 19l-2.5-5.5L4 11l5.5-2.5z" :
    href === "/dashboard/home-assistant" ? "M9 21h6m-3-4v4M7 13a5 5 0 1 1 10 0c0 2-2 3-2.5 4h-5C9 16 7 15 7 13z" :
    href === "/dashboard/profile-notes" ? "M6 3h9l3 3v15H6zM9 8h6M9 12h6M9 16h4" :
    href === "/dashboard/file-server" ? "M3 7h6l2 2h10v11H3z" :
    href.startsWith("/dashboard/settings") ? "M12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6zM4 12h2m12 0h2M12 4v2m0 12v2" :
    href === "/docs/deployment" ? "M5 4h14v16H5zM8 8h8M8 12h8M8 16h5" :
    "M5 12h14"; // fallback
  return (
    <svg className="tab-link-icon" viewBox="0 0 24 24" width="18" height="18"
      fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d={p} />
    </svg>
  );
}

const RESULT_GLYPH: Record<string, string> = {
  page: "↵", note: "✎", kanban_card: "▤", machine: "⌘", quick_link: "↗", app: "▦", member: "@", file: "▤", homeassistant: "⌂",
};

const COLLAPSE_KEY = "hank.nav.collapsed";
const MOBILE_PRIMARY_HREFS = new Set([
  "/dashboard",
  "/dashboard/hank",
  "/dashboard/profile-notes",
  "/dashboard/home-assistant",
  "/dashboard/file-server",
]);

function initialsFor(value: string): string {
  const local = value.includes("@") ? value.split("@")[0] : value;
  const parts = local.split(/[._\-\s]+/).filter(Boolean);
  const letters = parts.length > 1 ? `${parts[0][0]}${parts[1][0]}` : local.slice(0, 2);
  return (letters || "HK").toUpperCase();
}

function footerNameFor(value: string): string {
  if (!value.includes("@")) return value;
  const local = value.split("@")[0] || value;
  return local.replace(/[._-]+/g, " ");
}

export function Shell({
  navItems,
  currentPath,
  onNavigate,
  onPrefetch,
  children,
  onLogout,
  userEmail,
  userDisplayName,
  userRole,
  primaryAgentOnline = true,
}: {
  navItems: GroupedNavItem[];
  currentPath: string;
  onNavigate: (href: string) => void;
  onPrefetch?: (href: string) => void;
  onLogout: () => void;
  children: ReactNode;
  userEmail?: string;
  userDisplayName?: string;
  userRole?: string;
  primaryAgentOnline?: boolean;
}) {
  const [collapsed, setCollapsed] = useState<boolean>(() => {
    try { return localStorage.getItem(COLLAPSE_KEY) === "1"; } catch { return false; }
  });
  const [notifOpen, setNotifOpen] = useState(false);
  const [notifClosing, setNotifClosing] = useState(false);
  const notificationContext = useOptionalNotifications();
  const [fallbackUnreadCount, setFallbackUnreadCount] = useState(0);
  const unreadCount = notificationContext?.unreadCount ?? fallbackUnreadCount;
  const notifButtonRef = useRef<HTMLButtonElement>(null);
  const notifPopoverRef = useRef<HTMLDivElement>(null);
  const notifCloseTimerRef = useRef<number | null>(null);

  // --- global search state ---
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<SearchResult[]>([]);
  const [searchStatus, setSearchStatus] = useState<"idle" | "loading" | "ready" | "error">("idle");
  const [fileIndexStatus, setFileIndexStatus] = useState<"ready" | "indexing" | "partial" | "offline" | undefined>();
  const [searchOpen, setSearchOpen] = useState(false);
  const [activeIdx, setActiveIdx] = useState(0);
  const searchInputRef = useRef<HTMLInputElement>(null);
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);
  const [mobileSearchOpen, setMobileSearchOpen] = useState(false);
  const mobileMenuButtonRef = useRef<HTMLButtonElement>(null);
  const mobileSearchButtonRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    try { localStorage.setItem(COLLAPSE_KEY, collapsed ? "1" : "0"); } catch { /* ignore */ }
  }, [collapsed]);

  useEffect(() => () => {
    if (notifCloseTimerRef.current !== null) window.clearTimeout(notifCloseTimerRef.current);
  }, []);

  function openNotifications() {
    if (notifCloseTimerRef.current !== null) window.clearTimeout(notifCloseTimerRef.current);
    notifCloseTimerRef.current = null;
    setNotifClosing(false);
    setNotifOpen(true);
  }

  function closeNotifications() {
    if (!notifOpen) return;
    setNotifOpen(false);
    setNotifClosing(true);
    if (notifCloseTimerRef.current !== null) window.clearTimeout(notifCloseTimerRef.current);
    notifCloseTimerRef.current = window.setTimeout(() => {
      setNotifClosing(false);
      notifCloseTimerRef.current = null;
    }, 100);
  }

  // debounced search
  useEffect(() => {
    const q = query.trim();
    if (!q) {
      setResults([]);
      setSearchStatus("idle");
      setFileIndexStatus(undefined);
      return;
    }
    const controller = new AbortController();
    setResults([]);
    setSearchStatus("loading");
    const handle = window.setTimeout(() => {
      searchClient.search(q, controller.signal)
        .then((output) => {
          setResults(output.results);
          setFileIndexStatus(output.fileIndexStatus);
          setActiveIdx(0);
          setSearchStatus("ready");
        })
        .catch((error) => {
          if (controller.signal.aborted || (error instanceof DOMException && error.name === "AbortError")) return;
          setResults([]);
          setSearchStatus("error");
        });
    }, 180);
    return () => { controller.abort(); window.clearTimeout(handle); };
  }, [query]);

  // Close the notifications popover when clicking anywhere outside it.
  useEffect(() => {
    if (!notifOpen) return;
    function onPointerDown(event: PointerEvent) {
      const target = event.target as Node;
      if (notifPopoverRef.current?.contains(target) || notifButtonRef.current?.contains(target)) return;
      closeNotifications();
    }
    function onEscape(event: KeyboardEvent) {
      if (event.key === "Escape") {
        closeNotifications();
        notifButtonRef.current?.focus();
      }
    }
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onEscape);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onEscape);
    };
  }, [notifOpen]);

  // ⌘K / Ctrl-K focuses search and Escape dismisses mobile overlays.
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        searchInputRef.current?.focus();
        setSearchOpen(true);
      }
      if (e.key !== "Escape") return;
      setSearchOpen(false);
      if (mobileMenuOpen) {
        setMobileMenuOpen(false);
        mobileMenuButtonRef.current?.focus();
      }
      if (mobileSearchOpen) {
        setMobileSearchOpen(false);
        mobileSearchButtonRef.current?.focus();
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [mobileMenuOpen, mobileSearchOpen]);

  function chooseResult(result: SearchResult) {
    setSearchOpen(false);
    setQuery("");
    setResults([]);
    setSearchStatus("idle");
    if (result.external) {
      window.open(result.url, "_blank", "noopener");
    } else {
      onNavigate(result.url);
    }
  }

  function onSearchKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (!results.length) return;
    if (e.key === "ArrowDown") { e.preventDefault(); setActiveIdx((i) => (i + 1) % results.length); }
    else if (e.key === "ArrowUp") { e.preventDefault(); setActiveIdx((i) => (i - 1 + results.length) % results.length); }
    else if (e.key === "Enter") { e.preventDefault(); chooseResult(results[activeIdx]); }
  }

  function handleClick(event: MouseEvent<HTMLDivElement>) {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.altKey || event.ctrlKey || event.shiftKey) {
      return;
    }
    const anchor = (event.target as HTMLElement).closest("a");
    if (!anchor || anchor.target || anchor.hasAttribute("download")) {
      return;
    }
    const target = internalNavigationTarget(anchor.href);
    if (!target) {
      return;
    }
    event.preventDefault();
    onNavigate(target);
  }

  const settingsActive = currentPath.startsWith("/dashboard/settings");
  function isActive(href: string) {
    if (href === currentPath) return true;
    if (href === "/dashboard/settings" && settingsActive) return true;
    return false;
  }
  const current = navItems.find((item) => isActive(item.href));
  const crumbLabel = current && current.href !== "/dashboard" ? current.label : null;
  const showResults = searchOpen && query.trim().length > 0;
  const footerName = userDisplayName?.trim() || footerNameFor(userEmail || "Aaron D.");
  const roleLabel = userRole || "admin";
  const initials = initialsFor(footerName);
  const mobilePrimaryItems = navItems.filter((item) => MOBILE_PRIMARY_HREFS.has(item.href));
  const mobileOverflowItems = navItems.filter((item) => !MOBILE_PRIMARY_HREFS.has(item.href));

  return (
    <div
      className="app-shell"
      data-nav-collapsed={collapsed ? "true" : "false"}
      data-mobile-search-open={mobileSearchOpen ? "true" : "false"}
      data-mobile-menu-open={mobileMenuOpen ? "true" : "false"}
      onClick={handleClick}
    >
      <SidebarDividerToggle
        className="app-nav-divider-toggle"
        expanded={!collapsed}
        controls="main-sidebar"
        expandLabel="Expand sidebar"
        collapseLabel="Collapse sidebar"
        onToggle={() => setCollapsed((value) => !value)}
      />
      <nav id="main-sidebar" className="app-nav" aria-label="Main">
        <div className="sidebar-brand">
          <img className="sidebar-brand-icon" src="/assets/hank-icon-192.png" alt="" />
          <div className="sidebar-brand-copy">
            <div className="sidebar-title">Hank</div>
            <div className="sidebar-subtitle">home server</div>
          </div>
        </div>

        {navItems.map((item) => {
          return (
            <div key={item.href} style={{ display: "contents" }}>
              <a
                className="tab-link"
                href={item.href}
                title={item.label}
                aria-current={isActive(item.href) ? "page" : undefined}
                onFocus={() => onPrefetch?.(item.href)}
                onMouseEnter={() => onPrefetch?.(item.href)}
                onTouchStart={() => onPrefetch?.(item.href)}
              >
                <NavIcon href={item.href} />
                <span className="tab-link-label">{item.label}</span>
              </a>
            </div>
          );
        })}

        <div className="nav-footer" aria-label="Session status">
          <div className="nav-footer-status">
            <span className={`status-dot ${primaryAgentOnline ? "ok" : "warn"}`} aria-hidden="true" />
            <span>{primaryAgentOnline ? "Primary agent online" : "Primary agent offline"}</span>
            <small>{primaryAgentOnline ? "2m" : "now"}</small>
          </div>
          <div className="nav-footer-rule" />
          <div className="nav-footer-user">
            <span className="nav-footer-avatar" aria-hidden="true">{initials}</span>
            <span className="nav-footer-text">
              <strong title={userEmail}>{footerName}</strong>
              <small>{roleLabel}</small>
            </span>
            <button className="nav-footer-signout" type="button" onClick={onLogout} title="Sign out" aria-label="Sign out">
              <svg className="tab-link-icon" viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                <path d="M15 12H4m0 0 3.5-3.5M4 12l3.5 3.5M14 5h4a1 1 0 0 1 1 1v12a1 1 0 0 1-1 1h-4" />
              </svg>
            </button>
          </div>
        </div>
      </nav>

      <div className="app-content">
        <header className="app-topbar">
          <div className="mobile-topbar-title">
            <img src="/assets/hank-icon-192.png" alt="" />
            <strong>{current?.label || "Hank"}</strong>
          </div>
          <nav className="topbar-crumbs" aria-label="Breadcrumb">
            <a href="/dashboard">Home</a>
            {crumbLabel ? (
              <>
                <span aria-hidden="true">›</span>
                <span className="crumb-current">{crumbLabel}</span>
              </>
            ) : null}
          </nav>
          <div className="topbar-actions" style={{ marginLeft: "auto", display: "flex", alignItems: "center", gap: 10 }}>
            <button
              ref={mobileSearchButtonRef}
              className="mobile-topbar-action"
              type="button"
              aria-label="Open search"
              onClick={() => {
                closeNotifications();
                setMobileMenuOpen(false);
                setMobileSearchOpen(true);
                window.requestAnimationFrame(() => searchInputRef.current?.focus());
              }}
            >
              <svg viewBox="0 0 24 24" aria-hidden="true">
                <circle cx="11" cy="11" r="7" />
                <path d="m20 20-3-3" />
              </svg>
            </button>
            <div className="topbar-search-wrap">
              <label className="topbar-search">
                <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.7" aria-hidden="true">
                  <circle cx="11" cy="11" r="7" /><path d="m20 20-3-3" strokeLinecap="round" />
                </svg>
                <input
                  ref={searchInputRef}
                  type="search"
                  placeholder="Search Hank..."
                  aria-label="Search"
                  value={query}
                  onChange={(e) => { setQuery(e.target.value); setSearchOpen(true); closeNotifications(); }}
                  onFocus={() => { setSearchOpen(true); closeNotifications(); }}
                  onBlur={() => window.setTimeout(() => setSearchOpen(false), 120)}
                  onKeyDown={onSearchKeyDown}
                />
                <kbd>⌘K</kbd>
              </label>
              {mobileSearchOpen ? (
                <button
                  className="mobile-search-close"
                  type="button"
                  aria-label="Close search"
                  onClick={() => {
                    setMobileSearchOpen(false);
                    setSearchOpen(false);
                    mobileSearchButtonRef.current?.focus();
                  }}
                >
                  ×
                </button>
              ) : null}
              {showResults ? (
                <div className="search-results" role="listbox">
                  {searchStatus === "loading" ? (
                    <div className="search-empty" role="status">Searching…</div>
                  ) : searchStatus === "error" ? (
                    <div className="search-empty" role="alert">Search is unavailable. Try again.</div>
                  ) : results.length === 0 && (fileIndexStatus === undefined || fileIndexStatus === "ready") ? (
                    <div className="search-empty">No matches for “{query.trim()}”.</div>
                  ) : (
                    <>
                    {fileIndexStatus && fileIndexStatus !== "ready" ? <div className="search-empty" role="status">{fileIndexStatus === "indexing" ? "Files are still indexing; results may be incomplete." : fileIndexStatus === "partial" ? "Some file folders could not be indexed." : "File agents are offline; showing available results."}</div> : null}
                    {results.map((result, idx) => (
                      <button
                        type="button"
                        role="option"
                        aria-selected={idx === activeIdx}
                        key={`${result.type}:${result.url}:${idx}`}
                        className={`search-result${idx === activeIdx ? " is-active" : ""}`}
                        onMouseEnter={() => setActiveIdx(idx)}
                        onMouseDown={(e) => { e.preventDefault(); chooseResult(result); }}
                      >
                        <span className="search-result-glyph" aria-hidden="true">{RESULT_GLYPH[result.type] ?? "•"}</span>
                        <span className="search-result-text">
                          <span className="search-result-title">{result.title}</span>
                          {result.subtitle ? <span className="search-result-sub">{result.subtitle}</span> : null}
                        </span>
                        <span className="search-result-kind">{result.type.replace("_", " ")}</span>
                      </button>
                    ))}
                    </>
                  )}
                </div>
              ) : null}
            </div>
            <span className="operational-pill">
              <span className="status-dot ok" aria-hidden="true" />
              Operational
            </span>
            <button
              ref={notifButtonRef}
              type="button"
              className={`topbar-icon-btn${unreadCount ? " has-notifications" : ""}`}
              aria-label={unreadCount ? `Notifications, ${unreadCount} unread` : "Notifications"}
              aria-expanded={notifOpen}
              onClick={(e) => {
                e.preventDefault();
                setSearchOpen(false);
                if (notifOpen) closeNotifications();
                else openNotifications();
              }}
            >
              <svg viewBox="0 0 24 24" width="17" height="17" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                <path d="M6 9a6 6 0 0 1 12 0c0 5 2 6 2 6H4s2-1 2-6M10 20a2 2 0 0 0 4 0" />
              </svg>
              {unreadCount ? <span className="notification-unread-badge" aria-label={`${unreadCount} unread`}>{unreadCount > 99 ? "99+" : unreadCount}</span> : null}
            </button>
            {notifOpen || notifClosing ? (
              <div
                className="notif-popover"
                data-state={notifOpen ? "open" : "closing"}
                role="dialog"
                aria-label="Notifications"
                aria-hidden={notifClosing || undefined}
                ref={notifPopoverRef}
              >
                <NotificationInbox onClose={closeNotifications} onNavigate={onNavigate} onUnreadCount={setFallbackUnreadCount} />
              </div>
            ) : null}
            <button
              ref={mobileMenuButtonRef}
              className="mobile-topbar-action"
              type="button"
              aria-label="Open menu"
              aria-expanded={mobileMenuOpen}
              onClick={() => {
                closeNotifications();
                setMobileSearchOpen(false);
                setMobileMenuOpen((open) => !open);
              }}
            >
              <svg viewBox="0 0 24 24" aria-hidden="true">
                <path d="M5 7h14M5 12h14M5 17h14" />
              </svg>
            </button>
          </div>
        </header>
        <main className="app-main">{children}</main>
      </div>
      <nav className="mobile-bottom-nav" aria-label="Mobile primary">
        {mobilePrimaryItems.map((item) => (
          <a
            key={item.href}
            href={item.href}
            aria-label={item.label}
            aria-current={isActive(item.href) ? "page" : undefined}
            onFocus={() => onPrefetch?.(item.href)}
            onTouchStart={() => onPrefetch?.(item.href)}
          >
            <NavIcon href={item.href} />
            <span>{item.label === "Home Assistant" ? "HA" : item.label === "File Server" ? "Files" : item.label}</span>
          </a>
        ))}
      </nav>
      {mobileMenuOpen ? (
        <div
          className="mobile-menu-scrim"
          role="presentation"
          onPointerDown={() => {
            setMobileMenuOpen(false);
            mobileMenuButtonRef.current?.focus();
          }}
        >
          <section
            className="mobile-menu"
            role="dialog"
            aria-modal="true"
            aria-label="Mobile menu"
            onPointerDown={(event) => event.stopPropagation()}
          >
            <header>
              <strong>{footerName}</strong>
              <button
                type="button"
                aria-label="Close menu"
                onClick={() => {
                  setMobileMenuOpen(false);
                  mobileMenuButtonRef.current?.focus();
                }}
              >
                ×
              </button>
            </header>
            <p>{primaryAgentOnline ? "Primary agent online" : "Primary agent offline"} · {roleLabel}</p>
            <nav aria-label="More destinations">
              {mobileOverflowItems.map((item) => (
                <a key={item.href} href={item.href} onClick={() => setMobileMenuOpen(false)}>
                  <NavIcon href={item.href} />
                  <span>{item.label}</span>
                </a>
              ))}
            </nav>
            <InstallHankAction onComplete={() => setMobileMenuOpen(false)} />
            <button type="button" onClick={onLogout}>Sign out</button>
          </section>
        </div>
      ) : null}
    </div>
  );
}
