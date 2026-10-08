import { useEffect, useRef, useState } from "react";
import { usePWA } from "./PWAProvider";

export function InstallHankAction({ onComplete }: { onComplete?: () => void }) {
  const { install, installMode } = usePWA();
  const [instructionsOpen, setInstructionsOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!instructionsOpen) return;
    function handleEscape(event: KeyboardEvent) {
      if (event.key !== "Escape") return;
      setInstructionsOpen(false);
      triggerRef.current?.focus();
    }
    document.addEventListener("keydown", handleEscape);
    return () => document.removeEventListener("keydown", handleEscape);
  }, [instructionsOpen]);

  if (installMode === "none") return null;

  async function handleInstall() {
    if (installMode === "ios") {
      setInstructionsOpen(true);
      return;
    }
    await install();
    onComplete?.();
  }

  function closeInstructions() {
    setInstructionsOpen(false);
    triggerRef.current?.focus();
  }

  return (
    <>
      <button ref={triggerRef} className="pwa-install-action" type="button" onClick={() => void handleInstall()}>
        Install Hank
      </button>
      {instructionsOpen ? (
        <div className="pwa-install-dialog-scrim" role="presentation" onPointerDown={closeInstructions}>
          <section
            aria-label="Install Hank"
            aria-modal="true"
            className="pwa-install-dialog"
            role="dialog"
            onPointerDown={(event) => event.stopPropagation()}
          >
            <h2>Install Hank</h2>
            <ol>
              <li>Open Hank in Safari.</li>
              <li>Tap the Share button.</li>
              <li>Choose Add to Home Screen.</li>
              <li>Tap Add.</li>
            </ol>
            <button type="button" onClick={closeInstructions}>Close</button>
          </section>
        </div>
      ) : null}
    </>
  );
}
