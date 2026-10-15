# Validation gates. Included by the root Makefile. DEADFIN_DIR is the
# repository root since the Jellyport fold (D16); the variable stays so the
# recipes read the same as the plan documents that cite them.
DEADFIN_DIR := .
CONVENTION_ROOT ?= $(DEADFIN_DIR)

define assert_fixture_rejected
	@output="$$(go run tools/convention-gates/main.go -root "$(1)" -file "$(2)" 2>&1)"; status=$$?; \
	if [ $$status -eq 0 ]; then echo "$(4) did not fail convention gate"; exit 1; fi; \
	if ! printf '%s\n' "$$output" | grep -F "$(3)" >/dev/null; then echo "$(4) did not emit expected diagnostic: $(3)"; printf '%s\n' "$$output"; exit 1; fi
endef

define assert_fixture_accepted
	@output="$$(go run tools/convention-gates/main.go -root "$(1)" -file "$(2)" 2>&1)"; status=$$?; \
	if [ $$status -ne 0 ]; then echo "$(3) unexpectedly failed convention gate"; printf '%s\n' "$$output"; exit 1; fi
endef

.PHONY: lint verify-p0-01 verify-p0-06 verify-p2-01 verify-p5 verify-p7 verify-p8 verify-p8-itemlookup-diff verify-convention-gates verify-convention-gate-fixtures \
	reference-env reference-env-fixtures reference-env-seed reference-env-replay-smoke reference-env-down \
	difftool difftool-check-normalizers \
	ledger-routes ledger ledger-check verify-ledger

lint:
	golangci-lint fmt --diff
	golangci-lint run

verify-p0-01: verify-convention-gates verify-convention-gate-fixtures build test lint

verify-p0-06:
	@python3 -m unittest tools.goldenrec.tests.test_recorders

verify-p2-01:
	@dotnet restore tools/compat/itemnormoracle/ItemNormOracle.csproj
	@dotnet build tools/compat/itemnormoracle/ItemNormOracle.csproj --no-restore
	@python3 -m unittest tools.goldenrec.tests.test_itemnormoracle
	@cd $(DEADFIN_DIR) && go test ./internal/item/...

verify-p5:
	@cd $(DEADFIN_DIR) && go test ./internal/item ./internal/playstate ./internal/session ./internal/api/handlers ./internal/dtobuild ./internal/store/storetest ./cmd/deadfin
	@cd tools/difftool && go test . -run '^(TestStreamsCorpusDefinesStaticDirectStreamHTTPContract|TestStreamsCorpusReplaysHostLocalItemIDsAcrossNativeAndCamelCase|TestPlaystateCorpusDefinesExactStatefulSwiftfinContract|TestNormalizerItemUserDataPlaybackTimestampsOnlyNormalizesValidItemUserDataDates)$$' -count=1

verify-p7:
	@cd $(DEADFIN_DIR) && go test ./internal/events ./internal/activitylog ./internal/tasks ./internal/session ./internal/sessioncontrol ./internal/ws ./internal/playstate
	@cd $(DEADFIN_DIR) && go test ./internal/api/handlers -run '^(TestSessionControlRoutesAuthorizeAndEmitJellyfinCommands|TestSessionsListAuthorizesVisibilityAndControllableTargets)$$'
	@cd $(DEADFIN_DIR) && go test ./cmd/deadfin -run '^(TestApplicationComposesPersistentStartupAndAuthorization|TestApplicationSessionsProjectsActiveAndTerminalPlayback)$$'
	@cd $(DEADFIN_DIR) && go test -race ./internal/events ./internal/activitylog ./internal/tasks ./internal/session ./internal/sessioncontrol ./internal/ws ./internal/playstate
	@cd $(DEADFIN_DIR) && go test -race ./internal/api/handlers -run '^(TestSessionControlRoutesAuthorizeAndEmitJellyfinCommands|TestSessionsListAuthorizesVisibilityAndControllableTargets)$$'
	@cd $(DEADFIN_DIR) && go test -race ./cmd/deadfin -run '^(TestApplicationComposesPersistentStartupAndAuthorization|TestApplicationSessionsProjectsActiveAndTerminalPlayback)$$'

verify-p8:
	@cd $(DEADFIN_DIR) && go test ./internal/providers/musicbrainz ./internal/providers/audiodb ./internal/providers/listenbrainz -count=1
	@cd $(DEADFIN_DIR) && go test ./internal/probe -run '^(TestNormalizeMatchesCompleteSourceMediaInfoGoldens|TestNormalizeMinesPinnedAudioTagsInStockOrder|TestNormalizeMinesPinnedITunesTagsWithStockOverwriteSemantics|TestNormalizeMinesWTVTagsOnlyFromFormat|TestNormalizePreservesNullableStringsAndConstructedCollectionsOnWire)$$' -count=1
	@cd $(DEADFIN_DIR) && go test ./cmd/deadfin -count=1
	@cd $(DEADFIN_DIR) && go test -race ./internal/providers/musicbrainz ./internal/providers/audiodb ./internal/providers/listenbrainz -count=1
	@cd $(DEADFIN_DIR) && go test -race ./internal/probe -run '^(TestNormalizeMatchesCompleteSourceMediaInfoGoldens|TestNormalizeMinesPinnedAudioTagsInStockOrder|TestNormalizeMinesPinnedITunesTagsWithStockOverwriteSemantics|TestNormalizeMinesWTVTagsOnlyFromFormat|TestNormalizePreservesNullableStringsAndConstructedCollectionsOnWire)$$' -count=1
	@cd $(DEADFIN_DIR) && go test -race ./cmd/deadfin -count=1
	@cd $(DEADFIN_DIR) && go build ./...
	@cd $(DEADFIN_DIR) && go vet ./...
	@files="$$(find $(DEADFIN_DIR)/internal/providers/musicbrainz $(DEADFIN_DIR)/internal/providers/audiodb $(DEADFIN_DIR)/internal/providers/listenbrainz -type f -name '*.go' -print) $(DEADFIN_DIR)/cmd/deadfin/application.go $(DEADFIN_DIR)/cmd/deadfin/scanner_probe.go $(DEADFIN_DIR)/cmd/deadfin/scanner_probe_composition_test.go $(DEADFIN_DIR)/cmd/deadfin/scanner_provider_composition_test.go $(DEADFIN_DIR)/internal/library/scanner_probe.go $(DEADFIN_DIR)/internal/probe/media_info.go $(DEADFIN_DIR)/internal/probe/normalize.go $(DEADFIN_DIR)/internal/probe/normalize_test.go $(DEADFIN_DIR)/tools/music-providers-smoke/main.go"; \
	unformatted="$$(gofmt -l $$files)"; \
	if [ -n "$$unformatted" ]; then printf 'gofmt required:\n%s\n' "$$unformatted"; exit 1; fi
	@set -e; \
	for file in $$(find $(DEADFIN_DIR)/internal/providers/musicbrainz $(DEADFIN_DIR)/internal/providers/audiodb $(DEADFIN_DIR)/internal/providers/listenbrainz -type f -name '*.go' -print) \
		$(DEADFIN_DIR)/cmd/deadfin/application.go $(DEADFIN_DIR)/cmd/deadfin/scanner_probe.go \
		$(DEADFIN_DIR)/cmd/deadfin/scanner_probe_composition_test.go $(DEADFIN_DIR)/cmd/deadfin/scanner_provider_composition_test.go \
		$(DEADFIN_DIR)/internal/library/scanner_probe.go $(DEADFIN_DIR)/internal/probe/media_info.go \
		$(DEADFIN_DIR)/internal/probe/normalize.go $(DEADFIN_DIR)/internal/probe/normalize_test.go \
		$(DEADFIN_DIR)/tools/music-providers-smoke/main.go; do \
		go run tools/convention-gates/main.go -root "$(DEADFIN_DIR)" -file "$${file#$(DEADFIN_DIR)/}"; \
	done
	@find $(DEADFIN_DIR)/internal/providers/musicbrainz/testdata $(DEADFIN_DIR)/internal/providers/audiodb/testdata \
		$(DEADFIN_DIR)/internal/providers/listenbrainz/testdata -type f -name '*.json' -print0 | xargs -0 -r -n1 jq -e . >/dev/null
	@cd $(DEADFIN_DIR) && go test ./internal/identify ./internal/providers ./internal/providers/tmdb ./internal/dto -count=1
	@cd $(DEADFIN_DIR) && go test ./internal/api/handlers -run '^(TestItemLookup|TestRemoteSearch|TestRemoteImages|TestDownloadProvenance|TestDownloadItemNotFound)' -count=1
	@cd $(DEADFIN_DIR) && go test ./internal/imageserve -run '^(TestRemoteCache|TestSelect|TestTag|TestRender)' -count=1
	@cd $(DEADFIN_DIR) && go test -race ./internal/identify ./internal/providers ./internal/providers/tmdb ./internal/dto -count=1
	@cd $(DEADFIN_DIR) && go test -race ./internal/api/handlers -run '^(TestItemLookup|TestRemoteSearch|TestRemoteImages|TestDownloadProvenance|TestDownloadItemNotFound)' -count=1
	@cd $(DEADFIN_DIR) && go test -race ./internal/imageserve -run '^(TestRemoteCache|TestSelect|TestTag|TestRender)' -count=1
	@cd tools/difftool && go test . -run '^TestItemLookupCorpus' -count=1
	@python3 -m unittest tools.goldenrec.tests.test_provider_replay

verify-p8-itemlookup-diff:
	@bash tools/goldenrec/itemlookup-diff.sh

verify-convention-gates:
	@go run tools/convention-gates/main.go -root "$(CONVENTION_ROOT)"

verify-convention-gate-fixtures:
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/sql,internal/api/violation.go,raw SQL is only permitted under internal/store,SQL SELECT fixture)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/sql,internal/api/lowercase.go,raw SQL is only permitted under internal/store,SQL lowercase fixture)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/sql,internal/api/pragma.go,raw SQL is only permitted under internal/store,SQL PRAGMA fixture)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/sql,cmd/sqlcheck/violation.go,raw SQL is only permitted under internal/store,SQL command fixture)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/sql,internal/api/replace.go,raw SQL is only permitted under internal/store,SQL REPLACE fixture)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/sql,internal/api/leading_comment.go,raw SQL is only permitted under internal/store,SQL leading-comment fixture)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/sql,internal/api/returning.go,raw SQL is only permitted under internal/store,SQL DELETE RETURNING fixture)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/sql,internal/api/qualified_ids.go,raw SQL is only permitted under internal/store,SQL schema-qualified identifier fixture)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/sql,internal/api/temp_forms.go,raw SQL is only permitted under internal/store,SQL TEMP/TEMPORARY fixture)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/sql,internal/api/database_sql.go,database/sql is only permitted under internal/store,SQL database method fixture)
	$(call assert_fixture_accepted,.ci/fixtures/convention-gates/positive,internal/api/handlers/ordinary.go,ordinary handler fixture)
	$(call assert_fixture_accepted,.ci/fixtures/convention-gates/prose,internal/api/messages.go,SQL-prose fixture)
	$(call assert_fixture_accepted,.ci/fixtures/convention-gates/prose,internal/imageserve/diagnostics_test.go,testing-diagnostic prose fixture)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/sql,internal/imageserve/assigned_variable_test.go,raw SQL is only permitted under internal/store,SQL assigned-variable negative control)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/sql,internal/imageserve/query_call_test.go,raw SQL is only permitted under internal/store,SQL db.Query negative control)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/sql,internal/imageserve/concatenation_test.go,raw SQL is only permitted under internal/store,SQL concatenation negative control)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/sql,internal/imageserve/non_testing_call_test.go,raw SQL is only permitted under internal/store,SQL non-testing-call negative control)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/sql,internal/imageserve/same_name_receiver_test.go,raw SQL is only permitted under internal/store,SQL same-name-receiver scope boundary)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/sql,internal/imageserve/local_shadow_test.go,raw SQL is only permitted under internal/store,SQL local-shadow negative control)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/sql,internal/imageserve/aliased_testing_test.go,raw SQL is only permitted under internal/store,SQL aliased-testing negative control)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/query,internal/api/handlers/violation.go,handlers must use api/bind rather than URL.Query,query fixture)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/query,internal/api/handlers/method_value.go,handlers must use api/bind rather than URL.Query,query method-value fixture)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/query,internal/api/handlers/req_receiver.go,handlers must use api/bind rather than URL.Query,query receiver fixture)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/time,internal/service/violation.go,time.Now is only permitted in internal/clock,time fixture)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/time,internal/service/aliased.go,time.Now is only permitted in internal/clock,time alias fixture)
# REUSE-IgnoreStart
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/spdx,internal/api/no_header.go,missing the // SPDX-License-Identifier: GPL-2.0-only header,SPDX missing-header fixture)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/spdx,internal/api/late_header.go,missing the // SPDX-License-Identifier: GPL-2.0-only header,SPDX late-header fixture)
# REUSE-IgnoreEnd
	$(call assert_fixture_accepted,.ci/fixtures/convention-gates/spdx,cmd/tool/build_tag.go,SPDX build-constraint fixture)
	$(call assert_fixture_accepted,.ci/fixtures/convention-gates/spdx,internal/api/generated.go,SPDX generated-file fixture)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/time,internal/service/method_value.go,time.Now is only permitted in internal/clock,time method-value fixture)
	$(call assert_fixture_rejected,.ci/fixtures/convention-gates/time,cmd/clockcheck/violation.go,time.Now is only permitted in internal/clock,time command fixture)

reference-env:
	@tools/goldenrec/reference-env.sh

reference-env-fixtures:
	@tools/verify-env/generate-fixtures.sh

reference-env-seed:
	@tools/goldenrec/seed.sh

reference-env-replay-smoke:
	@tools/goldenrec/replay.sh corpora/captured/stock-public-info.smoke.yaml

reference-env-down:
	@docker compose -f tools/goldenrec/compose.yaml down

difftool:
	@cd tools/difftool && go run -mod=vendor . --corpus ../../corpora/captured --left "$${DIFFTOOL_LEFT:?set DIFFTOOL_LEFT}" --right "$${DIFFTOOL_RIGHT:?set DIFFTOOL_RIGHT}" --rules normalize --results ../../results --issues ../../.diffs

difftool-check-normalizers:
	@cd tools/difftool && go run -mod=vendor . --corpus ../../corpora/captured --left "$${JELLYFIN_BASE_URL:-http://127.0.0.1:8096}" --check-normalizers --rules normalize --results ../../results --issues ../../.diffs

# A tool can carry its own targets in tools/<tool>/make.mk.
-include $(wildcard tools/*/make.mk)

# ledger-routes refreshes the external route snapshot from a Deadfin checkout.
# It requires DEADFIN_DIR (a clean Deadfin checkout) and DEADFIN_REF (the
# 40-character Deadfin revision). It runs go run ./cmd/deadfin routes --json
# from that checkout and writes ledger/deadfin-routes.json.
ledger-routes:
	@DEADFIN_DIR="$${DEADFIN_DIR:?DEADFIN_DIR is required (path to a clean Deadfin checkout)}"; \
	DEADFIN_REF="$${DEADFIN_REF:?DEADFIN_REF is required (40-character Deadfin revision)}"; \
	cd tools/ledger && go run . routes

# ledger rewrites the generated files (ledger/parity.json,
# ledger/websocket.json, ledger/PARITY.md) from committed inputs. It
# reads the route snapshot, OpenAPI spec, difftool results, work-package
# status lines, shadow-spec.yaml, and annotations.yaml.
ledger:
	@cd tools/ledger && go run . generate

# ledger-check compares generated bytes with committed files. It exits
# nonzero if any generated file is stale.
ledger-check:
	@cd tools/ledger && go run . check

# verify-ledger executes focused ledger tests and ledger-check.
verify-ledger:
	@cd tools/ledger && go test ./... -count=1
	@cd tools/ledger && go run . check
