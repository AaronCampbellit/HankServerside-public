import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const styles = readFileSync("src/styles.css", "utf8");

function ruleBodies(selector: string): string[] {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return [...styles.matchAll(new RegExp(`${escaped}\\s*\\{(?<body>[^}]+)\\}`, "g"))].map((match) => match.groups?.body || "");
}

function lastRuleBody(selector: string): string {
  return ruleBodies(selector).at(-1) || "";
}

function isMediaRule(rule: CSSRule): rule is CSSMediaRule {
  return "conditionText" in rule && "cssRules" in rule;
}

function isStyleRule(rule: CSSRule): rule is CSSStyleRule {
  return "selectorText" in rule && "style" in rule;
}

function selectorKey(selector: string): string {
  return selector.split(",").map((part) => part.trim()).join(",");
}

function parsedTopLevelRules(): CSSRule[] {
  const style = document.createElement("style");
  style.textContent = styles;
  document.head.append(style);
  const rules = [...(style.sheet?.cssRules || [])];
  style.remove();
  return rules;
}

function topLevelStyleRules(selector: string): CSSStyleRule[] {
  const key = selectorKey(selector);
  return parsedTopLevelRules()
    .filter(isStyleRule)
    .filter((rule) => selectorKey(rule.selectorText) === key);
}

function mediaStyleRules(condition: string, selector: string): CSSStyleRule[] {
  const key = selectorKey(selector);
  return parsedTopLevelRules()
    .filter(isMediaRule)
    .filter((rule) => rule.conditionText === condition)
    .flatMap((rule) => [...rule.cssRules].filter(isStyleRule))
    .filter((rule) => selectorKey(rule.selectorText) === key);
}

describe("dashboard stylesheet", () => {
  it("keeps the expanded desktop sidebar slim", () => {
    expect(styles).toContain("--side-nav-width: 188px");
    expect(styles).toContain("--nav-w-collapsed: 58px");
    expect(ruleBodies(".app-nav").some((body) => body.includes("padding: 14px 8px"))).toBe(true);
  });

  it("anchors sidebar toggles to the divider and hides the main toggle on mobile", () => {
    const divider = topLevelStyleRules(".sidebar-divider-toggle").at(-1);
    const mobileMainToggle = mediaStyleRules("(max-width: 760px)", ".app-nav-divider-toggle").at(-1);

    expect(divider?.style.getPropertyValue("position")).toBe("absolute");
    expect(divider?.style.getPropertyValue("grid-column")).toBe("1 / 2");
    expect(mobileMainToggle?.style.getPropertyValue("display")).toBe("none");
    expect(styles).not.toContain(".nav-collapse-btn");
  });

  it("uses short notification motion with a reduced-motion override", () => {
    expect(ruleBodies(".notif-popover").some((body) => body.includes("notif-popover-in 140ms"))).toBe(true);
    expect(ruleBodies('.notif-popover[data-state="closing"]').some((body) => body.includes("notif-popover-out 100ms"))).toBe(true);
    const reducedMotion = mediaStyleRules(
      "(prefers-reduced-motion: reduce)",
      '.notif-popover, .notif-popover[data-state="closing"]',
    ).at(-1);
    expect(reducedMotion?.style.getPropertyValue("animation")).toBe("none");
  });

  it("shrinks the desktop shell column when navigation is collapsed", () => {
    const designSourceShell = styles.slice(styles.indexOf("/* Design-source home dashboard shell */"));
    expect(designSourceShell).toContain(`@media (min-width: 761px) {
  .app-shell[data-nav-collapsed="true"] {
    grid-template-columns: var(--nav-w-collapsed) minmax(0, 1fr);`);
  });

  it("uses the full desktop workspace without an outer page card", () => {
    const routeCanvases = ruleBodies(".app-main > .route-cache-panel > .dashboard-page:not(.home-dashboard)");
    const notesWorkspaces = ruleBodies(".notes-guide-layout");

    expect(styles).toContain("/* Productive edge-to-edge desktop workspace */");
    expect(routeCanvases.some((body) => body.includes("min-height: calc(100vh - 56px)") && body.includes("padding: 0") && body.includes("max-width: none"))).toBe(true);
    expect(notesWorkspaces.some((body) => body.includes("border: 0") && body.includes("border-radius: 0"))).toBe(true);
  });

  it("keeps the fixed dashboard shell scrollable inside the content pane", () => {
    expect(ruleBodies(".app-content").some((body) => body.includes("height: 100%"))).toBe(true);
    expect(ruleBodies(".app-content").some((body) => body.includes("display: flex") && body.includes("flex-direction: column"))).toBe(true);
    expect(ruleBodies(".app-main").some((body) => body.includes("flex: 1 1 auto") && body.includes("overflow-y: auto"))).toBe(true);
    expect(styles).toContain("grid-template-rows: minmax(0, 1fr)");
  });

  it("keeps settings subnavigation as the grouped reference rail", () => {
    expect(ruleBodies(".settings-layout").some((body) => body.includes("grid-template-columns: 230px minmax(0, 1fr)"))).toBe(true);
    expect(ruleBodies(".settings-subnav").some((body) => body.includes("position: sticky") && body.includes("border-right: 1px solid var(--line)"))).toBe(true);
    expect(styles).toContain(".settings-subnav-group");
    expect(styles).toContain(".settings-tab-icon");
  });

  it("gives non-home dashboard routes their own content padding", () => {
    expect(styles).toContain(".app-main > .dashboard-page:not(.home-dashboard)");
    expect(styles).toContain("--route-page-x-padding: 22px");
    expect(styles).toContain("padding: 26px var(--route-page-x-padding) 40px");
    expect(ruleBodies(".home-dashboard").some((body) => body.includes("padding: 26px var(--route-page-x-padding) 40px"))).toBe(true);
    expect(ruleBodies(".settings-content").some((body) => body.includes("padding: 26px 30px 42px"))).toBe(true);
  });

  it("keeps file server table content contained inside the list pane", () => {
    expect(styles).toContain(".file-guide-table");
    expect(ruleBodies(".file-list-scroll").some((body) => body.includes("overflow: auto"))).toBe(true);
    expect(ruleBodies(".file-guide-table").some((body) => body.includes("min-width: 560px"))).toBe(true);
  });

  it("layers confirmation dialogs above the Kanban card modal", () => {
    const confirmZIndex = Number(ruleBodies(".confirm-scrim").at(0)?.match(/z-index:\s*(\d+)/)?.[1]);
    const kanbanZIndex = Number(ruleBodies(".kanban-card-modal-backdrop").at(0)?.match(/z-index:\s*(\d+)/)?.[1]);

    expect(confirmZIndex).toBeGreaterThan(kanbanZIndex);
  });

  it("keeps Kanban card editing chrome compact", () => {
    expect(ruleBodies(".kanban-card-modal").at(0)).toContain("min-height: min(520px");
    expect(ruleBodies(".kanban-card-modal-header").at(0)).toContain("padding: 12px 14px 10px 16px");
    expect(ruleBodies(".kanban-formatbar").at(0)).toContain("padding: 2px");
    expect(ruleBodies(".kanban-upload").at(0)).toContain("min-height: 44px");
  });

  it("visually distinguishes connection-only attachment controls", () => {
    expect(ruleBodies(".kanban-upload.is-disabled").at(0)).toContain("cursor: not-allowed");
    expect(ruleBodies(".note-attachment-unavailable").at(0)).toContain("color: var(--muted)");
  });

  it("distinguishes column dragging from card drop feedback", () => {
    expect(ruleBodies(".kanban-column-grip").at(0)).toContain("cursor: grab");
    expect(ruleBodies(".kanban-column.is-column-dragging").at(0)).toContain("opacity:");
    expect(ruleBodies(".kanban-column.is-column-drop-target").at(0)).toContain("outline:");
  });

  it("keeps Kanban preview screenshots contained before description editing", () => {
    expect(lastRuleBody(".kanban-description-preview .kanban-rich-image")).toContain("max-height: 320px");
  });

  it("anchors Kanban column tools at the trailing edge of each header", () => {
    const header = topLevelStyleRules(".kanban-column-head").at(-1);
    const actions = topLevelStyleRules(".kanban-column-actions").at(-1);
    const summary = topLevelStyleRules(".kanban-column-summary").at(-1);

    expect(header?.style.getPropertyValue("grid-template-columns")).toBe("minmax(0, 1fr) auto");
    expect(actions?.style.getPropertyValue("grid-column")).toBe("2");
    expect(summary?.style.getPropertyValue("min-width")).toBe("0px");
  });

  it("keeps Kanban body and bold weights consistent between cards and the editor", () => {
    expect(lastRuleBody(".kanban-card-open")).toContain("font: 500");
    expect(lastRuleBody(".kanban-description-preview")).toContain("font: 500");
    expect(lastRuleBody(".kanban-description-editor")).toContain("font: 500");
    expect(lastRuleBody(".kanban-rich-strong")).toContain("font-weight: 700");
    expect(lastRuleBody(".kanban-description-editor strong")).toContain("font-weight: 700");
  });

  it("defines the authoritative safe-area mobile shell", () => {
    expect(styles).toContain("/* Mobile responsive pass */");
    expect(styles).toContain("--mobile-bottom-nav-height: 68px");
    expect(styles).toContain("grid-template-rows: auto minmax(0, 1fr)");
    expect(styles).toContain("env(safe-area-inset-bottom)");
    expect(styles).toContain("min-height: 100dvh");
    expect(styles).toContain("max-height: calc(100dvh - 16px)");
  });

  it("uses existing dashboard tokens for PWA status surfaces", () => {
    expect(ruleBodies(".pwa-status").some((body) => body.includes("color: var(--ink)"))).toBe(true);
    expect(lastRuleBody(".pwa-status.is-offline")).toContain("var(--warn)");
  });

  it("disables pinch zoom only in the installed PWA", () => {
    const rootRule = mediaStyleRules("(display-mode: standalone)", "html, body, #root").at(-1);
    const globalRootRule = topLevelStyleRules("html, body, #root").find((rule) => (
      rule.style.getPropertyValue("touch-action") !== ""
    ));

    expect(rootRule?.style.getPropertyValue("touch-action")).toBe("pan-x pan-y");
    expect(globalRootRule).toBeUndefined();
  });

  it("right-aligns mobile header controls and the compact connection status", () => {
    const actions = mediaStyleRules("(max-width: 760px)", ".app-topbar > .topbar-actions").at(-1);
    const connection = mediaStyleRules("(max-width: 760px)", ".home-mobile-connection").at(-1);

    expect(actions?.style.getPropertyValue("flex")).toBe("0 0 auto");
    expect(actions?.style.getPropertyValue("margin-left")).toBe("auto");
    expect(actions?.style.getPropertyPriority("margin-left")).toBe("important");
    expect(connection?.style.getPropertyValue("width")).toBe("fit-content");
    expect(connection?.style.getPropertyValue("max-width")).toBe("100%");
    expect(connection?.style.getPropertyValue("margin-left")).toBe("auto");
  });

  it("hides agent actions only in the installed mobile PWA", () => {
    const installedActions = mediaStyleRules(
      "(display-mode: standalone) and (max-width: 760px)",
      ".home-hero-actions",
    ).at(-1);
    const browserActions = mediaStyleRules("(max-width: 760px)", ".home-hero-actions");

    expect(installedActions?.style.getPropertyValue("display")).toBe("none");
    expect(browserActions.some((rule) => rule.style.getPropertyValue("display") === "grid")).toBe(true);
  });

  it("renders every Home Assistant switch as an oblong control", () => {
    const desktopSwitch = topLevelStyleRules(".ha-switch").at(-1);
    const desktopThumb = topLevelStyleRules(".ha-switch span").at(-1);
    const mobileSwitch = mediaStyleRules("(max-width: 760px)", ".ha-switch").at(-1);
    const mobileThumb = mediaStyleRules("(max-width: 760px)", ".ha-switch span").at(-1);

    expect(desktopSwitch?.style.getPropertyValue("width")).toBe("56px");
    expect(desktopSwitch?.style.getPropertyValue("min-height")).toBe("32px");
    expect(desktopThumb?.style.getPropertyValue("width")).toBe("24px");
    expect(mobileSwitch?.style.getPropertyValue("width")).toBe("64px");
    expect(mobileSwitch?.style.getPropertyValue("min-height")).toBe("44px");
    expect(mobileThumb?.style.getPropertyValue("width")).toBe("32px");
  });

  it("keeps the mobile note header on one compact readable row", () => {
    const header = mediaStyleRules("(max-width: 760px)", ".notes-editor-header").at(-1);
    const title = mediaStyleRules("(max-width: 760px)", ".notes-title-input").at(-1);

    expect(header?.style.getPropertyValue("grid-template-columns")).toBe("auto minmax(0, 1fr) auto");
    expect(header?.style.getPropertyValue("padding")).toBe("6px 8px");
    expect(title?.style.getPropertyValue("grid-column")).toBe("auto");
    expect(title?.style.getPropertyValue("font-size")).toBe("20px");
  });

  it("stacks installed-mobile Kanban tools below an untruncated column title", () => {
    const condition = "(display-mode: standalone) and (max-width: 760px)";
    const header = mediaStyleRules(condition, ".kanban-column-head").at(-1);
    const actions = mediaStyleRules(condition, ".kanban-column-actions").at(-1);
    const title = mediaStyleRules(condition, ".kanban-column-title h2, .kanban-column h2").at(-1);

    expect(header?.style.getPropertyValue("grid-template-columns")).toBe("minmax(0, 1fr)");
    expect(actions?.style.getPropertyValue("grid-column")).toBe("1");
    expect(actions?.style.getPropertyValue("grid-row")).toBe("2");
    expect(title?.style.getPropertyValue("white-space")).toBe("normal");
    expect(title?.style.getPropertyValue("text-overflow")).toBe("clip");
  });

  it("defines touch-sized Notes and snapping Kanban behavior", () => {
    expect(styles).toContain("scroll-snap-type: x mandatory");
    expect(styles).toContain("grid-auto-columns: min(86vw, 340px)");
    expect(styles).toContain(".notes-toolbar .icon-button");
    expect(styles).toContain(".kanban-card-modal-scroll");
  });

  it("keeps mobile topbar actions visually subordinate to page actions", () => {
    expect(ruleBodies(".mobile-topbar-action").some((body) => body.includes("background: transparent") && body.includes("color: var(--ink)"))).toBe(true);
    expect(styles).toContain('.app-shell[data-mobile-search-open="true"] .mobile-topbar-title');
    expect(styles).toContain("visibility: hidden");
  });

  it("removes the desktop Home Assistant table minimum width on mobile", () => {
    expect(lastRuleBody(".ha-entities-table")).toContain("min-width: 0");
  });

  it("defines task-focused mobile workspaces for the primary routes", () => {
    expect(styles).toContain("/* Task-focused mobile workspaces */");
    expect(styles).toContain('.notes-guide-layout[data-mobile-pane="browser"] .notes-guide-editor');
    expect(styles).toContain('.notes-guide-layout[data-mobile-pane="editor"] .notes-guide-rail');
    expect(styles).toContain(".ha-mobile-results-footer");
    expect(styles).toContain("max-height: 76px");
    expect(styles).toContain(".file-tree-pane");
    expect(styles).not.toContain(".file-activity-region.is-mobile-collapsed");
    expect(styles).toContain(".home-mobile-services-toggle");
    expect(styles).toContain("min-height: calc(100dvh");
  });

  it("keeps mobile file toolbar actions labeled and centered", () => {
    const mobileFileAction = lastRuleBody(".file-guide-actions button");
    expect(mobileFileAction).toContain("font-size: 13px");
    expect(mobileFileAction).toContain("gap: 8px");
    expect(mobileFileAction).toContain("justify-content: center");
  });

  it("keeps every mobile device workspace tab visible without horizontal scrolling", () => {
    const mobileWorkspaceTabs = lastRuleBody(".agent-workspace-tabs");
    const mobileWorkspaceLinks = lastRuleBody(".agent-workspace-tabs a");

    expect(mobileWorkspaceTabs).toContain("grid-template-columns: repeat(2, minmax(0, 1fr))");
    expect(mobileWorkspaceTabs).toContain("overflow-x: visible");
    expect(mobileWorkspaceLinks).toContain("min-height: 44px");
  });

  it("wraps long device identity and Remote Desktop trust values on mobile", () => {
    const mobileAgentValue = lastRuleBody(".agent-info-list dd");
    const mobileTrustCode = lastRuleBody(".agent-security-workspace code");

    expect(mobileAgentValue).toContain("white-space: normal");
    expect(mobileAgentValue).toContain("overflow-wrap: anywhere");
    expect(mobileTrustCode).toContain("white-space: normal");
    expect(mobileTrustCode).toContain("overflow-wrap: anywhere");
  });

  it("preserves mobile-sized touch targets after feature-specific overrides", () => {
    expect(lastRuleBody(".settings-page input:not([type=\"checkbox\"]):not([type=\"radio\"]):not([type=\"file\"])")).toContain("min-height: 44px");
    expect(lastRuleBody(".checkbox-field")).toContain("min-height: 44px");
    expect(lastRuleBody(".file-name-button")).toContain("min-height: 44px");
    expect(lastRuleBody(".ha-entities-table button")).toContain("min-height: 44px");
    expect(lastRuleBody(".home-mobile-services-toggle")).toContain("min-height: 44px");
  });
});
