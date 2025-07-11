# Contributing

Thank you for your interest in contributing to CometBFT! Before contributing, it
may be helpful to understand the goal of the project. The goal of CometBFT is to
develop a BFT consensus engine robust enough to support permissionless
value-carrying networks. While all contributions are welcome, contributors
should bear this goal in mind in deciding if they should target the main
CometBFT project or a potential fork. When targeting the main CometBFT project,
the following process leads to the best chance of landing changes in `main`.

All work on the code base should be motivated by a [GitHub
Issue](https://github.com/cometbft/cometbft/issues).
[Search](https://github.com/cometbft/cometbft/issues?q=is%3Aopen+is%3Aissue+label%3A%22good+first+issue%22)
is a good place to start when looking for places to contribute. If you would
like to work on an issue which already exists, please indicate so by leaving a
comment.

All new contributions should start with a [GitHub
Issue](https://github.com/cometbft/cometbft/issues/new/choose). The issue helps
capture the problem you're trying to solve and allows for early feedback. Once
the issue is created the process can proceed in different directions depending
on how well defined the problem and potential solution are. If the change is
simple and well understood, maintainers will indicate their support with a
heartfelt emoji.

If the issue would benefit from thorough discussion, maintainers may request
that you create a [Request For
Comment](https://github.com/cometbft/cometbft/tree/main/docs/rfc) in the
CometBFT repo. Discussion at the RFC stage will build collective
understanding of the dimensions of the problems and help structure conversations
around trade-offs.

When the problem is well understood but the solution leads to large structural
changes to the code base, these changes should be proposed in the form of an
[Architectural Decision Record (ADR)](./docs/architecture/). The ADR will help
build consensus on an overall strategy to ensure the code base maintains
coherence in the larger context. If you are not comfortable with writing an ADR,
you can open a less-formal issue and the maintainers will help you turn it into
an ADR.

> How to pick a number for the ADR?

Find the largest existing ADR number and bump it by 1.

When the problem as well as proposed solution are well understood,
changes should start with a [draft
pull request](https://github.blog/2019-02-14-introducing-draft-pull-requests/)
against `main`. The draft signals that work is underway. When the work
is ready for feedback, hitting "Ready for Review" will signal to the
maintainers to take a look.

![Contributing flow](./docs/imgs/contributing.png)

Each stage of the process is aimed at creating feedback cycles which align contributors and maintainers to make sure:

- Contributors don’t waste their time implementing/proposing features which won’t land in `main`.
- Maintainers have the necessary context in order to support and review contributions.


## Forking

Please note that Go requires code to live under absolute paths, which complicates forking.
While my fork lives at `https://github.com/ebuchman/cometbft`,
the code should never exist at `$GOPATH/src/github.com/ebuchman/cometbft`.
Instead, we use `git remote` to add the fork as a new remote for the original repo,
`$GOPATH/src/github.com/cometbft/cometbft`, and do all the work there.

For instance, to create a fork and work on a branch of it, I would:

- Create the fork on GitHub, using the fork button.
- Go to the original repo checked out locally (i.e. `$GOPATH/src/github.com/cometbft/cometbft`)
- `git remote rename origin upstream`
- `git remote add origin git@github.com:ebuchman/basecoin.git`

Now `origin` refers to my fork and `upstream` refers to the CometBFT version.
So I can `git push -u origin main` to update my fork, and make pull requests to CometBFT from there.
Of course, replace `ebuchman` with your git handle.

To pull in updates from the origin repo, run

- `git fetch upstream`
- `git rebase upstream/main` (or whatever branch you want)

## Dependencies

We use [go modules](https://github.com/golang/go/wiki/Modules) to manage dependencies.

That said, the `main` branch of every CometBFT repository should just build
with `go get`, which means they should be kept up-to-date with their
dependencies so we can get away with telling people they can just `go get` our
software.

Since some dependencies are not under our control, a third party may break our
build, in which case we can fall back on `go mod tidy`. Even for dependencies under our control, go helps us to
keep multiple repos in sync as they evolve. Anything with an executable, such
as apps, tools, and the core, should use dep.

Run `go list -u -m all` to get a list of dependencies that may not be
up-to-date.

When updating dependencies, please only update the particular dependencies you
need. Instead of running `go get -u=patch`, which will update anything,
specify exactly the dependency you want to update.

## Protobuf

We use [Protocol Buffers](https://developers.google.com/protocol-buffers) along
with [`gogoproto`](https://github.com/cosmos/gogoproto) to generate code for use
across CometBFT.

To generate proto stubs, lint, and check protos for breaking changes, you will
need to install [buf](https://buf.build/) and `gogoproto`. Then, from the root
of the repository, run:

```bash
# Lint all of the .proto files
make proto-lint

# Check if any of your local changes (prior to committing to the Git repository)
# are breaking
make proto-check-breaking

# Generate Go code from the .proto files
make proto-gen
```

To automatically format `.proto` files, you will need
[`clang-format`](https://clang.llvm.org/docs/ClangFormat.html) installed. Once
installed, you can run:

```bash
make proto-format
```

### Visual Studio Code

If you are a VS Code user, you may want to add the following to your `.vscode/settings.json`:

```json
{
  "protoc": {
    "options": [
      "--proto_path=${workspaceRoot}/proto",
    ]
  }
}
```

## Changelog

To manage and generate our changelog, we currently use [unclog](https://github.com/informalsystems/unclog).

Every fix, improvement, feature, or breaking change should be made in a
pull-request that includes a file
`.changelog/unreleased/${category}/${issue-or-pr-number}-${description}.md`,
where:
- `category` is one of `improvements`, `breaking-changes`, `bug-fixes`,
  `features` and if multiple apply, create multiple files;
- `description` is a short (4 to 6 word), hyphen separated description of the
  fix, starting the component changed; and,
- `issue or PR number` is the CometBFT issue number, if one exists, or the PR
  number, otherwise.

For examples, see the [.changelog](.changelog) folder.

A feature can also be worked on a feature branch, if its size and/or risk
justifies it (see [below](#branching-model-and-release)).

### What does a good changelog entry look like?

Changelog entries should answer the question: "what is important about this
change for users to know?" or "what problem does this solve for users?". It
should not simply be a reiteration of the title of the associated PR, unless the
title of the PR _very_ clearly explains the benefit of a change to a user.

Some good examples of changelog entry descriptions:

```md
- [consensus] \#1111 Small transaction throughput improvement (approximately
  3-5\% from preliminary tests) through refactoring the way we use channels
- [mempool] \#1112 Refactor Go API to be able to easily swap out the current
  mempool implementation in CometBFT forks
- [p2p] \#1113 Automatically ban peers when their messages are unsolicited or
  are received too frequently
```

Some bad examples of changelog entry descriptions:

```md
- [consensus] \#1111 Refactor channel usage
- [mempool] \#1112 Make API generic
- [p2p] \#1113 Ban for PEX message abuse
```

For more on how to write good changelog entries, see:

- <https://keepachangelog.com>
- <https://docs.gitlab.com/ee/development/changelog.html#writing-good-changelog-entries>
- <https://depfu.com/blog/what-makes-a-good-changelog>

### Changelog entry format

Changelog entries should be formatted as follows:

```md
- [module] \#xxx Some description of the change (@contributor)
```

Here, `module` is the part of the code that changed (typically a top-level Go
package), `xxx` is the pull-request number, and `contributor` is the author/s of
the change.

It's also acceptable for `xxx` to refer to the relevant issue number, but
pull-request numbers are preferred. Note this means pull-requests should be
opened first so the changelog can then be updated with the pull-request's
number. There is no need to include the full link, as this will be added
automatically during release. But please include the backslash and pound, eg.
`\#2313`.

Changelog entries should be ordered alphabetically according to the `module`,
and numerically according to the pull-request number.

Changes with multiple classifications should be doubly included (eg. a bug fix
that is also a breaking change should be recorded under both).

Breaking changes are further subdivided according to the APIs/users they impact.
Any change that affects multiple APIs/users should be recorded multiply - for
instance, a change to the `Blockchain Protocol` that removes a field from the
header should also be recorded under `CLI/RPC/Config` since the field will be
removed from the header in RPC responses as well.

## Branching Model and Release

### Celestia-specific Contribution Flow  

Celestia-core follows a specific contribution workflow due to its dual-branch maintenance strategy:

- **v0.38.x-celestia branch**: Used in celestia-app v4 (current production)
- **main branch**: Will be used in celestia-app v5 (future releases)

#### Pull Request Guidelines

**For changes intended for celestia-app v4:**
- Unless the change is temporary and v4-specific, **always merge to `main` first**
- After merging to `main`, backport to `v0.38.x-celestia` using mergify
- Add the `backport-to-v0.38.x-celestia` label to trigger automatic backporting
- If automatic backporting fails, manually resolve conflicts and create a backport PR

**For changes intended only for celestia-app v5:**
- Target the `main` branch directly
- No backporting needed

**For v4-specific changes only:**
- Target `v0.38.x-celestia` directly (rare case)
- These should be temporary fixes or configurations specific to v4

#### Backporting Process

We use [Mergify](https://mergify.io) to automate backporting:

**From main to release branches:**
1. Add the `backport-to-v0.38.x-celestia` label to your PR targeting `main`
2. Once the PR is merged, Mergify will automatically create a backport PR
3. If backporting fails due to conflicts, Mergify will create a draft PR for you to resolve conflicts
4. The original PR author is responsible for resolving backport conflicts

**From release branches to main:**
1. Add the `S:backport-to-main` label to your PR targeting `v0.38.x-celestia`
2. Once the PR is merged, Mergify will automatically create a backport PR to `main`
3. If backporting fails due to conflicts, Mergify will create a draft PR for you to resolve conflicts
4. The original PR author is responsible for resolving backport conflicts

### General Release Process

The main development branch is `main`.

Every release is maintained in a release branch named `vX.Y.Z`.

Pending minor releases have long-lived release candidate ("RC") branches. Minor
release changes should be merged to these long-lived RC branches at the same
time that the changes are merged to `main`.

If a feature's size is big and/or its risk is high, it can be implemented in a
feature branch. While the feature work is in progress, pull requests are open
and squash merged against the feature branch. Branch `main` is periodically
merged (merge commit) into the feature branch, to reduce branch divergence. When
the feature is complete, the feature branch is merged back (merge commit) into
`main`. The moment of the final merge can be carefully chosen so as to land
different features in different releases.

Note, all pull requests should be squash merged except for merging to a release
branch (named `vX.Y`). This keeps the commit history clean and makes it easy to
reference the pull request where a change was introduced.

### Development Procedure

The latest state of development is on `main`, which must never fail `make test`.
_Never_ force push `main`, unless fixing broken git history (which we rarely do
anyways).

To begin contributing, create a development branch either on
`github.com/cometbft/cometbft`, or your fork (using `git remote add origin`).

Make changes, and before submitting a pull request, update the changelog to
record your change. Also, run either `git rebase` or `git merge` on top of the
latest `main`. (Since pull requests are squash-merged, either is fine!)

Update the `UPGRADING.md` if the change you've made is breaking and the
instructions should be in place for a user on how he/she can upgrade its
software (ABCI application, CometBFT blockchain, light client, wallet).

Sometimes (often!) pull requests get out-of-date with `main`, as other people
merge different pull requests to `main`. It is our convention that pull request
authors are responsible for updating their branches with `main`. (This also
means that you shouldn't update someone else's branch for them; even if it seems
like you're doing them a favor, you may be interfering with their git flow in
some way!)

#### Merging Pull Requests

It is also our convention that authors merge their own pull requests, when
possible. External contributors may not have the necessary permissions to do
this, in which case, a member of the core team will merge the pull request once
it's been approved.

Before merging a pull request:

- Ensure pull branch is up-to-date with a recent `main` (GitHub won't let you
  merge without this!)
- Run `make test` to ensure that all tests pass
- [Squash](https://stackoverflow.com/questions/5189560/squash-my-last-x-commits-together-using-git)
  merge pull request

#### Pull Requests for Minor Releases

If your change should be included in a minor release, please also open a PR
against the long-lived minor release candidate branch (e.g., `rc1/v0.33.5`)
_immediately after your change has been merged to main_.

You can do this by cherry-picking your commit off `main`:

```sh
$ git checkout rc1/v0.33.5
$ git checkout -b {new branch name}
$ git cherry-pick {commit SHA from main}
# may need to fix conflicts, and then use git add and git cherry-pick --continue
$ git push origin {new branch name}
```

After this, you can open a PR. Please note in the PR body if there were merge
conflicts so that reviewers can be sure to take a thorough look.

### Git Commit Style

We follow the [Go style guide on commit
messages](https://tip.golang.org/doc/contribute.html#commit_messages). Write
concise commits that start with the package name and have a description that
finishes the sentence "This change modifies CometBFT to...". For example,

```sh
cmd/debug: execute p.Signal only when p is not nil

[potentially longer description in the body]

Fixes #nnnn
```

Each PR should have one commit once it lands on `main`; this can be accomplished
by using the "squash and merge" button on GitHub. Be sure to edit your commit
message, though!

## Testing

### Unit tests

Unit tests are located in `_test.go` files as directed by [the Go testing
package](https://golang.org/pkg/testing/). If you're adding or removing a
function, please check there's a `TestType_Method` test for it.

Run: `make test`

### Integration tests

Integration tests are also located in `_test.go` files. What differentiates
them is a more complicated setup, which usually involves setting up two or more
components.

Run: `make test_integrations`

### End-to-end tests

End-to-end tests are used to verify a fully integrated CometBFT network.

See [README](./test/e2e/README.md) for details.

Run:

```sh
cd test/e2e && \
  make && \
  ./build/runner -f networks/ci.toml
```

### Fuzz tests (ADVANCED)

*NOTE: if you're just submitting your first PR, you won't need to touch these
most probably (99.9%)*.

[Fuzz tests](https://en.wikipedia.org/wiki/Fuzzing) can be found inside the
`./test/fuzz` directory. See [README.md](./test/fuzz/README.md) for details.

Run: `cd test/fuzz && make fuzz-{PACKAGE-COMPONENT}`

### RPC Testing

**If you contribute to the RPC endpoints it's important to document your
changes in the [Openapi file](./rpc/openapi/openapi.yaml)**.

To test your changes you must install `nodejs` and run:

```bash
npm i -g dredd
make build-linux build-contract-tests-hooks
make contract-tests
```

**WARNING: these are currently broken due to <https://github.com/apiaryio/dredd>
not supporting complete OpenAPI 3**.

This command will popup a network and check every endpoint against what has
been documented.
