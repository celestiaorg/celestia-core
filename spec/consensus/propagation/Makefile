# Model checking of the compact-block propagation <-> consensus hand-off.
# See README.md. Every target is non-interactive and exits non-zero on
# failure. Requires node/npm (for quint), java 21+ and curl.

QUINT_VERSION      := 0.34.0
APALACHE_VERSION   := 0.62.4
EVALUATOR_VERSION  := v0.8.0
EVALUATOR_ASSET    := quint_evaluator-x86_64-unknown-linux-gnu.tar.gz
EVALUATOR_SHA256   := 774020b648a887f511d040fd564a8223c62a996b80f8dc50ea24d8630235be51
QUINT_HOME         ?= $(HOME)/.quint
EVALUATOR_DIR      := $(QUINT_HOME)/rust-evaluator-$(EVALUATOR_VERSION)

QUINT    ?= quint
MODEL    := propagation.qnt
INST     := instances.qnt
TESTS    := propagation_test.qnt
TLC_CFG  := tlc.json
TLC      := $(QUINT) verify $(INST) --backend tlc --tlc-config $(TLC_CFG) --apalache-version $(APALACHE_VERSION)
TRACES   := traces
SAMPLES  ?= 20000
STEPS    ?= 60

.PHONY: all deps typecheck test simulate verify verify-cap2 gate liveness liveness-full traces clean

all: typecheck test simulate verify gate liveness

# Checks the pinned quint and installs the pinned Rust evaluator that
# `quint run` / `quint test` use. quint fetches it through the GitHub API,
# which some CI networks refuse; this downloads the release asset directly
# and checks its hash. Apalache (whose jar also carries TLC) is downloaded by
# the first `quint verify`.
deps:
	@v=$$($(QUINT) --version); if [ "$$v" != "$(QUINT_VERSION)" ]; then \
	  echo "need quint $(QUINT_VERSION), found '$$v' (npm i -g @informalsystems/quint@$(QUINT_VERSION))"; exit 1; fi
	@if [ ! -x "$(EVALUATOR_DIR)/quint_evaluator" ]; then \
	  set -e; tmp=$$(mktemp -d); \
	  curl -fsSL -o "$$tmp/ev.tgz" "https://github.com/quint-co/quint/releases/download/evaluator%2F$(EVALUATOR_VERSION)/$(EVALUATOR_ASSET)"; \
	  echo "$(EVALUATOR_SHA256)  $$tmp/ev.tgz" | sha256sum -c -; \
	  mkdir -p "$(EVALUATOR_DIR)"; tar -xzf "$$tmp/ev.tgz" -C "$(EVALUATOR_DIR)"; chmod +x "$(EVALUATOR_DIR)/quint_evaluator"; \
	  rm -rf "$$tmp"; fi
	@java -version 2>&1 | head -1

typecheck: deps
	$(QUINT) typecheck $(MODEL)
	$(QUINT) typecheck $(INST)
	$(QUINT) typecheck $(TESTS)

test: deps
	$(QUINT) test --main prefixTest $(TESTS)
	$(QUINT) test --main forwardOnceOnlyTest $(TESTS)
	$(QUINT) test --main asyncOnlyTest $(TESTS)
	$(QUINT) test --main fixedTest $(TESTS)

# random simulation of the fixed model (Rust evaluator); bounded, not a proof.
simulate: deps
	$(QUINT) run $(INST) --main fixedTraced --max-samples $(SAMPLES) --max-steps $(STEPS) \
	  --invariants $(SAFETY)

SAFETY := S1_noWaitCycle S1_noQueueCycle S2_forwardOnce S3_decision sanity

# S1, S2, S3 (forward decision) on the fixed model, exhaustive (TLC), on the
# two configurations the check is split into (README, "Reductions").
verify: deps
	$(TLC) --main fixedIdentity --invariants $(SAFETY)
	$(TLC) --main fixedParts --invariants $(SAFETY)

# the same with channel capacity 2 (slower; not part of `all`).
verify-cap2: deps
	$(TLC) --main fixedIdentityCap2 --invariants $(SAFETY)
	$(TLC) --main fixedPartsCap2 --invariants $(SAFETY)

# The pilot's gate: with ab6a3df reverted, TLC must find the wait-for cycle
# (S1) and a duplicate forward (S2). Fails if either property holds.
gate: deps
	@./expect_violation.sh S1_noWaitCycle $(TLC) --main prefix --invariants S1_noWaitCycle
	@./expect_violation.sh S1_noQueueCycle $(TLC) --main prefix --invariants S1_noQueueCycle
	@./expect_violation.sh S2_forwardOnce $(TLC) --main prefix --invariants S2_forwardOnce
	@./expect_violation.sh S1_noWaitCycle $(TLC) --main forwardOnceOnly --invariants S1_noWaitCycle
	@./expect_violation.sh S2_forwardOnce $(TLC) --main asyncOnly --invariants S2_forwardOnce

# L1 under the fairness assumptions in propagation.qnt (TLC, exhaustive), on
# one round; plus the vacuity check (without fairness L1 must fail).
liveness: deps
	$(TLC) --main liveOneRound --step liveStep --temporal L1_eventualCompletion
	@./expect_violation.sh L1_withoutFairness $(TLC) --main liveOneRound --step liveStep --temporal L1_withoutFairness

# L1 on rounds 0 and 1 (about 45 min on 4 cores; not part of `all`).
liveness-full: deps
	$(TLC) --main live --step liveStep --temporal L1_eventualCompletion

# representative ITF traces: one per scenario test, plus random runs of the
# fixed model.
traces: deps
	rm -rf $(TRACES) && mkdir -p $(TRACES)
	$(QUINT) test --main prefixTest $(TESTS) --out-itf '$(TRACES)/prefix_{test}.itf.json'
	$(QUINT) test --main forwardOnceOnlyTest $(TESTS) --out-itf '$(TRACES)/forwardOnceOnly_{test}.itf.json'
	$(QUINT) test --main asyncOnlyTest $(TESTS) --out-itf '$(TRACES)/asyncOnly_{test}.itf.json'
	$(QUINT) test --main fixedTest $(TESTS) --out-itf '$(TRACES)/fixed_{test}.itf.json'
	$(QUINT) run $(INST) --main fixedTraced --seed 0x1 --max-samples 3 --max-steps 40 --n-traces 3 \
	  --invariants $(SAFETY) --out-itf '$(TRACES)/fixed_random_{seq}.itf.json'

clean:
	rm -rf _apalache-out
