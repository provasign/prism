import * as vscode from "vscode";
import { PrismClient } from "./prismClient";

type ToolResult = vscode.LanguageModelToolResult;

function asText(value: unknown): ToolResult {
  const text = typeof value === "string" ? value : JSON.stringify(value, null, 2);
  return new vscode.LanguageModelToolResult([new vscode.LanguageModelTextPart(text)]);
}

interface QueryInput { task: string; profile?: string; limit?: number; }
interface ReadInput { file: string; }
interface SearchInput { query: string; limit?: number; }
interface LookupInput { name: string; }
interface FeedbackInput { queryId: string; rating: number; notes?: string; }
interface CompactInput { turns: unknown[]; }

export function registerTools(
  context: vscode.ExtensionContext,
  client: PrismClient,
): void {
  context.subscriptions.push(
    vscode.lm.registerTool<QueryInput>("prism_query", {
      invoke: async (opts) => {
        const { task, profile, limit } = opts.input;
        const out = await client.query(task, profile ?? client.profile(), limit);
        return asText(out);
      },
    }),
    vscode.lm.registerTool<ReadInput>("prism_read", {
      invoke: async (opts) => asText(await client.read(opts.input.file)),
    }),
    vscode.lm.registerTool<SearchInput>("prism_search", {
      invoke: async (opts) => asText(await client.search(opts.input.query, opts.input.limit)),
    }),
    vscode.lm.registerTool<LookupInput>("prism_lookup", {
      invoke: async (opts) => asText(await client.lookup(opts.input.name)),
    }),
    vscode.lm.registerTool<{}>("prism_index", {
      invoke: async () => asText(await client.index()),
    }),
    vscode.lm.registerTool<{}>("prism_savings", {
      invoke: async () => asText(await client.savings()),
    }),
    vscode.lm.registerTool<CompactInput>("prism_compact", {
      invoke: async (opts) => asText(await client.compact(Array.isArray(opts.input.turns) ? opts.input.turns : [])),
    }),
    vscode.lm.registerTool<FeedbackInput>("prism_feedback", {
      invoke: async (opts) => {
        const { queryId, rating, notes } = opts.input;
        if (typeof rating !== "number" || rating < 0 || rating > 5) {
          return asText({ error: "rating must be 0-5" });
        }
        return asText(await client.feedback(queryId, rating, notes));
      },
    }),
  );
}
