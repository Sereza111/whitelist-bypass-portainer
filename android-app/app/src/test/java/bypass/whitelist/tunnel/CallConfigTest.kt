package bypass.whitelist.tunnel

import org.junit.Assert.assertEquals
import org.junit.Test

class CallConfigTest {

    @Test
    fun managedWbRefreshPreservesExplicitDcMode() {
        val existing = CallConfig.newWith("WB", "wbstream://old-room").copy(
            tunnelMode = TunnelMode.DC,
			tunnelModeExplicit = true,
            dualTrack = false,
            recoveryProfile = "client-profile",
        )

        val refreshed = CallConfig.managedInvite(
            existing = existing,
            name = "WB",
            url = "wbstream://new-room",
            profile = "client-profile",
            key = "abcdefghijklmnopqrstuvwxyz123456",
            generation = 2,
            syncUrl = "https://panel.example.test/api/client-profiles/client-profile/invite",
        )

        assertEquals(TunnelMode.DC, refreshed.tunnelMode)
        assertEquals(false, refreshed.dualTrack)
        assertEquals("wbstream://new-room", refreshed.url)
        assertEquals(2, refreshed.recoveryGeneration)
    }

	@Test
	fun managedWbRefreshPreservesExplicitVideoMode() {
		val existing = CallConfig.newWith("WB", "wbstream://old-room").copy(
			tunnelMode = TunnelMode.VIDEO,
			tunnelModeExplicit = true,
			recoveryProfile = "client-profile",
		)

		val refreshed = CallConfig.managedInvite(
			existing = existing,
			name = "WB",
			url = "wbstream://new-room",
			profile = "client-profile",
			key = "abcdefghijklmnopqrstuvwxyz123456",
			generation = 2,
			syncUrl = null,
		)

		assertEquals(TunnelMode.VIDEO, refreshed.tunnelMode)
		assertEquals(true, refreshed.tunnelModeExplicit)
	}

	@Test
	fun legacyManagedWbVideoMigratesToSmart() {
		val legacy = CallConfig.newWith("WB", "wbstream://old-room").copy(
			tunnelMode = TunnelMode.VIDEO,
			tunnelModeExplicit = false,
			recoveryProfile = "client-profile",
		)

		val refreshed = CallConfig.managedInvite(
			existing = legacy,
			name = "WB",
			url = "wbstream://new-room",
			profile = "client-profile",
			key = "abcdefghijklmnopqrstuvwxyz123456",
			generation = 2,
			syncUrl = null,
		)

		assertEquals(TunnelMode.SMART, refreshed.tunnelMode)
		assertEquals(TunnelMode.SMART, legacy.migrateManagedWbTransportDefault().tunnelMode)
	}

	@Test
	fun startupMigrationDoesNotOverrideExplicitOrManualVideo() {
		val explicit = CallConfig.newWith("WB", "wbstream://room-id").copy(
			tunnelMode = TunnelMode.VIDEO,
			tunnelModeExplicit = true,
			recoveryProfile = "client-profile",
		)
		val manual = CallConfig.newWith("WB", "wbstream://room-id").copy(
			tunnelMode = TunnelMode.VIDEO,
			tunnelModeExplicit = false,
			recoveryProfile = null,
		)

		assertEquals(TunnelMode.VIDEO, explicit.migrateManagedWbTransportDefault().tunnelMode)
		assertEquals(TunnelMode.VIDEO, manual.migrateManagedWbTransportDefault().tunnelMode)
	}

    @Test
    fun firstManagedWbInviteDefaultsToSmartDualTrack() {
        val created = CallConfig.managedInvite(
            existing = null,
            name = "WB",
            url = "wbstream://new-room",
            profile = "client-profile",
            key = "abcdefghijklmnopqrstuvwxyz123456",
            generation = 1,
            syncUrl = null,
        )

        assertEquals(TunnelMode.SMART, created.tunnelMode)
        assertEquals(true, created.dualTrack)
    }

	@Test
	fun newWbDestinationDefaultsToSmartWithoutChangingOtherProviders() {
		val wb = CallConfig.newWith("WB", "wbstream://room-id")
		val vk = CallConfig.newWith("VK", "https://vk.com/call/example")

		assertEquals(TunnelMode.SMART, wb.tunnelMode)
		assertEquals(true, wb.dualTrack)
		assertEquals(null, vk.tunnelMode)
		assertEquals(null, vk.dualTrack)
	}
}
