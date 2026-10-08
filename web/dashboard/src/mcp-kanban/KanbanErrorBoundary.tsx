import { Component, type ErrorInfo, type ReactNode } from "react";

type Props = { children: ReactNode };
type State = { failed: boolean };

export class KanbanErrorBoundary extends Component<Props, State> {
  state: State = { failed: false };

  static getDerivedStateFromError(): State {
    return { failed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("Hank Kanban failed to render", error, info);
  }

  render() {
    if (this.state.failed) {
      return (
        <main className="kanban-app kanban-centered" role="alert">
          Hank Kanban could not open. Close and reopen this board to try again.
        </main>
      );
    }
    return this.props.children;
  }
}
