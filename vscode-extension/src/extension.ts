import * as vscode from "vscode";
import { PrismClient } from "./prismClient";
import { registerTools } from "./tools";

let client: PrismClient | undefined;
let output: vscode.OutputChannel | undefined;
let groveStatus: vscode.StatusBarItem | undefined;
let savingsStatus: vscode.StatusBarItem | undefined;

function logLine(message: string): void {
  if (!output) return;
  const ts = new Date().toISOString();
  output.appendLine(`[${ts}] ${message}`);
}

function formatSavingsText(value: unknown): string {
  if (typeof value === "number" && Number.isFinite(value)) {
    return `Prism ${(Math.round(value * 10) / 10).toFixed(1)}%`;
  }
  return "Prism --";
}

function formatGroveText(symbolCount: unknown): string {
  if (typeof symbolCount === "number" && Number.isFinite(symbolCount)) {
    return `$(database) Grove ${symbolCount.toLocaleString()} syms`;
  }
  return "$(database) Grove --";
}

async function refreshGroveStatus(): Promise<void> {
  if (!client || !groveStatus) {
    return;
  }
  try {
    const result = await client.status() as { symbolCount?: number };
    groveStatus.text = formatGroveText(result?.symbolCount);
    groveStatus.tooltip = `Grove knowledge graph: ${result?.symbolCount ?? "unknown"} symbols indexed. Click for details.`;
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    logLine(`Grove status refresh failed: ${msg}`);
    groveStatus.text = "$(database) Grove --";
    groveStatus.tooltip = "Grove symbol count unavailable.";
  }
}

async function refreshSavingsStatus(): Promise<void> {
  if (!client || !savingsStatus) {
    return;
  }
  try {
    const result = await client.savings() as { savingsPercent?: number };
    savingsStatus.text = `$(graph) ${formatSavingsText(result?.savingsPercent)}`;
    savingsStatus.tooltip = "Prism session savings percent. Click to view details.";
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    logLine(`Status refresh failed: ${msg}`);
    savingsStatus.text = "$(graph) Prism --";
    savingsStatus.tooltip = "Prism savings unavailable.";
  }
}

async function runSetup(clientRef: PrismClient, root: string, silent: boolean): Promise<boolean> {
  try {
    logLine(`Setup started for workspace: ${root}`);
    const result = await clientRef.invoke("init", []);
    logLine(`Setup succeeded: ${JSON.stringify(result)}`);
    if (!silent) {
      vscode.window.showInformationMessage("Prism setup complete for this workspace.");
    }
    return true;
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    logLine(`Setup failed: ${msg}`);
    if (!silent) {
      vscode.window.showErrorMessage(`Prism setup failed: ${msg}`);
    }
    return false;
  }
}

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  output = vscode.window.createOutputChannel("Prism");
  context.subscriptions.push(output);
  logLine("Prism extension activating.");

  groveStatus = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 101);
  groveStatus.name = "Grove Symbols";
  groveStatus.command = "prism.index";
  groveStatus.text = "$(database) Grove --";
  groveStatus.tooltip = "Grove symbol count. Click to re-index.";
  groveStatus.show();
  context.subscriptions.push(groveStatus);

  savingsStatus = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 100);
  savingsStatus.name = "Prism Savings";
  savingsStatus.command = "prism.savings";
  savingsStatus.text = "$(graph) Prism --";
  savingsStatus.tooltip = "Prism session savings percent. Click to view details.";
  savingsStatus.show();
  context.subscriptions.push(savingsStatus);

  const folders = vscode.workspace.workspaceFolders;
  if (!folders || folders.length === 0) {
    logLine("No workspace folder is open. Prism extension is idle.");
    return;
  }
  const root = folders[0].uri.fsPath;
  client = new PrismClient(root);
  logLine(`Workspace root: ${root}`);

  registerTools(context, client);
  logLine("Prism language model tools registered.");

  context.subscriptions.push(
    vscode.commands.registerCommand("prism.setup", async () => {
      if (!client) {
        vscode.window.showErrorMessage("Prism setup failed: no workspace is open.");
        return;
      }
      await runSetup(client, root, false);
    }),
    vscode.commands.registerCommand("prism.index", async () => {
      logLine("Command prism.index started.");
      try {
        const result = await client!.index();
        logLine(`Command prism.index succeeded: ${JSON.stringify(result)}`);
        await refreshSavingsStatus();
        vscode.window.showInformationMessage(
          `Prism indexed: ${JSON.stringify(result)}`,
        );
      } catch (e) {
        logLine(`Command prism.index failed: ${(e as Error).message}`);
        vscode.window.showErrorMessage(`Prism index failed: ${(e as Error).message}`);
      }
    }),
    vscode.commands.registerCommand("prism.query", async () => {
      const task = await vscode.window.showInputBox({
        prompt: "Describe the task — Prism will return ranked, compressed context.",
      });
      if (!task) return;
      logLine(`Command prism.query started for task: ${task}`);
      try {
        const result = await client!.query(task, client!.profile());
        logLine("Command prism.query succeeded.");
        await refreshSavingsStatus();
        const doc = await vscode.workspace.openTextDocument({
          language: "json",
          content: JSON.stringify(result, null, 2),
        });
        await vscode.window.showTextDocument(doc, { preview: true });
      } catch (e) {
        logLine(`Command prism.query failed: ${(e as Error).message}`);
        vscode.window.showErrorMessage(`Prism query failed: ${(e as Error).message}`);
      }
    }),
    vscode.commands.registerCommand("prism.savings", async () => {
      logLine("Command prism.savings started.");
      try {
        const result = await client!.savings();
        logLine(`Command prism.savings succeeded: ${JSON.stringify(result)}`);
        await refreshSavingsStatus();
        vscode.window.showInformationMessage(
          `Prism savings: ${JSON.stringify(result)}`,
        );
      } catch (e) {
        logLine(`Command prism.savings failed: ${(e as Error).message}`);
        vscode.window.showErrorMessage(`Prism savings failed: ${(e as Error).message}`);
      }
    }),
    vscode.commands.registerCommand("prism.newSession", async () => {
      // A new session in the Go server happens per-process; the CLI is
      // stateless across invocations, so the only meaningful action here
      // is to surface a hint.
      logLine("Command prism.newSession invoked.");
      vscode.window.showInformationMessage(
        "Prism: CLI invocations are stateless; restart `prism serve` for a fresh session ledger.",
      );
    }),
  );

  // Auto-index on save (debounced — fire-and-forget).
  context.subscriptions.push(
    vscode.workspace.onDidSaveTextDocument(() => {
      if (!vscode.workspace.getConfiguration("prism").get<boolean>("autoIndex", true)) {
        return;
      }
      logLine("Auto-index triggered by file save.");
      client!.index().then((result) => {
        logLine(`Auto-index succeeded: ${JSON.stringify(result)}`);
        void refreshSavingsStatus();
        void refreshGroveStatus();
      }).catch((err: unknown) => {
        const msg = err instanceof Error ? err.message : String(err);
        logLine(`Auto-index failed: ${msg}`);
      });
    }),
  );

  // Initial index - best-effort.
  logLine("Initial index started.");
  client.index().then((result) => {
    logLine(`Initial index succeeded: ${JSON.stringify(result)}`);
    void refreshSavingsStatus();
    void refreshGroveStatus();
  }).catch((err: unknown) => {
    const msg = err instanceof Error ? err.message : String(err);
    logLine(`Initial index failed: ${msg}`);
  });

  void refreshSavingsStatus();
  void refreshGroveStatus();
  const refreshTimer = setInterval(() => {
    void refreshSavingsStatus();
    void refreshGroveStatus();
  }, 15000);
  context.subscriptions.push({ dispose: () => clearInterval(refreshTimer) });
}

export function deactivate(): void {
  logLine("Prism extension deactivated.");
  client = undefined;
  groveStatus?.dispose();
  groveStatus = undefined;
  savingsStatus?.dispose();
  savingsStatus = undefined;
  output?.dispose();
  output = undefined;
}
