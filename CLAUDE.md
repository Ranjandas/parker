# Parker: notes for contributors and coding agents

## Keep the user guide in sync

The user guide in `docs/guide/` is what users read on the website
(https://ranjandas.github.io/parker/docs/). It must match the product.

- **Every change that adds or changes a feature updates the guide in the
  same PR**: a new command, flag, lab file key, scenario step, selector,
  TUI key, web UI feature, default or behaviour. Update the page that
  covers it, with usage and an example, and the
  [command reference](docs/guide/cli.md) for commands and flags.
- Removing or renaming something removes or renames it in the guide too.
- Write for users, not for maintainers: what it does, how to use it, an
  example, and any limits. Internals belong in code comments and the
  README's "How it works".
- Examples should be real: prefer output captured from a running lab,
  and complete lab files that `parker validate` accepts.
- New pages go in `docs/guide/nav.yaml`, with `title:` and
  `description:` front matter. Links between pages use `page.md` or
  `page.md#heading`.

Tests enforce part of this, and fail CI when the guide falls behind:

- `cmd/parker/docs_test.go`: every visible command has a `### parker ...`
  section in `cli.md` that mentions each of its flags, and every complete
  lab file in the guide loads and expands.
- `hack/sitegen/main_test.go`: every lab file key is explained in a guide
  page, every page is in the nav, and every link between pages lands on
  a page and heading that exist.

The lab file reference page is generated from
`internal/spec/parker.schema.json`, so keep the schema's field comments
(in `internal/spec`) user-friendly and run `go generate ./internal/spec`
after changing them.

## The website

`site/` is the static homepage; `hack/sitegen` renders the guide into
`docs/` of the built site in the same look. Preview it with:

```console
$ go run ./hack/sitegen -out _site && python3 -m http.server -d _site 8000
```

Keep the site's existing look and feel (colours, type, components in
`site/assets/style.css` and `docs.css`) unless there's a reason to change
it.
