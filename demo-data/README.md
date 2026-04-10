# pagefault demo data

This is the sample content served by `configs/minimal.yaml`.

pagefault is a personal memory service that exposes files, search indices,
and agent sessions to AI clients over MCP and REST.

## Try it

Start the server:

```
make run
```

Then in another terminal:

```
curl -s -X POST localhost:8444/api/search -d '{"query":"pagefault"}' | jq
```

## What's in this directory

- `README.md`  — this file
- `notes.md`   — a second sample file
