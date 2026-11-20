# @micro-teams/connector-applets

The screen engine and the program declarations that micro-connector serves to a machine: one
self-contained JavaScript file per program, built for the goja runtime the connector embeds.

A control plane serves `dist/<program>.js` to a connector, which runs it against a real terminal.
Nothing here talks to a network or a filesystem: the host provides a terminal, mirrored variables
and two directions of function call (see `src/engine/host.ts`).

## Drivers

| file | program | commands besides `snapshot` and `say(text, id?)` |
|---|---|---|
| `claude.js` | Claude Code | `choose(n)`, `compact`, `usage`, `login` |
| `codex.js` | Codex | `usage` |
| `pi.js` | pi | |
| `dsh.js` | an ACP agent behind `acp-screen` | `interrupt` |

Every driver mirrors `status` (`starting`, `busy`, `waiting`, `idle`, `dead`), `elapsed`, `tokens`,
`question` and `choices`. `say` queues the text; it is pasted once the program shows its prompt and
submitted two frames later, and when it was given an id the driver calls the control plane's
`sayResult({id, ok, error})`. Build the drivers from the same release as the connector that runs
them.

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
npm run typecheck && npm test    # the tests run the built files, each in a fresh scope, as goja does
npm run build
```

MIT.
