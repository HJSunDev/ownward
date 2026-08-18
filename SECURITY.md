# Security Policy

## Reporting

Report vulnerabilities privately through [GitHub Security Advisories](https://github.com/HJSunDev/ownward/security/advisories/new). Do not open a public issue before a fix is available.

Include the affected version, impact, minimal reproduction, and proposed mitigation when known. Use synthetic data only. Never submit personal information assets, backups, API keys, model credentials, or local data directories.

## Scope

Security issues include unauthorized disclosure or modification of personal information, backup or restore integrity failures, path traversal, credential exposure, and ways for adapters or derived state to bypass the authoritative core.

Ownward is currently pre-release; security fixes target the latest `main` revision until a versioned support policy is published.

## Data boundary

Information assets remain in the configured local data directory. When a semantic model endpoint is configured, Ownward sends the information being organized, selected related candidates, and search queries to that endpoint; use only a provider whose data handling you accept. The stdio MCP adapter does not expose a network listener.
