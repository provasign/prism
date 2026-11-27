# Cross-repository map and graph (design, 2026-11-27)

`prism map` and `prism graph` show one repository. In a workspace, the edges that matter most for
a shared library are the ones crossing the repository boundary: which of its packages each
consumer uses, and how heavily. This adds them without changing what either command shows inside
the repository.

## The data, already indexed

A repository's link bundle (`linkserver.Build`, the same one `workspace push` uploads) holds two
lists:
- `refs`: every compiler-resolved use of another repository's symbol, with the file it is in and
  the symbol's key (`go:github.com/provasign/grove/pkg/grove#ScanManifests`);
- `defs`: every symbol it declares that others can use, keyed the same way, with its file.

So for the repository you run in (`R`) and each linked repository (`L`):
- **R uses L:** `R.refs` whose key is in `L.defs`. The edge runs from R's component (the directory
  of the referencing file, at the map's depth) to L's component (the directory of the declaring
  file).
- **L uses R:** `L.refs` whose key is in `R.defs`, the other way round.
- **Uploaded repositories use R:** the link server's consumers answer for `R.defs`' keys gives each
  readable uploaded repository's sites. Each edge runs from that repository's component (the
  directory of the site's file) to R's component. The repository is named with its uploaded commit.

Keys are matched exactly, so an edge is as precise as the consumer's compiler pass. Python sites are
import-scoped and labelled as such.

Not drawn, but counted: R's references to packages that no linked or uploaded repository
declares ("N references to K packages in repositories that are not linked").

Outgoing edges to uploaded repositories (R uses a repository only the server knows) are not
drawn. The server answers "who uses X", not "what does X declare" in bulk. The coverage line says
so.

## `prism map`

Nothing above the line changes. Below the existing edges, a new section:

```
other repositories (2 linked; edges between this repository and them, at depth 2):
  uses     internal/grove -> grove:pkg/grove            31  [resolved]
  uses     internal/linkserver -> grove:pkg/grove        2  [resolved]
  used by  mason:cmd/mason -> pkg/kit                    4  [resolved]
  used by  github.com/acme/app@abc12345:internal/api -> internal/core  12  [server, main]
cross-repository coverage: grove, mason read (indexes current); link server: 1 of 1 uploaded repositories, 0 not readable;
  37 references to 9 packages in repositories that are not linked; repositories neither linked nor uploaded are not covered
```

- `--expand 'grove:pkg/grove<-internal/grove'` lists one cross edge's sites; the existing
  `--expand 'from->to'` syntax with repository-qualified names covers both directions.
- `--component X` keeps the cross edges touching X.
- `--no-repos` leaves the section out.
- JSON adds `crossRepo: {repos, edges, coverage}`. Each edge has `from`/`to` (`{repo, component}`,
  with an empty repo meaning this one), `weight`, `evidence` and up to `--max-sites` sites.
- The footer keeps saying "external dependencies excluded" for the in-repository map, and adds
  "linked repositories: see other repositories".
- A linked repository whose index is stale is refreshed first (as change-impact does) or labelled
  stale. A repository that cannot be read is named in the coverage, never silently dropped.

## `prism graph`

- **Dependencies view, at the repository level (packages):** each linked or uploaded repository
  that has edges becomes one node.
  - Its shape is distinct (a rounded rectangle) and its colour is muted.
  - Its label is the repository ID; uploaded repositories also show their short commit.
  - Edges join it to this repository's package nodes, weighted like other links.
  - The legend gains "other repository".
- **Selecting a repository node:** the panel lists the edges in both directions with their sites
  (file, line, text), the source (checkout, clone or server), commit and pin. A button copies the
  site list. There is no drill-down: the explorer holds one repository's index. The panel says to
  run `prism graph` in that repository to explore it.
- **Selecting a link to a repository node:** the panel shows that edge's evidence like any other
  link.
- **Toggle:** an "Other repos" switch next to Packages and Tests, on by default when a workspace
  or server is configured. With many repositories, only the 20 heaviest are drawn, plus a
  "N more repositories" node that lists the rest.
- **Inside a package or file:** repository nodes are not drawn there. Change impact already lists
  other repositories' sites.

## Honesty rules

The rules are the same as change-impact's:
- every cross edge names its evidence;
- the coverage line names what was not read;
- a stale or unreadable repository is labelled, never dropped;
- the server's answers carry their commit.

## Cost

Each `map` or graph load reads every linked repository's bundle (its index): about the cost of
`workspace export` per repository, under a second for Grove-sized repositories. One server
request covers all uploaded repositories. Results are cached per index modification time.
