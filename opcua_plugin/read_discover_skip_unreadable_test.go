// Copyright 2026 UMH Systems GmbH
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package opcua_plugin

import (
	"os"

	"github.com/gopcua/opcua/ua"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Regression test for the "single unreadable node kills the whole input"
// problem. The MonitorBatched result-handling loop used to close the OPC UA
// client and return an error on ANY BAD-severity per-result status code,
// which caused benthos to flap into a reconnect loop whenever a server
// exposed a write-only / permission-restricted tag (e.g. a Siemens DB
// MaintenanceRetain block where bCollOperatorMaintenanceTimeExpired returns
// StatusBadNotReadable).
//
// The fix classifies per-result BAD codes into:
//   - "skippable" (per-node permanent, e.g. BadNotReadable): the node is
//     dropped from the subscription and the rest of the batch continues
//     to be monitored.
//   - everything else: existing close-and-return behaviour is preserved so
//     genuine session/transport errors still trigger reconnect.
//
// This file pins down the policy decision via isSkippableSubscriptionError
// so future code changes cannot silently regress the user-visible behaviour.
var _ = Describe("MonitorBatched per-node error classification", func() {
	BeforeEach(func() {
		if os.Getenv("TEST_OPCUA_UNIT") == "" {
			Skip("Skipping OPC UA unit tests: TEST_OPCUA_UNIT not set")
		}
	})

	DescribeTable("isSkippableSubscriptionError",
		func(code ua.StatusCode, expectedSkippable bool, reason string) {
			Expect(isSkippableSubscriptionError(code)).To(Equal(expectedSkippable), reason)
		},

		// Per-node permanent errors - MUST be skippable so a single bad tag
		// doesn't take down the whole subscription.
		Entry("BadNotReadable - the exact code reported in the user bug report",
			ua.StatusBadNotReadable, true,
			"AccessLevel lacks CurrentRead; reconnecting will not change that"),
		Entry("BadUserAccessDenied - server-side ACL forbids reading",
			ua.StatusBadUserAccessDenied, true,
			"User does not have permission; permanent per-node failure"),
		Entry("BadNodeIDUnknown - node was deleted or never existed",
			ua.StatusBadNodeIDUnknown, true,
			"NodeID does not exist in address space; reconnect will not help"),
		Entry("BadNodeIDInvalid - syntactically invalid node id",
			ua.StatusBadNodeIDInvalid, true,
			"Bad NodeID syntax cannot be fixed at runtime"),
		Entry("BadAttributeIDInvalid - node does not expose AttributeIDValue",
			ua.StatusBadAttributeIDInvalid, true,
			"Attribute not supported for this Node"),
		Entry("BadTypeMismatch - per-node data type incompatibility",
			ua.StatusBadTypeMismatch, true,
			"Per-node data-model mismatch"),
		Entry("BadIndexRangeInvalid - bad IndexRange parameter",
			ua.StatusBadIndexRangeInvalid, true,
			"Per-node range error"),
		Entry("BadIndexRangeNoData - no data in requested range",
			ua.StatusBadIndexRangeNoData, true,
			"Per-node range error"),
		Entry("BadMonitoredItemFilterInvalid - per-node filter shape",
			ua.StatusBadMonitoredItemFilterInvalid, true,
			"Filter shape is wrong for this specific node"),
		Entry("BadMonitoredItemFilterUnsupported - per-node filter support",
			ua.StatusBadMonitoredItemFilterUnsupported, true,
			"Server does not support the filter for this node"),

		// NOT skippable - these still need the existing close-and-reconnect
		// path because the session/transport itself is in a bad state.
		Entry("BadFilterNotAllowed - handled by trial-and-retry, not by skip",
			ua.StatusBadFilterNotAllowed, false,
			"This is a connection-wide capability question handled separately"),
		Entry("BadSessionClosed - session-level, reconnect is the right action",
			ua.StatusBadSessionClosed, false,
			"Session-level error; client must reconnect"),
		Entry("BadServerNotConnected - transport-level",
			ua.StatusBadServerNotConnected, false,
			"Transport-level error; client must reconnect"),
		Entry("BadInternalError - opaque server failure",
			ua.StatusBadInternalError, false,
			"Unclear scope; default to close-and-reconnect"),
		Entry("BadTooManyMonitoredItems - server-wide resource limit",
			ua.StatusBadTooManyMonitoredItems, false,
			"Server resource exhaustion; not per-node permanent"),

		// Good and Uncertain codes must NOT be classified as skip-worthy
		// because they are not even BAD; statusIsBad guards them out before
		// isSkippableSubscriptionError is consulted, but pin it down anyway.
		Entry("StatusOK is not skippable", ua.StatusOK, false,
			"GOOD severity must never be treated as a failure"),
	)
})
