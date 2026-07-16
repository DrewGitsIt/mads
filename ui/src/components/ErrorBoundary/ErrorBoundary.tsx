import { Component, type ReactNode } from "react";
import { Button } from "../Button/Button";
import { Card } from "../Card/Card";
import styles from "./ErrorBoundary.module.css";

export interface ErrorBoundaryProps {
  children: ReactNode;
}

interface ErrorBoundaryState {
  error: Error | null;
}

export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { error: null };

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  render() {
    if (this.state.error) {
      return (
        <div className={styles.wrap}>
          <Card className={styles.card}>
            <div className={styles.title}>Something went wrong</div>
            <div className={styles.message}>{this.state.error.message}</div>
            <Button variant="primary" onClick={() => window.location.reload()}>
              Reload
            </Button>
          </Card>
        </div>
      );
    }
    return this.props.children;
  }
}
