import { runIngest, runIngestRecent } from "./ingest.js";
import { runServer } from "./server.js";

async function main(): Promise<void> {
  const cmd = process.argv[2];
  if (!cmd) {
    process.stderr.write("usage: yasumem <server|ingest|ingest-recent>\n");
    process.exit(1);
  }
  switch (cmd) {
    case "server":
      await runServer();
      break;
    case "ingest":
      await runIngest();
      break;
    case "ingest-recent":
      await runIngestRecent();
      break;
    default:
      process.stderr.write(`unknown command: ${cmd}\n`);
      process.exit(1);
  }
}

main().catch((e) => {
  process.stderr.write(`Error: ${(e as Error).message}\n`);
  process.exit(1);
});
