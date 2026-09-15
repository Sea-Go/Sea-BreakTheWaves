#!/usr/bin/env bash
set -euo pipefail

# Reuse the same isolated PG16/DC/RTW lifecycle as the v1 process test, with
# a fixed v2/v1/v2 two-user source fixture and a dedicated native Graph test.
export FAVORITE_ACCEPTANCE_FIXTURE_TEST=TestFavoriteDeliveryTwoUsersSharedAuthorityFixture
export FAVORITE_ACCEPTANCE_BTW_TEST=TestFactWorkerFavoriteAuthorityV2MixedUsers
export FAVORITE_TWO_SUBJECTREF_MODE=v2-mixed
exec bash "$(dirname "$0")/favorite_acceptance.sh"
