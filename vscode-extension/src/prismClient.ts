import { spawn } from "child_process";
import * as path from "path";
import * as vscode from "vscode";

/**
 * PrismClient invokes the prism CLI as a child process for each tool call.
 * No HTTP server needed; each invocation is a short-lived process that
 * itself talks to Grove on :7777.
 *
 *   prism <tool> --json '<payload>' [workspace]
 *
 * The Go CLI is responsible for auto-starting Grove if needed.
 */
export class PrismClient {
  constructor(private readonly workspaceRoot: string) {}

  private cfg(): vscode.WorkspaceConfiguration {
    return vscode.workspace.getConfiguration("prism");
  }

  binary(): string {
    return this.cfg().get<string>("binaryPath", "prism");
  }

  profile(): string {
    return this.cfg().get<string>("profile", "default");
  }

  /** Invoke a CLI subcommand and parse stdout as JSON. */
  async invoke(subcommand: string, args: string[] = []): Promise<unknown> {
    return new Promise((resolve, reject) => {
      const bin = this.binary();
      const fullArgs = [subcommand, ...args, this.workspaceRoot];
      const child = spawn(bin, fullArgs, { cwd: this.workspaceRoot });
      let stdout = "";
      let stderr = "";
      child.stdout.on("data", (b: Buffer) => (stdout += b.toString()));
      child.stderr.on("data", (b: Buffer) => (stderr += b.toString()));
      child.on("error", (e) =>
        reject(new Error(`failed to spawn ${bin}: ${e.message}`)),
      );
      child.on("close", (code) => {
        if (code !== 0) {
          reject(new Error(`prism ${subcommand} exited ${code}: ${stderr}`));
          return;
        }
        const trimmed = stdout.trim();
        if (!trimmed) {
          resolve({});
          return;
        }
        try {
          resolve(JSON.parse(trimmed));
        } catch {
          resolve({ raw: stdout });
        }
      });
    });
  }

  // Typed wrappers used by tool implementations.

  index(): Promise<unknown> {
    return this.invoke("index");
  }

  status(): Promise<unknown> {
    return this.invoke("status");
  }

  query(task: string, profile?: string, limit?: number): Promise<unknown> {
    const args: string[] = [task];
    // The CLI accepts --profile and --limit before the optional dir arg.
    // Position-aware: insert flags after the task.
    if (profile) args.push("--profile", profile);
    if (typeof limit === "number") args.push("--limit", String(limit));
    return this.invoke("query", args);
  }

  read(file: string): Promise<unknown> {
    return this.invoke("read", [file]);
  }

  search(query: string, limit?: number): Promise<unknown> {
    const args = [query];
    if (typeof limit === "number") args.push("--limit", String(limit));
    return this.invoke("search", args);
  }

  lookup(name: string): Promise<unknown> {
    return this.invoke("lookup", [name]);
  }

  savings(): Promise<unknown> {
    return this.invoke("savings");
  }

  absolutePath(file: string): string {
    return path.isAbsolute(file) ? file : path.join(this.workspaceRoot, file);
  }
}
