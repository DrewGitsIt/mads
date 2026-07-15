// A minimal scripted-playback stand-in for the browser EventSource, shared
// by router/events.test.ts and any feature test that needs to simulate the
// Router's SSE stream without a real network connection.
export class FakeEventSource {
  static instances: FakeEventSource[] = [];

  url: string;
  closed = false;
  private listeners = new Map<string, ((e: MessageEvent) => void)[]>();

  constructor(url: string | URL) {
    this.url = url.toString();
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, handler: EventListener): void {
    const handlers = this.listeners.get(type) ?? [];
    handlers.push(handler as (e: MessageEvent) => void);
    this.listeners.set(type, handlers);
  }

  close(): void {
    this.closed = true;
  }

  /** Dispatches one scripted `event: <type>\ndata: <data>` frame. */
  emit(type: string, data: string): void {
    for (const handler of this.listeners.get(type) ?? []) {
      handler({ data } as MessageEvent<string>);
    }
  }
}
