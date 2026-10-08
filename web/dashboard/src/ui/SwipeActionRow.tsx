import { useEffect, useRef, type ReactNode } from "react";

export function SwipeActionRow({ children, actions }: { children: ReactNode; actions: ReactNode }) {
  const rowRef = useRef<HTMLDivElement>(null);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  function cancelReveal() {
    if (timerRef.current !== null) clearTimeout(timerRef.current);
    timerRef.current = null;
  }
  useEffect(() => cancelReveal, []);
  return (
    <div
      ref={rowRef}
      className="swipe-action-row"
      onMouseEnter={() => {
        if (!window.matchMedia?.("(hover: hover) and (pointer: fine)").matches) return;
        cancelReveal();
        timerRef.current = setTimeout(() => {
          const row = rowRef.current;
          row?.scrollTo({ left: row.scrollWidth, behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
        }, 1000);
      }}
      onMouseLeave={() => {
        cancelReveal();
        if (!window.matchMedia?.("(hover: hover) and (pointer: fine)").matches || rowRef.current?.contains(document.activeElement)) return;
        rowRef.current?.scrollTo({ left: 0, behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
      }}
      onScroll={cancelReveal}
    >
      {children}
      <div className="swipe-row-actions">{actions}</div>
    </div>
  );
}
