# Plugin documentation implementation plan

## Goal

Publish complete, validated English and Russian documentation for all 18
catalog plugins and render it in `ohtools-web` from a pinned immutable bundle.

## Delivery order

1. Add the documentation parser/validator contract through red-green TDD and
   prove it with `server-setup-base`.
2. Document `system-base`, `storage-base`, `systemd-base`, and `docker-base`.
3. Document `network-base`, `security-base`, `apt-base`, and `baseline-base`.
4. Document `postgres-base`, `k8s-base`, `java-base`, `kafka-base`, and
   `gitlab-runner-base`.
5. Document `monitoring-base`, `backup-base`, `incident-base`, and
   `runbook-base`.
6. Generate and validate `plugin-docs-v1.json`; add its protected release
   workflow.
7. Add the portal fetch/verification seam through TDD, render the content in
   both locales, and cover search, copy, mobile, keyboard, and accessibility.
8. Publish the immutable documentation bundle, pin its URL and SHA-256 in the
   portal, merge both PRs, and verify GitHub Actions and Pages.

Each wave uses focused failing tests before implementation, then targeted tests,
the full repository gates, diff review, and a small commit. Parallel agents may
author non-overlapping plugin document sets after the contract and pilot are
green.

## Required checks

The plugin repository must pass formatting, vet, race tests, all static plugin
builds, manifest validation, documentation validation, and deterministic bundle
generation.

The portal must pass formatting, TypeScript/content checks, unit tests, catalog
and documentation synchronization, production build, route validation,
Playwright, and axe checks. The generated site must contain all 18 English and
Russian plugin routes and make no client-side upstream requests.

