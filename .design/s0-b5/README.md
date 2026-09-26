# B5 S0 empirical spike

All programs in this directory are disposable evidence probes. They do not
belong to the AgentSQL production build.

Run from the repository root with the environment in the task description:

```powershell
go run ./.design/s0-b5/list-images.go
go run ./.design/s0-b5/mcp-cancel.go
```

Container probes use names and labels prefixed with `agentsql-b5-s0-` and
remove their own containers/networks/volumes. `cleanup.go` is a final
label/name-scoped cleanup and audit; it intentionally does not prune Docker or
touch unrelated/demo resources.

