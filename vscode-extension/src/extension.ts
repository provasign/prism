import * as vscode from "vscode";
import { PrismClient } from "./prismClient";
import { registerTools } from "./tools";

let client: PrismClient | undefined;

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  const folders = vscode.workspace.workspaceFolders;
  if (!folders || folders.length === 0) {
    return;
  }
  const root = folders[0].uri.fsPath;
  client = new PrismClient(root);

  registerTools(context, client);

  context.subscriptions.push(
    vscode.commands.registerCommand("prism.index", async () => {
      try {
        const result = await client!.index();
        vscode.window.showInformationMessage(
          `Prism indexed: ${JSON.stringify(result)}`,
        );
      } catch (e) {
        vscode.window.showErrorMessage(`Prism index failed: ${(e as Error).message}`);
      }
    }),
    vscode.commands.registerCommand("prism.query", async () => {
      const task = await vscode.window.showInputBox({
        prompt: "Describe the task — Prism will return ranked, compressed context.",
      });
      if (!task) return;
      try {
        const result = await client!.query(task, client!.profile());
        const doc = await vscode.workspace.openTextDocument({
          language: "json",
          content: JSON.stringify(result, null, 2),
        });
        await vscode.window.showTextDocument(doc, { preview: true });
      } catch (e) {
        vscode.window.showErrorMessage(`Prism query failed: ${(e as Error).message}`);
      }
    }),
    vscode.commands.registerCommand("prism.savings", async () => {
      try {
        const result = await client!.savings();
        vscode.window.showInformationMessage(
          `Prism savings: ${JSON.stringify(result)}`,
        );
      } catch (e) {
        vscode.window.showErrorMessage(`Prism savings failed: ${(e as Error).message}`);
      }
    }),
    vscode.commands.registerCommand("prism.newSession", async () => {
      // A new session in the Go server happens per-process; the CLI is
      // stateless across invocations, so the only meaningful action here
      // is to surface a hint.
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
      client!.index().catch(() => { /* swallow */ });
    }),
  );

  // Initial index — best-effort.
  client.index().catch(() => { /* Grove may not yet be reachable; user can retry */ });
}

export function deactivate(): void {
  client = undefined;
}
