import React from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { PWAProvider } from "./pwa/PWAProvider";
import { PWAStatus } from "./pwa/PWAStatus";
import { OfflineNotesProvider } from "./offlineNotes/OfflineNotesProvider";
import { ErrorBoundary } from "./ui/primitives";
import "./styles.css";

const rootElement = document.getElementById("root");

if (!rootElement) {
  throw new Error("Hank dashboard root element was not found.");
}

createRoot(rootElement).render(
  <PWAProvider>
    <OfflineNotesProvider>
      <PWAStatus />
      <React.StrictMode>
        <ErrorBoundary>
          <App />
        </ErrorBoundary>
      </React.StrictMode>
    </OfflineNotesProvider>
  </PWAProvider>,
);
