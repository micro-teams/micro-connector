# @micro-teams/connector-applets

The screen engine and the program declarations that micro-connector serves to a machine: one
self-contained JavaScript file per program, built for the goja runtime the connector embeds.

A control plane installs this package and serves `dist/claude.js` (or `dist/codex.js`) to a
connector, which runs it against a real terminal. Nothing here talks to a network or a filesystem —
the host provides a terminal, mirrored variables and two directions of function call, and that is
the whole of it.

## Why it is a package

Understanding what a coding agent's terminal is showing you is the expensive part of driving one,
and it goes stale every time that agent ships a release. Publishing it means a fix is made once and
taken by everyone, instead of each product discovering the same changed dialog on its own.

## Use

```
npm install @micro-teams/connector-applets
```

The package is published to GitHub Packages, so a consumer's `.npmrc` needs:

```
@micro-teams:registry=https://npm.pkg.github.com
```

Then serve the file:

```js
const source = fs.readFileSync(
  require.resolve('@micro-teams/connector-applets/dist/claude.js'), 'utf8')
// hand `source` to the connector as the screen's applet
```

Versions published from `main` carry a `-main.<n>` prerelease suffix and the `dev` dist-tag, so
`npm install` still resolves to the last released version unless a consumer asks for `@dev`.

## Develop

```
npm run typecheck && npm test    # the tests run the built artifact, in a fresh scope, as goja does
npm run build
```

MIT.
