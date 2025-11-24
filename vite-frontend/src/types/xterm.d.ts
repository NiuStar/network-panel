declare module "xterm" {
  export interface ITerminalOptions {
    convertEol?: boolean;
    cursorBlink?: boolean;
    disableStdin?: boolean;
    fontSize?: number;
    theme?: Record<string, string>;
    scrollback?: number;
  }
  export interface IDisposable {
    dispose(): void;
  }
  export class Terminal {
    constructor(options?: ITerminalOptions);
    open(container: HTMLElement): void;
    write(data: string): void;
    reset(): void;
    focus(): void;
    scrollToBottom(): void;
    onData(cb: (data: string) => void): IDisposable;
  }
}
