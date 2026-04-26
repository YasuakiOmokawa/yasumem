import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));

function pluginRoot(): string {
  return join(here, "..", "..", "..");
}

export function getDBPath(): string {
  const env = process.env.YASUMEM_DB;
  if (env && env !== "") return env;
  return join(pluginRoot(), "data", "memory.db");
}

export function getCurrentProject(): string {
  try {
    const p = join(pluginRoot(), "data", "current_project");
    return readFileSync(p, "utf8").trim();
  } catch {
    return "";
  }
}

export function lastIngestAtPath(): string {
  return join(pluginRoot(), "data", "last_ingest_at");
}

export function resolveCanonicalProject(cwd: string): string {
  try {
    const out = execFileSync("git", ["-C", cwd, "worktree", "list", "--porcelain"], {
      encoding: "utf8",
    });
    for (const line of out.split("\n")) {
      if (line.startsWith("worktree ")) {
        return line.slice("worktree ".length);
      }
    }
  } catch {
    // ignore
  }
  return cwd;
}

export function getWorktreePaths(cwd: string): string[] {
  try {
    const out = execFileSync("git", ["-C", cwd, "worktree", "list", "--porcelain"], {
      encoding: "utf8",
    });
    const paths: string[] = [];
    for (const line of out.split("\n")) {
      if (line.startsWith("worktree ")) {
        paths.push(line.slice("worktree ".length));
      }
    }
    return paths.length > 0 ? paths : [cwd];
  } catch {
    return [cwd];
  }
}
