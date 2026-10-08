# Hank screenshot provenance

Captured from the implemented production dashboard build on October 5, 2026,
using the real Go server and an isolated PostgreSQL 18/pgvector container.

- `notes.png`: synthetic **Welcome to Hank** note, 1440 × 1024.
- `dashboard.png`: real Home dashboard for the synthetic **Demo Home**, 1920 × 1080.
- `kanban.png`: synthetic **Launch checklist** board, 1920 × 1080.

The fixture account is `alex@hank.example`. All notes and cards are synthetic;
the primary agent is currently offline. No Home Assistant instance, SMB share,
managed machine, production account, or model provider was connected. API
responses and rendered content were not replaced for the capture.

To reproduce, install the locked dashboard dependencies and run
`bash docs/demo/run.sh`. Sign in using its temporary credentials, open the
corresponding pages, and take screenshots at the sizes above. The demo uses
the actual registration, Home, and Notes APIs through `docs/demo/seed.py`.

Only these PNGs and this provenance document belong in source control.
Browser profiles, session state, generated credentials, database state, and
temporary capture/test output must remain outside the repository.
