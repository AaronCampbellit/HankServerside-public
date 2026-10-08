package cloud

import "testing"

// These names are receipt-facing scenario tests. Each delegates to the focused
// invariant test that owns that abuse case so -list/-json evidence cannot pass
// through a broad or unrelated test selection.
func TestDesktopLoadCredentialReplay(t *testing.T) {
	TestDesktopWebSocketRejectsWrongOriginAndCredentialReuse(t)
}
func TestDesktopLoadWrongSide(t *testing.T) {
	TestDesktopRelayStoreAuthorizationBindsSideSessionAgentAndEpoch(t)
}
func TestDesktopLoadWrongSession(t *testing.T) {
	TestDesktopRelayStoreAuthorizationBindsSideSessionAgentAndEpoch(t)
}
func TestDesktopLoadWrongEpoch(t *testing.T) {
	TestDesktopRelayStoreAuthorizationBindsSideSessionAgentAndEpoch(t)
}
func TestDesktopLoadProcessSession33(t *testing.T) {
	TestDesktopServicePreAdmissionIsAtomicBeforeCredentialsAndPersistence(t)
}
func TestDesktopLoadHomeSession5(t *testing.T) {
	TestDesktopRelayHomeAndAgentLimitsRejectOnlyNewSession(t)
}
func TestDesktopLoadDuplicateOperator(t *testing.T) {
	TestDesktopRelayHomeAndAgentLimitsRejectOnlyNewSession(t)
}
func TestDesktopLoadFrameLimit(t *testing.T) {
	TestDesktopRelayRejectsDuplicateSideAndOversizedFrame(t)
}
func TestDesktopLoadRateLimit(t *testing.T) { TestDesktopRelayEnforcesPerSideRateLimit(t) }
func TestDesktopLoadQueueLimit(t *testing.T) {
	TestDesktopRelaySignalsBackpressureBeforeSlowConsumerTermination(t)
}
func TestDesktopLoadIdleExpiry(t *testing.T) { TestDesktopRelayIdleReadUsesStableIdleTimeoutReason(t) }
func TestDesktopLoadReconnectExpiry(t *testing.T) {
	TestDesktopRelayUsesReconnectWindowForUnpairedEpoch(t)
}
func TestDesktopLoadHardExpiry(t *testing.T) { TestDesktopRelayEnforcesHardExpiry(t) }
func TestDesktopLoadSlowConsumer(t *testing.T) {
	TestDesktopRelaySignalsBackpressureBeforeSlowConsumerTermination(t)
}
func TestDesktopLoadRevocation(t *testing.T) { TestDesktopTrustMutationRevokesEveryAffectedRelay(t) }
