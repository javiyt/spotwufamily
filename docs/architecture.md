# Architecture

Spot Wu Family v2 is being rebuilt as a hexagonal Go application.

```mermaid
flowchart LR
  CLI[CLI adapter] --> App[Application use cases]
  App --> Domain[Domain]
  App --> Ports[Outbound ports]
  Ports --> YAML[YAML catalog adapter]
  Ports --> SQLite[SQLite adapter]
  Ports --> Spotify[Spotify HTTP adapter]
  Ports --> Export[JSON export adapter]
```

Current phase:

- Domain contains editorial artist catalog rules.
- Application contains `artists validate` and `artists import-groups`.
- YAML is an outbound adapter.
- CLI composition lives at the inbound edge.

Planned phases add Spotify, SQLite, export, Hugo and automation without making domain packages depend on those technologies.
